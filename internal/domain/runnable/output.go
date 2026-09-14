package runnable

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

const MaxExecutionOutputBytes = 512 * 1024

// OutputCapture preserves provider stdout and stderr as separate immutable
// streams. The report stores only its reference, so mutable provider buffers
// never become the report's source of truth.
type OutputCapture struct {
	Stdout []byte `json:"stdout"`
	Stderr []byte `json:"stderr"`
}

func (c OutputCapture) CanonicalJSON() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Stdout == nil {
		c.Stdout = []byte{}
	}
	if c.Stderr == nil {
		c.Stderr = []byte{}
	}
	return CanonicalJSON(c)
}

func (c OutputCapture) Validate() error {
	if len(c.Stdout) > MaxExecutionOutputBytes || len(c.Stderr) > MaxExecutionOutputBytes-len(c.Stdout) {
		return errors.New("runnable execution output exceeds the platform limit")
	}
	return nil
}

func (c OutputCapture) Reference() (ImmutableReference, []byte, error) {
	encoded, err := c.CanonicalJSON()
	if err != nil {
		return ImmutableReference{}, nil, err
	}
	sum := sha256.Sum256(encoded)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	return ImmutableReference{
		Reference: "runnable-output://sha256/" + hex.EncodeToString(sum[:]),
		Digest:    digest,
		SizeBytes: int64(len(encoded)),
	}, encoded, nil
}
