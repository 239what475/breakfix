package documentpractice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/audit"
	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
)

// DefaultBatchConcurrency is the deployment's Agent-chain throttle: the
// scheduler tops up in-flight workflows to this value.
const DefaultBatchConcurrency = 2

// MaxBatchConcurrency caps the throttle a batch may request. Capacity, not
// preference, bounds this: Worker replicas are sized against the paired
// threshold documented in the operations runbook.
const MaxBatchConcurrency = 8

// CorpusAnchor is one manifest heading of a corpus page.
type CorpusAnchor struct {
	ID    string
	Level int
}

// CorpusPage is one library page's corpus projection.
type CorpusPage struct {
	Path     string
	Title    string
	PageKind string
	Anchors  []CorpusAnchor
}

// BatchCorpus is the read-only corpus projection batch scopes resolve
// against. It is the library manifest as data; resolution never reads page
// bytes.
type BatchCorpus interface {
	// CorpusPages lists every page of the library sorted by path.
	CorpusPages() []CorpusPage
	// HasSection reports whether one exact tree node path exists.
	HasSection(section string) bool
	// WorkflowContext is the library identity every batch item's workflow
	// identity derives from.
	WorkflowContext() domain.DocumentContext
}

// BatchService resolves declarative scopes into deterministic batch items and
// persists batches with their creation audit. It deliberately does not run
// anything: ignition is the scheduler's job.
type BatchService struct {
	service *Service
	corpus  BatchCorpus
	now     func() time.Time
}

// NewBatchService requires the durable product service and a corpus view.
func NewBatchService(service *Service, corpus BatchCorpus) (*BatchService, error) {
	if service == nil || corpus == nil {
		return nil, errors.New("documentation batch service requires a store and a corpus")
	}
	return &BatchService{service: service, corpus: corpus, now: service.now}, nil
}

