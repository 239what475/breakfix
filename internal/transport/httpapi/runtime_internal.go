package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/execution"
	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

const (
	internalRuntimeRole = config.InternalWorkerRuntime
	runtimeMinLease     = 5 * time.Second
	runtimeMaxLease     = 2 * time.Minute
)

type runtimeClaimRequest struct {
	WorkerID       string `json:"worker_id"`
	LeaseTTLMillis int64  `json:"lease_ttl_millis"`
}

type runtimeRenewRequest struct {
	runtime.Credential
	LeaseTTLMillis int64 `json:"lease_ttl_millis"`
}

type runtimeBuildCompleteRequest struct {
	runtime.Credential
	Output execution.BuildOutput `json:"output"`
}

type runtimeArtifactCompleteRequest struct {
	runtime.Credential
	Artifact execution.ArtifactReference `json:"artifact"`
}

type runtimeVerificationEnvironmentRequest struct {
	runtime.Credential
	Environment execution.VerificationEnvironment `json:"environment"`
}

type runtimeVerificationCompleteRequest struct {
	runtime.Credential
	Report execution.VerificationReport `json:"report"`
}

type runtimeArtifactFailureRequest struct {
	runtime.Credential
	Failure runtime.Failure               `json:"failure"`
	Report  *execution.VerificationReport `json:"report,omitempty"`
}

type runtimeInfrastructureFailureRequest struct {
	runtime.Credential
	Failure runtime.Failure `json:"failure"`
}

type runtimeResourceReapClaimRequest struct {
	WorkerID       string `json:"worker_id"`
	LeaseTTLMillis int64  `json:"lease_ttl_millis"`
}

type runtimeResourceReapCompleteRequest struct {
	Claim   runtime.ReapClaim `json:"claim"`
	Failure string            `json:"failure,omitempty"`
}

type claimedRuntimeAction struct {
	Context         runtime.Context
	generationClaim *generation.Claim
}

// InternalClaimRuntimeAction returns one fixed external action. Server Agent
// phases are never visible here; Catalog and authoring candidates share this
// closed Runtime Worker boundary without a string-dispatched task queue.
func (h *Handler) InternalClaimRuntimeAction(c *gin.Context) {
	var request runtimeClaimRequest
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	leaseTTL := time.Duration(request.LeaseTTLMillis) * time.Millisecond
	if strings.TrimSpace(request.WorkerID) == "" || leaseTTL < runtimeMinLease || leaseTTL > runtimeMaxLease {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "worker_id and a lease between 5 seconds and 2 minutes are required"})
		return
	}
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "runtime action store is unavailable"})
		return
	}
	action, err := claimFirstAvailable(&h.runtimeActions, func(lane runtimeClaimLane) (*runtime.Context, error) {
		return h.claimRuntimeAction(c.Request.Context(), lane, request.WorkerID, leaseTTL, time.Now().UTC())
	})
	if err != nil {
		h.writeInternalRuntimeError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Action *runtime.Context `json:"action,omitempty"`
	}{Action: action})
}

func (h *Handler) InternalClaimRuntimeResourceReap(c *gin.Context) {
	var request runtimeResourceReapClaimRequest
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	leaseTTL := time.Duration(request.LeaseTTLMillis) * time.Millisecond
	if strings.TrimSpace(request.WorkerID) == "" || leaseTTL < runtimeMinLease || leaseTTL > runtimeMaxLease {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "worker_id and a lease between 5 seconds and 2 minutes are required"})
		return
	}
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "runtime resource reap store is unavailable"})
		return
	}
	claim, err := claimFirstAvailable(&h.runtimeReaps, func(lane runtimeClaimLane) (*runtime.ReapClaim, error) {
		return h.claimRuntimeResourceReap(c.Request.Context(), lane, request.WorkerID, leaseTTL, time.Now().UTC())
	})
	if err != nil {
		h.writeInternalRuntimeError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Claim *runtime.ReapClaim `json:"claim,omitempty"`
	}{Claim: claim})
}

