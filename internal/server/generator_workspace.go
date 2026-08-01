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
	"github.com/breakfix/breakfix/internal/generation"
	"github.com/breakfix/breakfix/internal/generator"
	"github.com/breakfix/breakfix/internal/opensandbox"
	"github.com/breakfix/breakfix/internal/workspace"
	"github.com/gin-gonic/gin"
)

const generatorWorkspaceCleanupTimeout = 2 * time.Minute

type internalGeneratorReadRequest struct {
	generation.LeaseCredential
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

type internalGeneratorWriteRequest struct {
	generation.LeaseCredential
	Path    string `json:"path"`
	Content string `json:"content"`
}

type internalGeneratorExecuteRequest struct {
	generation.LeaseCredential
	Command string `json:"command"`
}

// InternalGeneratorContext creates or reconnects the Server-owned workspace
// for the current Generator agent run. The Generate Worker never receives a
// sandbox identifier, PVC name, or provider credential.
func (h *Handler) InternalGeneratorContext(c *gin.Context) {
	var credential generation.LeaseCredential
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &credential) {
		return
	}
	claim, record, err := h.generatorWorkspaceForGenerationClaim(c.Request.Context(), c.Param("id"), credential, true)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	if err := h.materializeGenerationWorkspace(c.Request.Context(), record, claim); err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	context, err := h.db.LoadGenerationContext(c.Request.Context(), *claim, time.Now().UTC())
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	c.JSON(http.StatusOK, generator.WorkspaceContext{Plan: context.Plan, Feedback: context.Feedback})
}

func (h *Handler) InternalGeneratorReadFile(c *gin.Context) {
	var request internalGeneratorReadRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	_, record, err := h.generatorWorkspaceForGenerationClaim(c.Request.Context(), c.Param("id"), request.LeaseCredential, false)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	if err := opensandbox.ValidateWorkspacePath(request.Path); err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	content, err := h.generatorSandbox.ReadFile(c.Request.Context(), record.SandboxID, opensandbox.WorkspacePath(request.Path))
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	c.JSON(http.StatusOK, generator.FileReadResponse{Content: selectWorkspaceLines(string(content), request.Offset, request.Limit)})
}

func (h *Handler) InternalGeneratorWriteFile(c *gin.Context) {
	var request internalGeneratorWriteRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	_, record, err := h.generatorWorkspaceForGenerationClaim(c.Request.Context(), c.Param("id"), request.LeaseCredential, false)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	if err := opensandbox.ValidateWorkspacePath(request.Path); err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	if err := h.generatorSandbox.WriteFile(c.Request.Context(), record.SandboxID, opensandbox.WorkspacePath(request.Path), []byte(request.Content), 0o644); err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalGeneratorArchiveWorkspace(c *gin.Context) {
	var credential generation.LeaseCredential
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &credential) {
		return
	}
	_, record, err := h.generatorWorkspaceForGenerationClaim(c.Request.Context(), c.Param("id"), credential, false)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	archive, err := h.generatorSandbox.ArchiveWorkspace(c.Request.Context(), record.SandboxID)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	c.JSON(http.StatusOK, generator.ArchiveResponse{Archive: archive})
}

// InternalGeneratorExecute proxies a streaming command to the workspace. It
// rechecks the workflow lease while the remote command is running, fencing a
// worker that lost ownership before it can keep changing the workspace.
func (h *Handler) InternalGeneratorExecute(c *gin.Context) {
	var request internalGeneratorExecuteRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	claim, record, err := h.generatorWorkspaceForGenerationClaim(c.Request.Context(), c.Param("id"), request.LeaseCredential, false)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	if strings.TrimSpace(request.Command) == "" {
		h.writeInternalGenerationError(c, errors.New("generator command is required"))
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
	go h.monitorGenerationCommandLease(execCtx, done, cancel, *claim, &leaseErr, &leaseMu)
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

func (h *Handler) generatorWorkspaceForGenerationClaim(ctx context.Context, workflowID string, credential generation.LeaseCredential, ensure bool) (*generation.Claim, *workspace.Record, error) {
	if h.db == nil || h.generatorSandbox == nil || h.generatorWorkspace == nil {
		return nil, nil, errors.New("generator workspace runtime is unavailable")
	}
	if strings.TrimSpace(workflowID) == "" || !credential.Valid() {
		return nil, nil, errors.New("generation workflow lease credentials are required")
	}
	claim, err := h.db.GetGenerationClaim(ctx, workflowID, credential, time.Now().UTC())
	if err != nil {
		return nil, nil, err
	}
	if claim.Workflow.State != generation.StateGenerating || strings.TrimSpace(claim.Workflow.ActiveAgentRunID) == "" {
		return nil, nil, errors.New("generation workflow is not running a generator")
	}
	run, err := h.db.GetRun(ctx, claim.Workflow.ActiveAgentRunID)
	if err != nil {
		return nil, nil, err
	}
	if run.Status != agentruntime.RunRunning || run.Purpose != generator.GeneratorPurpose || run.OwnerKind != "generation-workflow" || run.OwnerRef != claim.Workflow.ID || strings.TrimSpace(run.SessionID) == "" {
		return nil, nil, errors.New("generation workflow has no current generator agent run")
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

func (h *Handler) materializeGenerationWorkspace(ctx context.Context, record *workspace.Record, claim *generation.Claim) error {
	if record == nil || claim == nil || strings.TrimSpace(record.SandboxID) == "" {
		return errors.New("generation workspace record is incomplete")
	}
	var archive []byte
	if candidateID := strings.TrimSpace(claim.Workflow.CandidateRevisionID); candidateID != "" {
		revision, err := h.db.GetCandidateRevision(ctx, candidateID)
		if err != nil {
			return fmt.Errorf("read generator repair candidate: %w", err)
		}
		if revision.AuthoringSessionID != claim.Workflow.AuthoringSessionID || revision.AuthoringRevision > claim.Workflow.AuthoringRevision {
			return errors.New("generator repair candidate does not belong to workflow lineage")
		}
		archive, err = candidate.ReadArchive(revision.ArchivePath, revision.ArchiveSHA256)
		if err != nil {
			return fmt.Errorf("read generator repair archive: %w", err)
		}
	}
	return h.generatorSandbox.ResetWorkspace(ctx, record.SandboxID, archive)
}

func (h *Handler) monitorGenerationCommandLease(ctx context.Context, done <-chan struct{}, cancel context.CancelFunc, claim generation.Claim, target *error, mu *sync.Mutex) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			if _, err := h.db.GetGenerationClaim(context.Background(), claim.Workflow.ID, claim.LeaseCredential, time.Now().UTC()); err != nil {
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

func (h *Handler) scheduleGeneratorWorkspaceCleanup(runID string) {
	if h.generatorWorkspace == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), generatorWorkspaceCleanupTimeout)
		defer cancel()
		_ = h.generatorWorkspace.Cleanup(ctx, runID)
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

func (h *Handler) writeInternalGenerationError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, generation.ErrNotFound), errors.Is(err, candidate.ErrNotFound), errors.Is(err, agentruntime.ErrNotFound), errors.Is(err, workspace.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, generation.ErrLeaseLost):
		status = http.StatusConflict
	case strings.Contains(err.Error(), "unavailable"):
		status = http.StatusServiceUnavailable
	}
	c.JSON(status, api.ErrorResponse{Error: err.Error()})
}
