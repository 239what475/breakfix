package verification

import (
	"testing"
	"time"
)

func TestGrantIsBoundToTaskOperationAndExpiry(t *testing.T) {
	key := []byte("grant-key")
	now := time.Unix(1_700_000_000, 0)
	token, err := IssueGrant(key, Grant{
		Operation:    GrantSubmissionDownload,
		TaskID:       "verify-sub-123",
		SubmissionID: "sub-123",
		ExpiresAt:    now.Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyGrant(key, token, GrantSubmissionDownload, "verify-sub-123", now); err != nil {
		t.Fatalf("verify grant: %v", err)
	}
	if _, err := VerifyGrant(key, token, GrantOCIUpload, "verify-sub-123", now); err == nil {
		t.Fatal("operation mismatch was accepted")
	}
	if _, err := VerifyGrant(key, token, GrantSubmissionDownload, "other-task", now); err == nil {
		t.Fatal("task mismatch was accepted")
	}
	if _, err := VerifyGrant(key, token, GrantSubmissionDownload, "verify-sub-123", now.Add(2*time.Hour)); err == nil {
		t.Fatal("expired grant was accepted")
	}
}
