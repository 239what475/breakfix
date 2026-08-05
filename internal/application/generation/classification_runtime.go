package generation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	roadmapapp "github.com/breakfix/breakfix/internal/application/roadmap"
	"github.com/breakfix/breakfix/internal/domain/agent"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
	roadmapdomain "github.com/breakfix/breakfix/internal/domain/roadmap"
)

// ClassificationClaimStore is the narrow generation ownership check needed by
// every Classifier retrieval tool call.
type ClassificationClaimStore interface {
	GetGenerationClaim(context.Context, string, domain.LeaseCredential, time.Time) (*domain.Claim, error)
}

type ClassificationRunStore interface {
	GetRun(context.Context, string) (*agent.Run, error)
}

type ClassificationRoadmapStore interface {
	RoadmapRevision(context.Context, string) (*roadmapdomain.Revision, error)
}

// ClassificationRuntime provides the Classifier's read-only, immutable
// Roadmap retrieval tools directly inside Server. It replaces the former
// Worker-to-Server HTTP proxy and never exposes a Roadmap mutation method.
type ClassificationRuntime struct {
	claims  ClassificationClaimStore
	runs    ClassificationRunStore
	roadmap ClassificationRoadmapStore
	now     func() time.Time
}

func NewClassificationRuntime(claims ClassificationClaimStore, runs ClassificationRunStore, roadmap ClassificationRoadmapStore) (*ClassificationRuntime, error) {
	if claims == nil || runs == nil || roadmap == nil {
		return nil, errors.New("classification runtime requires generation claims, agent runs, and roadmap store")
	}
	return &ClassificationRuntime{claims: claims, runs: runs, roadmap: roadmap, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (r *ClassificationRuntime) SearchClassificationTopics(ctx context.Context, claim domain.Claim, search roadmapapp.TopicSearch) ([]roadmapapp.TopicMatch, error) {
	retrieval, err := r.retrieval(ctx, claim)
	if err != nil {
		return nil, err
	}
	return retrieval.SearchTopics(search)
}

func (r *ClassificationRuntime) ReadClassificationTopic(ctx context.Context, claim domain.Claim, id string) (*roadmapdomain.Topic, error) {
	retrieval, err := r.retrieval(ctx, claim)
	if err != nil {
		return nil, err
	}
	return retrieval.ReadTopic(id)
}

func (r *ClassificationRuntime) SearchClassificationTags(ctx context.Context, claim domain.Claim, search roadmapapp.TagSearch) ([]roadmapapp.TagMatch, error) {
	retrieval, err := r.retrieval(ctx, claim)
	if err != nil {
		return nil, err
	}
	return retrieval.SearchTags(search)
}

func (r *ClassificationRuntime) ReadClassificationTag(ctx context.Context, claim domain.Claim, id string) (*roadmapdomain.Tag, error) {
	retrieval, err := r.retrieval(ctx, claim)
	if err != nil {
		return nil, err
	}
	return retrieval.ReadTag(id)
}

func (r *ClassificationRuntime) retrieval(ctx context.Context, claim domain.Claim) (*roadmapapp.Retrieval, error) {
	if r == nil || !claim.Valid() || claim.Workflow.State != domain.StateClassifying || strings.TrimSpace(claim.Workflow.ActiveAgentRunID) == "" ||
		!roadmapdomain.ValidRevision(claim.Workflow.ClassificationRoadmapRevision) {
		return nil, errors.New("classifier retrieval requires an active Classifying claim and immutable roadmap revision")
	}
	current, err := r.claims.GetGenerationClaim(ctx, claim.Workflow.ID, claim.LeaseCredential, r.now())
	if err != nil {
		return nil, err
	}
	if current.Workflow.State != domain.StateClassifying || current.Workflow.ActiveAgentRunID != claim.Workflow.ActiveAgentRunID ||
		current.Workflow.ClassificationRoadmapRevision != claim.Workflow.ClassificationRoadmapRevision {
		return nil, domain.ErrLeaseLost
	}
	run, err := r.runs.GetRun(ctx, current.Workflow.ActiveAgentRunID)
	if err != nil {
		return nil, err
	}
	if run.Status != agent.RunRunning || run.Purpose != ClassifierPurpose || run.OwnerKind != "generation-workflow" || run.OwnerRef != current.Workflow.ID || run.SessionID != "" {
		return nil, domain.ErrLeaseLost
	}
	revision, err := r.roadmap.RoadmapRevision(ctx, current.Workflow.ClassificationRoadmapRevision)
	if err != nil {
		return nil, fmt.Errorf("read classifier roadmap revision: %w", err)
	}
	return roadmapapp.NewRetrieval(*revision)
}