func (h *Handler) claimRuntimeAction(ctx context.Context, lane runtimeClaimLane, workerID string, leaseTTL time.Duration, now time.Time) (*runtime.Context, error) {
	switch lane {
	case runtimeClaimGeneration:
		claim, err := h.db.Generation.ClaimGenerationWorkflow(ctx, workerID, leaseTTL, now)
		if err != nil || claim == nil {
			return nil, err
		}
		return h.db.Generation.LoadGenerationRuntimeAction(ctx, *claim, now)
	case runtimeClaimCatalog:
		return h.db.Catalog.ClaimCatalogRuntimeAction(ctx, workerID, leaseTTL, now)
	default:
		return nil, runtime.ErrActionNotFound
	}
}

func (h *Handler) claimRuntimeResourceReap(ctx context.Context, lane runtimeClaimLane, workerID string, leaseTTL time.Duration, now time.Time) (*runtime.ReapClaim, error) {
	switch lane {
	case runtimeClaimGeneration:
		return h.db.Generation.ClaimGenerationResourceReap(ctx, workerID, leaseTTL, now)
	case runtimeClaimCatalog:
		return h.db.Catalog.ClaimCatalogResourceReap(ctx, workerID, leaseTTL, now)
	default:
		return nil, runtime.ErrActionNotFound
	}
}

func (h *Handler) InternalCompleteRuntimeResourceReap(c *gin.Context) {
	var request runtimeResourceReapCompleteRequest
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	if request.Claim.Valid() != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "runtime resource reap completion is invalid"})
		return
	}
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "runtime resource reap store is unavailable"})
		return
	}
	var err error
	switch request.Claim.Scope {
	case runtime.ScopeGenerationWorkflow:
		err = h.db.Generation.CompleteGenerationResourceReap(c.Request.Context(), request.Claim, request.Failure, time.Now().UTC())
	case runtime.ScopeCatalogEntry:
		err = h.db.Catalog.CompleteCatalogResourceReap(c.Request.Context(), request.Claim, request.Failure, time.Now().UTC())
	default:
		err = runtime.ErrActionNotFound
	}
	if err != nil {
		h.writeInternalRuntimeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalRenewRuntimeAction(c *gin.Context) {
	var request runtimeRenewRequest
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	leaseTTL := time.Duration(request.LeaseTTLMillis) * time.Millisecond
	if leaseTTL < runtimeMinLease || leaseTTL > runtimeMaxLease {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "lease must be between 5 seconds and 2 minutes"})
		return
	}
	action, err := h.runtimeAction(c, request.Credential)
	if err == nil {
		switch action.Context.Identity.Scope {
		case runtime.ScopeGenerationWorkflow:
			err = h.db.Generation.RenewGenerationLease(c.Request.Context(), *action.generationClaim, leaseTTL, time.Now().UTC())
		case runtime.ScopeCatalogEntry, runtime.ScopeCatalogCommit:
			err = h.db.Catalog.RenewCatalogRuntimeLease(c.Request.Context(), request.Credential, leaseTTL, time.Now().UTC())
		default:
			err = runtime.ErrActionNotFound
		}
	}
	if err != nil {
		h.writeInternalRuntimeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalDownloadRuntimeArchive(c *gin.Context) {
	var credential runtime.Credential
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &credential) {
		return
	}
	action, err := h.runtimeAction(c, credential)
	if err == nil && action.Context.Identity.State != runtime.StateBuilding {
		err = runtime.ErrLeaseLost
	}
	if err != nil {
		h.writeInternalRuntimeError(c, err)
		return
	}
	var archive []byte
	switch action.Context.Identity.Scope {
	case runtime.ScopeGenerationWorkflow:
		revision, readErr := h.db.Generation.GetCandidateRevision(c.Request.Context(), action.Context.Identity.CandidateID)
		if readErr != nil {
			err = readErr
		} else {
			archive, err = candidate.ReadArchive(revision.ArchivePath, revision.ArchiveSHA256)
		}
	case runtime.ScopeCatalogEntry:
		entry, readErr := h.db.Catalog.Entry(c.Request.Context(), action.Context.Identity.OwnerID)
		if readErr != nil {
			err = readErr
		} else {
			archive, err = appcatalog.ReadStagedEntryArchive(h.dataDir, action.Context.Identity.ParentID, *entry)
		}
	default:
		err = runtime.ErrActionNotFound
	}
	if err != nil {
		h.writeInternalRuntimeError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Archive []byte `json:"archive"`
		SHA256  string `json:"sha256"`
	}{Archive: archive, SHA256: action.Context.ArchiveSHA256})
}