// CreateBatch resolves the scope into items, marks items whose workflow is
// already Published as Skipped, and creates the batch with its human audit in
// one transaction. The batch stays Pending: the scheduler owns every state
// after creation.
func (s *BatchService) CreateBatch(ctx context.Context, scope domain.BatchScope, concurrency int, action *audit.HumanAction) (domain.DocumentBatch, error) {
	if s == nil {
		return domain.DocumentBatch{}, errors.New("documentation batch service is not configured")
	}
	if err := scope.Validate(); err != nil {
		return domain.DocumentBatch{}, err
	}
	if concurrency == 0 {
		concurrency = DefaultBatchConcurrency
	}
	if concurrency < 1 || concurrency > MaxBatchConcurrency {
		return domain.DocumentBatch{}, fmt.Errorf("batch concurrency must be between 1 and %d", MaxBatchConcurrency)
	}
	if action == nil {
		return domain.DocumentBatch{}, errors.New("batch creation requires a human action audit")
	}
	if err := action.Validate(); err != nil {
		return domain.DocumentBatch{}, err
	}
	now := s.now().UTC()
	batch := domain.DocumentBatch{
		ID:          domain.NewBatchID(now),
		State:       domain.BatchPending,
		Scope:       scope,
		Concurrency: concurrency,
		CreatedBy:   action.UserID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	items, resolution, err := s.resolveBatchItems(ctx, batch, scope)
	if err != nil {
		return domain.DocumentBatch{}, err
	}
	batch.Resolution = resolution
	batch.TotalItems = len(items)
	// The audit row names the batch it created, with the resolved scope as the
	// durable explanation of what was about to roll out.
	enriched := *action
	enriched.TargetID = batch.ID
	detail, err := json.Marshal(map[string]any{"scope": scope, "concurrency": batch.Concurrency, "total_items": batch.TotalItems})
	if err != nil {
		return domain.DocumentBatch{}, err
	}
	enriched.Detail = detail
	if err := s.service.store.CreateBatch(ctx, batch, items, &enriched); err != nil {
		return domain.DocumentBatch{}, err
	}
	return batch, nil
}

// resolveBatchItems applies the deterministic anchor rule. Items are ordered
// by (page path, anchor) so a batch's ordering is corpus stable, never
// curated.
func (s *BatchService) resolveBatchItems(ctx context.Context, batch domain.DocumentBatch, scope domain.BatchScope) ([]domain.BatchItem, domain.BatchResolution, error) {
	pages := s.corpus.CorpusPages()
	byPath := make(map[string]CorpusPage, len(pages))
	for _, page := range pages {
		byPath[page.Path] = page
	}

	base := map[string]bool{}
	resolution := domain.BatchResolution{}
	switch scope.Kind {
	case domain.BatchScopeFull:
		for _, page := range pages {
			base[page.Path] = true
		}
	case domain.BatchScopeSections:
		for _, section := range scope.Sections {
			normalized := strings.TrimSuffix(strings.TrimSpace(section), "/")
			if !s.corpus.HasSection(normalized) {
				return nil, domain.BatchResolution{}, fmt.Errorf("batch scope section %q is not part of the library", normalized)
			}
			matched := false
			for _, page := range pages {
				if page.Path == normalized || strings.HasPrefix(page.Path, normalized+"/") {
					base[page.Path] = true
					matched = true
				}
			}
			if !matched {
				return nil, domain.BatchResolution{}, fmt.Errorf("batch scope section %q contains no pages", normalized)
			}
		}
	case domain.BatchScopePages:
		for _, pagePath := range scope.Pages {
			normalized := strings.TrimSuffix(strings.TrimSpace(pagePath), "/")
			if _, ok := byPath[normalized]; !ok {
				return nil, domain.BatchResolution{}, fmt.Errorf("batch scope page %q is not part of the library", normalized)
			}
			base[normalized] = true
		}
	}

	type pair struct{ page, anchor string }
	anchors := map[string]pair{}
	for _, page := range pages {
		if !base[page.Path] {
			continue
		}
		if page.PageKind == "index" {
			resolution.Excluded = append(resolution.Excluded, domain.BatchExcludedPage{PagePath: page.Path, Reason: domain.ExcludedIndex})
			continue
		}
		anchor, ok := firstLevel2(page)
		if !ok {
			resolution.Excluded = append(resolution.Excluded, domain.BatchExcludedPage{PagePath: page.Path, Reason: domain.ExcludedNoAnchor})
			continue
		}
		anchors[page.Path] = pair{page: page.Path, anchor: anchor.ID}
	}
	for _, override := range scope.Overrides {
		normalized := strings.TrimSuffix(override.PagePath, "/")
		page, ok := byPath[normalized]
		if !ok || !base[normalized] {
			return nil, domain.BatchResolution{}, fmt.Errorf("batch scope override page %q is outside the batch scope", normalized)
		}
		found := false
		for _, candidate := range page.Anchors {
			if candidate.ID == override.Anchor {
				found = true
				break
			}
		}
		if !found {
			return nil, domain.BatchResolution{}, fmt.Errorf("batch scope override anchor %q is absent from page %q", override.Anchor, normalized)
		}
		anchors[normalized] = pair{page: normalized, anchor: override.Anchor}
	}

	published, err := s.service.store.ListPublishedWorkflowAnchors(ctx, s.corpus.WorkflowContext())
	if err != nil {
		return nil, domain.BatchResolution{}, err
	}

	ordered := make([]pair, 0, len(anchors))
	for _, value := range anchors {
		ordered = append(ordered, value)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].page != ordered[j].page {
			return ordered[i].page < ordered[j].page
		}
		return ordered[i].anchor < ordered[j].anchor
	})

	items := make([]domain.BatchItem, 0, len(ordered))
	for ordinal, value := range ordered {
		page := byPath[value.page]
		context := s.corpus.WorkflowContext()
		context.PagePath = value.page
		context.Anchor = value.anchor
		item := domain.BatchItem{
			ID:         domain.NewBatchItemID(batch.ID, ordinal),
			BatchID:    batch.ID,
			Ordinal:    ordinal,
			PagePath:   value.page,
			Anchor:     value.anchor,
			Title:      page.Title,
			WorkflowID: "document-workflow-" + domain.ContentID(context),
			State:      domain.ItemPending,
			CreatedAt:  batch.CreatedAt,
			UpdatedAt:  batch.UpdatedAt,
		}
		if err := item.Validate(); err != nil {
			return nil, domain.BatchResolution{}, err
		}
		// The index is append-only: an already-published anchor can never be
		// re-practiced, so its item skips instead of scheduling dead work.
		if published[value.page+"\x00"+value.anchor] {
			item.State = domain.ItemSkipped
			item.Detail = "already-published"
		}
		items = append(items, item)
	}
	resolution.Resolved = len(items)
	if len(items) == 0 {
		return nil, domain.BatchResolution{}, errors.New("batch scope resolved to no practice-able pages")
	}
	return items, resolution, nil
}

func firstLevel2(page CorpusPage) (CorpusAnchor, bool) {
	for _, anchor := range page.Anchors {
		if anchor.Level == 2 {
			return anchor, true
		}
	}
	return CorpusAnchor{}, false
}

// GetBatch returns one batch with its item counts.
func (s *BatchService) GetBatch(ctx context.Context, batchID string) (domain.DocumentBatch, domain.BatchItemCounts, error) {
	return s.service.store.GetBatch(ctx, batchID)
}

// ListBatches returns the newest batches with their item counts.
func (s *BatchService) ListBatches(ctx context.Context, limit int) ([]domain.DocumentBatch, []domain.BatchItemCounts, error) {
	return s.service.store.ListBatches(ctx, limit)
}

// ListBatchItems returns one page of a batch's items.
func (s *BatchService) ListBatchItems(ctx context.Context, filter domain.BatchItemFilter) ([]domain.BatchItem, *domain.BatchItemCursor, error) {
	return s.service.store.ListBatchItems(ctx, filter)
}
