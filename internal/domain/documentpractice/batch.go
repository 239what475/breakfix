package documentpractice

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// BatchState is the deliberately minimal batch machine. Failure is item
// level: a batch carries counts, not failures, and closes as Completed once
// every item reached a terminal state.
type BatchState string

const (
	BatchPending   BatchState = "Pending"
	BatchRunning   BatchState = "Running"
	BatchPaused    BatchState = "Paused"
	BatchCompleted BatchState = "Completed"
	BatchCancelled BatchState = "Cancelled"
)

func (s BatchState) Terminal() bool { return s == BatchCompleted || s == BatchCancelled }

func (s BatchState) Valid() bool {
	switch s {
	case BatchPending, BatchRunning, BatchPaused, BatchCompleted, BatchCancelled:
		return true
	}
	return false
}

// BatchItemState follows one (page, anchor) pair through the pipeline. The
// active states are Scheduled and Running; the rest are terminal.
type BatchItemState string

const (
	ItemPending    BatchItemState = "Pending"
	ItemScheduled  BatchItemState = "Scheduled"
	ItemRunning    BatchItemState = "Running"
	ItemPublished  BatchItemState = "Published"
	ItemNoPractice BatchItemState = "NoPractice"
	ItemRejected   BatchItemState = "Rejected"
	ItemFailed     BatchItemState = "Failed"
	ItemSkipped    BatchItemState = "Skipped"
	ItemCancelled  BatchItemState = "Cancelled"
)

func (s BatchItemState) Terminal() bool {
	switch s {
	case ItemPublished, ItemNoPractice, ItemRejected, ItemFailed, ItemSkipped, ItemCancelled:
		return true
	}
	return false
}

func (s BatchItemState) Valid() bool {
	switch s {
	case ItemPending, ItemScheduled, ItemRunning, ItemPublished, ItemNoPractice, ItemRejected, ItemFailed, ItemSkipped, ItemCancelled:
		return true
	}
	return false
}

// BatchScopeKind names what a batch scope is declared over. A scope is data,
// not a curated list: it resolves deterministically from the corpus manifest.
type BatchScopeKind string

const (
	BatchScopeFull     BatchScopeKind = "full"
	BatchScopeSections BatchScopeKind = "sections"
	BatchScopePages    BatchScopeKind = "pages"
)

func (k BatchScopeKind) Valid() bool {
	switch k {
	case BatchScopeFull, BatchScopeSections, BatchScopePages:
		return true
	}
	return false
}

// BatchScope declares the pages a batch covers. Overrides pin an explicit
// (page, anchor) pair that bypasses the level-2 anchor rule.
type BatchScope struct {
	Kind      BatchScopeKind   `json:"kind"`
	Sections  []string         `json:"sections,omitempty"`
	Pages     []string         `json:"pages,omitempty"`
	Overrides []BatchAnchorRef `json:"overrides,omitempty"`
}

func (s BatchScope) Validate() error {
	if !s.Kind.Valid() {
		return fmt.Errorf("batch scope kind %q is unsupported", s.Kind)
	}
	seen := map[string]bool{}
	for _, section := range s.Sections {
		normalized := strings.TrimSuffix(strings.TrimSpace(section), "/")
		if normalized == "" {
			return errors.New("batch scope section must not be empty")
		}
		if seen["section:"+normalized] {
			return fmt.Errorf("batch scope section %q is duplicated", normalized)
		}
		seen["section:"+normalized] = true
	}
	for _, page := range s.Pages {
		normalized := strings.TrimSuffix(strings.TrimSpace(page), "/")
		if normalized == "" {
			return errors.New("batch scope page must not be empty")
		}
		if seen["page:"+normalized] {
			return fmt.Errorf("batch scope page %q is duplicated", normalized)
		}
		seen["page:"+normalized] = true
	}
	anchors := map[string]bool{}
	for _, override := range s.Overrides {
		if err := override.Validate(); err != nil {
			return err
		}
		key := override.PagePath + "\x00" + override.Anchor
		if anchors[key] {
			return fmt.Errorf("batch scope override for %s#%s is duplicated", override.PagePath, override.Anchor)
		}
		anchors[key] = true
	}
	return nil
}

// BatchAnchorRef pins one explicit page anchor.
type BatchAnchorRef struct {
	PagePath string `json:"page_path"`
	Anchor   string `json:"anchor"`
}

func (r BatchAnchorRef) Validate() error {
	if err := ValidateRelativePath(r.PagePath); err != nil {
		return fmt.Errorf("batch scope override page: %w", err)
	}
	if strings.TrimSpace(r.Anchor) == "" {
		return fmt.Errorf("batch scope override for %s requires an anchor", r.PagePath)
	}
	return nil
}