func (h *Handler) InternalCompleteRuntimeBuild(c *gin.Context) {
	var request runtimeBuildCompleteRequest
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	h.completeRuntimeAction(c, request.Credential, runtime.StateBuilding, func(action *claimedRuntimeAction) error {
		if err := h.validateRuntimeBuildOutput(action.Context, request.Output); err != nil {
			return h.reportRuntimeArtifactFailure(c.Request.Context(), action, runtime.Failure{Class: runtime.FailureArtifact, Code: "BUILD_OUTPUT_INVALID", Summary: err.Error()}, nil)
		}
		switch action.Context.Identity.Scope {
		case runtime.ScopeGenerationWorkflow:
			return h.db.Generation.CompleteGenerationBuild(c.Request.Context(), *action.generationClaim, request.Output, time.Now().UTC())
		case runtime.ScopeCatalogEntry:
			return h.db.Catalog.CompleteCatalogBuild(c.Request.Context(), action.Context, request.Output, time.Now().UTC())
		default:
			return runtime.ErrActionNotFound
		}
	})
}

func (h *Handler) InternalCompleteRuntimeArtifactPublish(c *gin.Context) {
	var request runtimeArtifactCompleteRequest
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	h.completeRuntimeAction(c, request.Credential, runtime.StateArtifactPublishing, func(action *claimedRuntimeAction) error {
		if err := h.validateRuntimeStagingArtifact(action.Context, request.Artifact); err != nil {
			return h.reportRuntimeArtifactFailure(c.Request.Context(), action, runtime.Failure{Class: runtime.FailureArtifact, Code: "ARTIFACT_REFERENCE_INVALID", Summary: err.Error()}, nil)
		}
		switch action.Context.Identity.Scope {
		case runtime.ScopeGenerationWorkflow:
			return h.db.Generation.CompleteGenerationArtifactPublish(c.Request.Context(), *action.generationClaim, request.Artifact, time.Now().UTC())
		case runtime.ScopeCatalogEntry:
			return h.db.Catalog.CompleteCatalogArtifactPublish(c.Request.Context(), action.Context, request.Artifact, time.Now().UTC())
		default:
			return runtime.ErrActionNotFound
		}
	})
}

func (h *Handler) InternalRecordRuntimeVerificationEnvironment(c *gin.Context) {
	var request runtimeVerificationEnvironmentRequest
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	h.completeRuntimeAction(c, request.Credential, runtime.StateVerifying, func(action *claimedRuntimeAction) error {
		if err := request.Environment.Validate(action.Context.Snapshot.Runtime); err != nil {
			return h.reportRuntimeArtifactFailure(c.Request.Context(), action, runtime.Failure{Class: runtime.FailureArtifact, Code: "VERIFICATION_ENVIRONMENT_INVALID", Summary: err.Error()}, nil)
		}
		switch action.Context.Identity.Scope {
		case runtime.ScopeGenerationWorkflow:
			return h.db.Generation.RecordGenerationVerificationEnvironment(c.Request.Context(), *action.generationClaim, request.Environment, time.Now().UTC())
		case runtime.ScopeCatalogEntry:
			return h.db.Catalog.RecordCatalogVerificationEnvironment(c.Request.Context(), action.Context, request.Environment, time.Now().UTC())
		default:
			return runtime.ErrActionNotFound
		}
	})
}

func (h *Handler) InternalCompleteRuntimeVerification(c *gin.Context) {
	var request runtimeVerificationCompleteRequest
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	h.completeRuntimeAction(c, request.Credential, runtime.StateVerifying, func(action *claimedRuntimeAction) error {
		if err := request.Report.Validate(action.Context.Snapshot); err != nil {
			return h.reportRuntimeArtifactFailure(c.Request.Context(), action, runtime.Failure{Class: runtime.FailureArtifact, Code: "VERIFICATION_REPORT_INVALID", Summary: err.Error()}, nil)
		}
		if !request.Report.Passed {
			return h.reportRuntimeArtifactFailure(c.Request.Context(), action, runtime.Failure{Class: runtime.FailureArtifact, Code: "VERIFY_FAILED", Summary: request.Report.Summary}, &request.Report)
		}
		switch action.Context.Identity.Scope {
		case runtime.ScopeGenerationWorkflow:
			return h.db.Generation.CompleteGenerationVerification(c.Request.Context(), *action.generationClaim, request.Report, time.Now().UTC())
		case runtime.ScopeCatalogEntry:
			return h.db.Catalog.CompleteCatalogVerification(c.Request.Context(), action.Context, request.Report, time.Now().UTC())
		default:
			return runtime.ErrActionNotFound
		}
	})
}

