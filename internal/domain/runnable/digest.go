package runnable

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// CanonicalJSON returns stable bytes for digesting. Public structs contain no
// interface values; encoding/json sorts map keys and preserves struct field
// order, making this representation stable for this format version.
func CanonicalJSON(value any) ([]byte, error) {
	bytes, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("canonical JSON: %w", err)
	}
	return bytes, nil
}

func digest(value any) (string, error) {
	canonical, err := CanonicalJSON(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (s RunnableSpec) CanonicalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return CanonicalJSON(s)
}

func (s RunnableSpec) Digest() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	return digest(s)
}

func (p RuntimeProfile) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	return digest(p)
}

func (r RunnableRevision) CanonicalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return CanonicalJSON(r)
}

func (r RunnableRevision) Digest() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	return digest(r)
}
