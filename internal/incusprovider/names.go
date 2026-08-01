package incusprovider

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

type EnvironmentNames struct {
	Project string
	Network string
	ACL     string
	Profile string
}

type BuildNames struct {
	Instance string
	Alias    string
}

func NamesForEnvironment(prefix, environmentUID string) (EnvironmentNames, error) {
	if strings.TrimSpace(prefix) == "" || strings.TrimSpace(environmentUID) == "" {
		return EnvironmentNames{}, fmt.Errorf("%w: name prefix and environment UID are required", ErrInvalid)
	}
	suffix := opaqueSuffix(environmentUID, 20)
	return EnvironmentNames{
		Project: prefix + "-env-" + suffix,
		Network: prefix + "-n-" + opaqueSuffix(environmentUID, 12-len(prefix)),
		ACL:     prefix + "-acl-" + suffix,
		Profile: prefix + "-profile-" + suffix,
	}, nil
}

func NameForNode(prefix, environmentUID, logicalName string) (string, error) {
	if strings.TrimSpace(prefix) == "" || strings.TrimSpace(environmentUID) == "" || strings.TrimSpace(logicalName) == "" {
		return "", fmt.Errorf("%w: name prefix, environment UID, and logical node name are required", ErrInvalid)
	}
	return prefix + "-node-" + opaqueSuffix(environmentUID, 16) + "-" + opaqueSuffix(logicalName, 10), nil
}

func NamesForBuildAttempt(prefix, workflowID string, attempt int64) (BuildNames, error) {
	if strings.TrimSpace(prefix) == "" || strings.TrimSpace(workflowID) == "" || attempt < 1 {
		return BuildNames{}, fmt.Errorf("%w: name prefix, workflow ID, and positive attempt are required", ErrInvalid)
	}
	suffix := opaqueSuffix(fmt.Sprintf("%s\x00%d", workflowID, attempt), 24)
	return BuildNames{
		Instance: prefix + "-build-" + suffix,
		Alias:    prefix + "-build-image-" + suffix,
	}, nil
}

func AliasForCandidate(prefix, candidateRevisionID string) (string, error) {
	if strings.TrimSpace(prefix) == "" || strings.TrimSpace(candidateRevisionID) == "" {
		return "", fmt.Errorf("%w: name prefix and candidate revision ID are required", ErrInvalid)
	}
	return prefix + "-candidate-" + opaqueSuffix(candidateRevisionID, 24), nil
}

func AliasForChallenge(prefix, challengeID string) (string, error) {
	if strings.TrimSpace(prefix) == "" || strings.TrimSpace(challengeID) == "" {
		return "", fmt.Errorf("%w: name prefix and challenge ID are required", ErrInvalid)
	}
	return prefix + "-challenge-" + opaqueSuffix(challengeID, 24), nil
}

func opaqueSuffix(value string, length int) string {
	digest := sha256.Sum256([]byte(value))
	encoded := hex.EncodeToString(digest[:])
	return encoded[:length]
}