func (h *Handler) InternalRecordRuntimeFinalArtifact(c *gin.Context) {
	var request runtimeArtifactCompleteRequest
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	h.completeRuntimeAction(c, request.Credential, runtime.StateArtifactFinalizing, func(action *claimedRuntimeAction) error {
		if err := h.validateRuntimeFinalArtifact(action.Context, request.Artifact); err != nil {
			return h.reportRuntimeArtifactFailure(c.Request.Context(), action, runtime.Failure{Class: runtime.FailureArtifact, Code: "FINAL_ARTIFACT_INVALID", Summary: err.Error()}, nil)
		}
		switch action.Context.Identity.Scope {
		case runtime.ScopeGenerationWorkflow:
			return h.db.Generation.RecordGenerationScenarioPublicationResult(c.Request.Context(), *action.generationClaim, request.Artifact, time.Now().UTC())
		case runtime.ScopeCatalogCommit:
			return h.db.Catalog.CompleteCatalogScenarioPublication(c.Request.Context(), action.Context, request.Artifact, time.Now().UTC())
		default:
			return runtime.ErrActionNotFound
		}
	})
}

func (h *Handler) InternalReportRuntimeInfrastructureFailure(c *gin.Context) {
	var request runtimeInfrastructureFailureRequest
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	h.completeRuntimeAction(c, request.Credential, request.Identity.State, func(action *claimedRuntimeAction) error {
		if request.Failure.Class != runtime.FailureInfrastructure || request.Failure.Validate() != nil {
			return errors.New("runtime infrastructure failure is invalid")
		}
		return h.reportRuntimeInfrastructureFailure(c.Request.Context(), action, request.Failure)
	})
}

func (h *Handler) InternalReportRuntimeArtifactFailure(c *gin.Context) {
	var request runtimeArtifactFailureRequest
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	h.completeRuntimeAction(c, request.Credential, request.Identity.State, func(action *claimedRuntimeAction) error {
		if request.Failure.Class != runtime.FailureArtifact || request.Failure.Validate() != nil {
			return errors.New("runtime artifact failure is invalid")
		}
		return h.reportRuntimeArtifactFailure(c.Request.Context(), action, request.Failure, request.Report)
	})
}

