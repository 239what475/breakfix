package toolresult

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestStatusForErrorDistinguishesKnownAndUnknownOutcomes(t *testing.T) {
	if got := StatusForError(errors.New("command exited with code 1")); got != Failed {
		t.Fatalf("known failure status = %q", got)
	}
	if got := StatusForError(context.DeadlineExceeded); got != Unknown {
		t.Fatalf("deadline status = %q", got)
	}
	unknown := FromError(context.DeadlineExceeded)
	if unknown.Status != Unknown || unknown.Error == "" {
		t.Fatalf("unknown envelope = %#v", unknown)
	}
}

func TestEnvelopeRoundTripPreservesTypedData(t *testing.T) {
	envelope, err := Success(struct {
		Value string `json:"value"`
	}{Value: "ok"})
	if err != nil {
		t.Fatalf("create success envelope: %v", err)
	}
	raw, err := Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var decoded Envelope
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if decoded.Status != Succeeded || string(decoded.Data) != `{"value":"ok"}` {
		t.Fatalf("decoded envelope = %#v", decoded)
	}
}