// BatchResolution records what the deterministic scope resolution decided, so
// a batch explains why it has the items it has: index pages and pages without
// a level-2 anchor are dropped and counted here.
type BatchResolution struct {
	Resolved int                 `json:"resolved_pages"`
	Excluded []BatchExcludedPage `json:"excluded,omitempty"`
}

type BatchExcludedPage struct {
	PagePath string `json:"page_path"`
	Reason   string `json:"reason"`
}

const (
	ExcludedIndex     = "index"
	ExcludedNoAnchor  = "no-anchor"
	ExcludedDuplicate = "duplicate"
)

func (r BatchResolution) Validate() error {
	for _, excluded := range r.Excluded {
		switch excluded.Reason {
		case ExcludedIndex, ExcludedNoAnchor, ExcludedDuplicate:
		default:
			return fmt.Errorf("batch resolution reason %q is unsupported", excluded.Reason)
		}
	}
	return nil
}

// DocumentBatch is the declarative rollout unit: scope, concurrency policy,
// and state. It exists so a multi-day rollout is driven by the Server, not by
// any client keeping a loop alive.
type DocumentBatch struct {
	ID          string          `json:"id"`
	State       BatchState      `json:"state"`
	Scope       BatchScope      `json:"scope"`
	Concurrency int             `json:"concurrency"`
	Resolution  BatchResolution `json:"resolution"`
	TotalItems  int             `json:"total_items"`
	CreatedBy   string          `json:"created_by"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

func NewBatchID(now time.Time) string {
	return fmt.Sprintf("document-batch-%d", now.UnixNano())
}

func (b DocumentBatch) Validate() error {
	if strings.TrimSpace(b.ID) == "" || !b.State.Valid() || b.CreatedBy == "" || b.CreatedAt.IsZero() || b.UpdatedAt.IsZero() {
		return errors.New("document batch is incomplete")
	}
	if err := b.Scope.Validate(); err != nil {
		return err
	}
	if b.Concurrency < 1 || b.Concurrency > 8 {
		return errors.New("document batch concurrency must be between 1 and 8")
	}
	if b.TotalItems < 0 {
		return errors.New("document batch item count is invalid")
	}
	return b.Resolution.Validate()
}

// NewBatchItemID derives the deterministic item identifier from the batch and
// the item ordinal.
func NewBatchItemID(batchID string, ordinal int) string {
	return fmt.Sprintf("%s-item-%04d", batchID, ordinal)
}

// BatchItem is one (page, anchor) pair of a batch. The workflow identifier is
// the deterministic document workflow identity for the pair; empty until the
// item's workflow has been derived (which happens at creation time).
type BatchItem struct {
	ID         string         `json:"id"`
	BatchID    string         `json:"batch_id"`
	Ordinal    int            `json:"ordinal"`
	PagePath   string         `json:"page_path"`
	Anchor     string         `json:"anchor"`
	Title      string         `json:"title"`
	WorkflowID string         `json:"workflow_id"`
	State      BatchItemState `json:"state"`
	Detail     string         `json:"detail,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

func (i BatchItem) Validate() error {
	if strings.TrimSpace(i.ID) == "" || strings.TrimSpace(i.BatchID) == "" || i.Ordinal < 0 || ValidateRelativePath(i.PagePath) != nil || !i.State.Valid() || i.CreatedAt.IsZero() || i.UpdatedAt.IsZero() {
		return errors.New("document batch item is incomplete")
	}
	return nil
}

// BatchItemCounts is the per-state projection shown on lists and details.
type BatchItemCounts map[BatchItemState]int

func (c BatchItemCounts) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[BatchItemState]int(c))
}

// BatchItemCursor is the keyset continuation of one item page, ordered by
// (ordinal).
type BatchItemCursor struct {
	Ordinal int `json:"o"`
}

// BatchItemFilter narrows an item page. An empty state means unfiltered.
type BatchItemFilter struct {
	BatchID string
	State   BatchItemState
	Cursor  *BatchItemCursor
	Limit   int
}

// TransitionBatch moves a batch through its minimal machine: Pending ->
// Running, Running <-> Paused, and any active state -> Completed | Cancelled.
func TransitionBatch(from, to BatchState) error {
	if from == to {
		return nil
	}
	switch from {
	case BatchPending:
		if to == BatchRunning || to == BatchCancelled {
			return nil
		}
	case BatchRunning:
		if to == BatchPaused || to == BatchCompleted || to == BatchCancelled {
			return nil
		}
	case BatchPaused:
		if to == BatchRunning || to == BatchCancelled {
			return nil
		}
	}
	return fmt.Errorf("invalid batch transition %s -> %s", from, to)
}