func (h *Handler) completeRuntimeAction(c *gin.Context, credential runtime.Credential, expected runtime.State, complete func(*claimedRuntimeAction) error) {
	action, err := h.runtimeAction(c, credential)
	if err == nil && action.Context.Identity.State != expected {
		err = runtime.ErrLeaseLost
	}
	if err == nil {
		err = complete(action)
	}
	if err != nil {
		h.writeInternalRuntimeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) runtimeAction(c *gin.Context, credential runtime.Credential) (*claimedRuntimeAction, error) {
	if h.db == nil || !credential.Valid() || strings.TrimSpace(c.Param("id")) == "" || credential.Identity.OwnerID != c.Param("id") {
		return nil, errors.New("valid runtime action credentials are required")
	}
	now := time.Now().UTC()
	switch credential.Identity.Scope {
	case runtime.ScopeGenerationWorkflow:
		claim, err := h.db.Generation.GetGenerationClaim(c.Request.Context(), credential.Identity.OwnerID, generation.LeaseCredential{
			StateVersion: credential.Identity.StateVersion, LeaseOwner: credential.LeaseOwner,
		}, now)
		if err != nil {
			return nil, err
		}
		action, err := h.db.Generation.LoadGenerationRuntimeAction(c.Request.Context(), *claim, now)
		if err != nil {
			return nil, err
		}
		if action.Identity != credential.Identity {
			return nil, runtime.ErrLeaseLost
		}
		return &claimedRuntimeAction{Context: *action, generationClaim: claim}, nil
	case runtime.ScopeCatalogEntry, runtime.ScopeCatalogCommit:
		action, err := h.db.Catalog.GetCatalogRuntimeAction(c.Request.Context(), credential, now)
		if err != nil {
			return nil, err
		}
		return &claimedRuntimeAction{Context: *action}, nil
	default:
		return nil, runtime.ErrActionNotFound
	}
}

func (h *Handler) reportRuntimeInfrastructureFailure(ctx context.Context, action *claimedRuntimeAction, failure runtime.Failure) error {
	switch action.Context.Identity.Scope {
	case runtime.ScopeGenerationWorkflow:
		_, err := h.db.Generation.ReportGenerationInfrastructureFailure(ctx, *action.generationClaim, generationStateForRuntime(action.Context.Identity.State), generation.Failure{
			Class: generation.FailureInfrastructure, Code: failure.Code, Summary: failure.Summary,
		}, time.Now().UTC())
		return err
	case runtime.ScopeCatalogEntry, runtime.ScopeCatalogCommit:
		return h.db.Catalog.ReportCatalogRuntimeInfrastructureFailure(ctx, action.Context, failure, time.Now().UTC())
	default:
		return runtime.ErrActionNotFound
	}
}

func (h *Handler) reportRuntimeArtifactFailure(ctx context.Context, action *claimedRuntimeAction, failure runtime.Failure, report *execution.VerificationReport) error {
	switch action.Context.Identity.Scope {
	case runtime.ScopeGenerationWorkflow:
		return h.db.Generation.ReportGenerationArtifactFailure(ctx, *action.generationClaim, generationStateForRuntime(action.Context.Identity.State), generation.Failure{
			Class: generation.FailureArtifact, Code: failure.Code, Summary: failure.Summary,
		}, report, time.Now().UTC())
	case runtime.ScopeCatalogEntry, runtime.ScopeCatalogCommit:
		return h.db.Catalog.ReportCatalogRuntimeArtifactFailure(ctx, action.Context, failure, report, time.Now().UTC())
	default:
		return runtime.ErrActionNotFound
	}
}

func generationStateForRuntime(state runtime.State) generation.WorkflowState {
	if state == runtime.StateArtifactFinalizing {
		return generation.StateScenarioPublishing
	}
	return generation.WorkflowState(state)
}

func (h *Handler) validateRuntimeBuildOutput(action runtime.Context, output execution.BuildOutput) error {
	if err := output.Validate(action.Snapshot.Runtime); err != nil {
		return err
	}
	switch action.Snapshot.Runtime {
	case scenario.RuntimeK8s:
		expected, err := candidate.BuildOCIRepository(h.registryRepository, action.Identity.OwnerID, action.Identity.CandidateID, action.Identity.StateVersion)
		if err != nil {
			return err
		}
		actual, err := candidate.OCIRepository(output.OCIReference)
		if err != nil {
			return err
		}
		if actual != expected {
			return errors.New("K8s build artifact OCI repository does not belong to this runtime action")
		}
		return nil
	case scenario.RuntimeNode:
		if output.Incus == nil || output.Incus.Project != h.incusConfig.BuildProject || output.Incus.WorkflowID != action.Identity.OwnerID ||
			output.Incus.CandidateRevisionID != action.Identity.CandidateID || output.Incus.Attempt != action.Identity.StateVersion {
			return errors.New("node build output does not match its fenced runtime action")
		}
		return nil
	default:
		return errors.New("runtime action has an unsupported build runtime")
	}
}

func (h *Handler) writeInternalRuntimeError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, runtime.ErrActionNotFound), errors.Is(err, generation.ErrWorkflowNotFound), errors.Is(err, generation.ErrCandidateNotFound), errors.Is(err, agent.ErrNotFound), errors.Is(err, generation.ErrWorkspaceNotFound), errors.Is(err, runnable.ErrSourceNotFound), errors.Is(err, runnable.ErrOutputNotFound):
		status = http.StatusNotFound
	case errors.Is(err, runtime.ErrLeaseLost), errors.Is(err, generation.ErrLeaseLost), errors.Is(err, runnable.ErrActionLeaseLost):
		status = http.StatusConflict
	case strings.Contains(err.Error(), "unavailable"):
		status = http.StatusServiceUnavailable
	}
	c.JSON(status, api.ErrorResponse{Error: err.Error()})
}
