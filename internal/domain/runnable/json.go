package runnable

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// DecodeStrictJSON is the only JSON ingress for runnable contracts. It
// rejects unknown fields at every nested object and trailing JSON values so a
// producer cannot smuggle undeclared behavior through an otherwise valid
// contract.
func DecodeStrictJSON[T any](raw []byte) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("decode runnable JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return value, fmt.Errorf("decode runnable JSON: multiple JSON values")
		}
		return value, fmt.Errorf("decode runnable JSON: %w", err)
	}
	return value, nil
}

func ParseRunnableSpec(raw []byte) (RunnableSpec, error) {
	value, err := DecodeStrictJSON[RunnableSpec](raw)
	if err != nil {
		return RunnableSpec{}, err
	}
	if err := value.Validate(); err != nil {
		return RunnableSpec{}, err
	}
	return value, nil
}

func ParseArtifactReference(raw []byte) (ArtifactReference, error) {
	value, err := DecodeStrictJSON[ArtifactReference](raw)
	if err != nil {
		return ArtifactReference{}, err
	}
	if err := value.Validate(); err != nil {
		return ArtifactReference{}, err
	}
	return value, nil
}

func ParseRunnableRevision(raw []byte) (RunnableRevision, error) {
	value, err := DecodeStrictJSON[RunnableRevision](raw)
	if err != nil {
		return RunnableRevision{}, err
	}
	if err := value.Validate(); err != nil {
		return RunnableRevision{}, err
	}
	return value, nil
}
