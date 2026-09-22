// Package audit models the human operation ledger: one append-only row per
// administrator control-plane verb. It answers "who pressed the button" and
// complements, never replaces, the machine-side Agent audits.
package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The action vocabulary is a closed set. A new verb must be registered here
// before any write path may emit it.
const (
	ActionUserTOTPReset          = "user.totp.reset"
	ActionEnvironmentRelease     = "environment.release"
	ActionDocumentationLinkWrite = "documentation.link.write"
)

// Target types name the durable object an action was aimed at.
const (
	TargetUser               = "user"
	TargetRuntimeEnvironment = "runtime_environment"
	TargetDocumentationLink  = "documentation_link"
)

type HumanAction struct {
	ID         string          `json:"id"`
	UserID     string          `json:"user_id"`
	Action     string          `json:"action"`
	TargetType string          `json:"target_type"`
	TargetID   string          `json:"target_id"`
	Detail     json.RawMessage `json:"detail"`
	CreatedAt  time.Time       `json:"created_at"`
}

func (a HumanAction) Validate() error {
	if strings.TrimSpace(a.ID) == "" || strings.TrimSpace(a.UserID) == "" || !validAction(a.Action) || strings.TrimSpace(a.TargetType) == "" || strings.TrimSpace(a.TargetID) == "" || !json.Valid(a.Detail) || len(a.Detail) == 0 || a.CreatedAt.IsZero() {
		return errors.New("human action audit is incomplete")
	}
	return nil
}

func validAction(action string) bool {
	switch action {
	case ActionUserTOTPReset, ActionEnvironmentRelease, ActionDocumentationLinkWrite:
		return true
	}
	return false
}

// NewID follows the repository's unixnano identifier style.
func NewID(now time.Time) string {
	return fmt.Sprintf("audit-%d", now.UnixNano())
}

// HumanActionCursor is the keyset continuation of one audit page. Rows are
// ordered by (created_at DESC, id) so the cursor carries both.
type HumanActionCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        string    `json:"i"`
}
