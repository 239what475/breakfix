package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/generator"
	"github.com/breakfix/breakfix/internal/opensandbox"
	"github.com/breakfix/breakfix/internal/workspace"
	"github.com/gin-gonic/gin"
)

type internalGeneratorReadRequest struct {
	generator.LeaseCredential
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

type internalGeneratorWriteRequest struct {
	generator.LeaseCredential
	Path    string `json:"path"`
	Content string `json:"content"`
}

type internalGeneratorExecuteRequest struct {
	generator.LeaseCredential
	Command string `json:"command"`
}

type internalGeneratorFinalizeRequest struct {
	generator.LeaseCredential
	Archive []byte `json:"archive"`
}

// InternalGeneratorContext creates or reconnects the single Server-owned
// workspace for a valid Generator Run. The Worker sees neither sandbox ID
// nor provider credentials.
func (h *Handler) InternalGeneratorContext(c *gin.Context) {
	var credential generator.LeaseCredential
	if !h.decodeInternalAgentRequest(c, &credential) {
		return
	}
	claim, record, err := h.generatorWorkspaceForClaim(c.Request.Context(), c.Param("id"), credential, true)
	if err != nil {
		h.writeInternalGeneratorError(c, err)
		return
	}
	runRecord, err := h.db.GetGeneratorRun(c.Request.Context(), claim.Run.ID)
	if err != nil {
		h.writeInternalGeneratorError(c, err)
		return
	}
	input, err := generator.DecodeRunInput(claim.Run.Input)
	if err != nil || input.AuthoringSessionID != runRecord.AuthoringSessionID || input.Revision != runRecord.AuthoringRevision || input.SeedCandidateRevisionID != runRecord.SeedCandidateRevisionID {
		h.writeInternalGeneratorError(c, errors.New("generator run input does not match its durable record"))
		return
	}
	if !runRecord.WorkspaceInitialized {
		if err := h.materializeGeneratorWorkspace(c.Request.Context(), record, runRecord); err != nil {
			h.writeInternalGeneratorError(c, err)
			return
		}
		if err := h.db.MarkGeneratorWorkspaceInitialized(c.Request.Context(), *claim); err != nil {
			h.writeInternalGeneratorError(c, err)
			return
		}
	}
	revision, err := h.db.GetAuthoringRevision(c.Request.Context(), runRecord.AuthoringSessionID, runRecord.AuthoringRevision)
	if err != nil {
		h.writeInternalGeneratorError(c, err)
		return
	}
	c.JSON(http.StatusOK, generator.WorkspaceContext{
		Plan:     revision.Plan,
		Feedback: input.Feedback,
	})
}

func (h *Handler) InternalGeneratorReadFile(c *gin.Context) {
	var request internalGeneratorReadRequest
	if !h.decodeInternalAgentRequest(c, &request) {
		return
	}
	_, record, err := h.generatorWorkspaceForClaim(c.Request.Context(), c.Param("id"), request.LeaseCredential, false)
	if err != nil {
		h.writeInternalGeneratorError(c, err)
		return
	}
	if err := opensandbox.ValidateWorkspacePath(request.Path); err != nil {
		h.writeInternalGeneratorError(c, err)
		return
	}
	content, err := h.generatorSandbox.ReadFile(c.Request.Context(), record.SandboxID, opensandbox.WorkspacePath(request.Path))
	if err != nil {
		h.writeInternalGeneratorError(c, err)
		return
	}
	c.JSON(http.StatusOK, generator.FileReadResponse{Content: selectWorkspaceLines(string(content), request.Offset, request.Limit)})
}

func (h *Handler) InternalGeneratorWriteFile(c *gin.Context) {
	var request internalGeneratorWriteRequest
	if !h.decodeInternalAgentRequest(c, &request) {
		return
	}
	_, record, err := h.generatorWorkspaceForClaim(c.Request.Context(), c.Param("id"), request.LeaseCredential, false)
	if err != nil {
		h.writeInternalGeneratorError(c, err)
		return
	}
	if err := opensandbox.ValidateWorkspacePath(request.Path); err != nil {
		h.writeInternalGeneratorError(c, err)
		return
	}
	if err := h.generatorSandbox.WriteFile(c.Request.Context(), record.SandboxID, opensandbox.WorkspacePath(request.Path), []byte(request.Content), 0o644); err != nil {
		h.writeInternalGeneratorError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalGeneratorArchiveWorkspace(c *gin.Context) {
	var credential generator.LeaseCredential
	if !h.decodeInternalAgentRequest(c, &credential) {
		return
	}
	_, record, err := h.generatorWorkspaceForClaim(c.Request.Context(), c.Param("id"), credential, false)
	if err != nil {
		h.writeInternalGeneratorError(c, err)
		return
	}
	archive, err := h.generatorSandbox.ArchiveWorkspace(c.Request.Context(), record.SandboxID)
	if err != nil {
		h.writeInternalGeneratorError(c, err)
		return
	}
	c.JSON(http.StatusOK, generator.ArchiveResponse{Archive: archive})
}

// InternalGeneratorExecute proxies a streaming command to the workspace. A
// periodic lease check cancels the remote request as soon as an attempt loses
// ownership, rather than letting a stale Worker continue changing files.
func (h *Handler) InternalGeneratorExecute(c *gin.Context) {
	var request internalGeneratorExecuteRequest
	if !h.decodeInternalAgentRequest(c, &request) {
		return
	}
	claim, record, err := h.generatorWorkspaceForClaim(c.Request.Context(), c.Param("id"), request.LeaseCredential, false)
	if err != nil {
		h.writeInternalGeneratorError(c, err)
		return
	}
	if strings.TrimSpace(request.Command) == "" {
		h.writeInternalGeneratorError(c, errors.New("generator command is required"))
		return
	}

	c.Header("Content-Type", "application/x-ndjson")
	c.Header("Cache-Control", "no-cache")
	c.Status(http.StatusOK)
	encoder := json.NewEncoder(c.Writer)
	flusher, _ := c.Writer.(http.Flusher)
	write := func(event generator.ExecuteEvent) error {
		if err := encoder.Encode(event); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	execCtx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	var leaseErr error
	var leaseMu sync.Mutex
	done := make(chan struct{})
	go h.monitorGeneratorCommandLease(execCtx, done, cancel, claim, &leaseErr, &leaseMu)
	streamed := false
	result, runErr := h.generatorSandbox.Execute(execCtx, record.SandboxID, request.Command, "/workspace", func(content string) error {
		streamed = streamed || content != ""
		return write(generator.ExecuteEvent{Type: "stdout", Content: content})
	})
	close(done)
	leaseMu.Lock()
	currentLeaseErr := leaseErr
	leaseMu.Unlock()
	if currentLeaseErr != nil {
		_ = write(generator.ExecuteEvent{Type: "error", Error: currentLeaseErr.Error()})
		return
	}
	if runErr != nil {
		_ = write(generator.ExecuteEvent{Type: "error", Error: runErr.Error()})
		return
	}
	content := ""
	if !streamed {
		content = result.Output
	}
	_ = write(generator.ExecuteEvent{Type: "result", Content: content, ExitCode: &result.ExitCode})
}

func (h *Handler) generatorWorkspaceForClaim(ctx context.Context, runID string, credential generator.LeaseCredential, ensure bool) (*agentruntime.Claim, *workspace.Record, error) {
	if h.db == nil || h.generatorSandbox == nil || h.generatorWorkspace == nil {
		return nil, nil, errors.New("generator workspace runtime is unavailable")
	}
	if strings.TrimSpace(runID) == "" || !credential.Valid() {
		return nil, nil, errors.New("generator run lease credentials are required")
	}
	claim, err := h.getAgentClaim(ctx, runID, credential)
	if err != nil {
		return nil, nil, err
	}
	run := &claim.Run
	if run.Purpose != generator.RuntimePurpose || run.OwnerKind != "authoring-session" || strings.TrimSpace(run.OwnerRef) == "" || strings.TrimSpace(run.SessionID) == "" {
		return nil, nil, errors.New("agent run is not a current generator attempt")
	}
	generatorRun, err := h.db.GetGeneratorRun(ctx, run.ID)
	if err != nil {
		return nil, nil, err
	}
	if generatorRun.GeneratorSessionID != run.SessionID || generatorRun.AuthoringSessionID != run.OwnerRef {
		return nil, nil, errors.New("generator run does not match the claimed runtime session")
	}
	var record *workspace.Record
	if ensure {
		record, err = h.generatorWorkspace.Ensure(ctx, run.ID)
	} else {
		record, err = h.db.GetGeneratorWorkspace(ctx, run.ID)
	}
	if err != nil {
		return nil, nil, err
	}
	if record.State != workspace.StateActive || strings.TrimSpace(record.SandboxID) == "" {
		return nil, nil, errors.New("generator workspace is not active")
	}
	return claim, record, nil
}

func (h *Handler) materializeGeneratorWorkspace(ctx context.Context, workspaceRecord *workspace.Record, run *generator.Record) error {
	if workspaceRecord == nil || run == nil || strings.TrimSpace(workspaceRecord.SandboxID) == "" {
		return errors.New("generator workspace record is incomplete")
	}
	var archive []byte
	if candidateID := strings.TrimSpace(run.SeedCandidateRevisionID); candidateID != "" {
		revision, err := h.db.GetCandidateRevision(ctx, candidateID)
		if err != nil {
			return fmt.Errorf("read generator seed candidate: %w", err)
		}
		if revision.AuthoringSessionID != run.AuthoringSessionID || revision.AuthoringRevision > run.AuthoringRevision {
			return errors.New("generator seed candidate does not belong to the authoring lineage")
		}
		if revision.AuthoringRevision == run.AuthoringRevision && revision.GeneratorSessionID != run.GeneratorSessionID {
			return errors.New("generator repair seed does not belong to the generator session")
		}
		archive, err = candidate.ReadArchive(revision.ArchivePath, revision.ArchiveSHA256)
		if err != nil {
			return fmt.Errorf("read generator seed candidate archive: %w", err)
		}
	}
	if err := h.generatorSandbox.ResetWorkspace(ctx, workspaceRecord.SandboxID, archive); err != nil {
		return err
	}
	return nil
}

func (h *Handler) monitorGeneratorCommandLease(ctx context.Context, done <-chan struct{}, cancel context.CancelFunc, claim *agentruntime.Claim, target *error, mu *sync.Mutex) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			if err := h.db.ValidateLease(context.Background(), *claim, time.Now().UTC()); err != nil {
				mu.Lock()
				*target = err
				mu.Unlock()
				cancel()
				return
			}
		}
	}
}

func (h *Handler) StartGeneratorWorkspaceCleanup(ctx context.Context) {
	if h.generatorWorkspace == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			if err := h.generatorWorkspace.CleanupDue(ctx); err != nil && !errors.Is(err, context.Canceled) {
				// A deleting row remains durable and is retried on the next tick.
				// Do not include workspace or provider payloads in telemetry.
				_ = err
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func selectWorkspaceLines(content string, offset, limit int) string {
	if offset < 1 {
		offset = 1
	}
	lines := strings.SplitAfter(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	start := offset - 1
	if start >= len(lines) {
		return ""
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return strings.Join(lines[start:end], "")
}

func (h *Handler) writeInternalGeneratorError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, agentruntime.ErrNotFound), errors.Is(err, workspace.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, agentruntime.ErrLeaseLost):
		status = http.StatusConflict
	case strings.Contains(err.Error(), "unavailable"):
		status = http.StatusServiceUnavailable
	}
	c.JSON(status, api.ErrorResponse{Error: err.Error()})
}
