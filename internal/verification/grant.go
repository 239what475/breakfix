package verification

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// GrantOperation narrows a short-lived build-plane credential to one HTTP
// operation. A grant never authorizes the Server's general internal API.
type GrantOperation string

const (
	GrantSubmissionDownload GrantOperation = "submission-download"
	GrantBaseDownload       GrantOperation = "base-download"
	GrantOCIUpload          GrantOperation = "oci-upload"
	GrantOCIDownload        GrantOperation = "oci-download"
)

type Grant struct {
	Operation    GrantOperation `json:"operation"`
	TaskID       string         `json:"taskId"`
	SubmissionID string         `json:"submissionId,omitempty"`
	Runtime      string         `json:"runtime,omitempty"`
	ExpiresAt    int64          `json:"expiresAt"`
}

func IssueGrant(key []byte, grant Grant) (string, error) {
	if len(key) == 0 {
		return "", fmt.Errorf("verification grant key is required")
	}
	if strings.TrimSpace(string(grant.Operation)) == "" || strings.TrimSpace(grant.TaskID) == "" || grant.ExpiresAt <= 0 {
		return "", fmt.Errorf("invalid verification grant")
	}
	payload, err := json.Marshal(grant)
	if err != nil {
		return "", fmt.Errorf("encode verification grant: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func VerifyGrant(key []byte, token string, operation GrantOperation, taskID string, now time.Time) (Grant, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(key) == 0 || len(parts) != 2 {
		return Grant{}, fmt.Errorf("invalid verification grant")
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Grant{}, fmt.Errorf("invalid verification grant signature")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return Grant{}, fmt.Errorf("invalid verification grant signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Grant{}, fmt.Errorf("invalid verification grant payload")
	}
	var grant Grant
	if err := json.Unmarshal(payload, &grant); err != nil {
		return Grant{}, fmt.Errorf("invalid verification grant payload")
	}
	if grant.Operation != operation || grant.TaskID != taskID || grant.ExpiresAt <= now.Unix() {
		return Grant{}, fmt.Errorf("verification grant is not valid for this operation")
	}
	return grant, nil
}

func GrantFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum[:])
}
