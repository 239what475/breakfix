package runnable

import (
	"bytes"
	"strings"
	"testing"
)

func TestOutputCaptureReferenceIsCanonicalAndSeparatesStreams(t *testing.T) {
	first := OutputCapture{Stdout: []byte("ready\n"), Stderr: []byte("warning\n")}
	second := OutputCapture{Stdout: append([]byte(nil), first.Stdout...), Stderr: append([]byte(nil), first.Stderr...)}
	firstRef, firstJSON, err := first.Reference()
	if err != nil {
		t.Fatal(err)
	}
	secondRef, secondJSON, err := second.Reference()
	if err != nil {
		t.Fatal(err)
	}
	if firstRef != secondRef || !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("equivalent captures differ: %#v / %#v", firstRef, secondRef)
	}
	if !strings.HasPrefix(firstRef.Reference, "runnable-output://sha256/") || firstRef.SizeBytes != int64(len(firstJSON)) {
		t.Fatalf("invalid immutable output reference: %#v", firstRef)
	}
	changed := OutputCapture{Stdout: first.Stderr, Stderr: first.Stdout}
	changedRef, _, err := changed.Reference()
	if err != nil {
		t.Fatal(err)
	}
	if changedRef.Digest == firstRef.Digest {
		t.Fatal("stdout and stderr positions did not affect the output digest")
	}
}

func TestOutputCaptureNormalizesEmptyStreamsAndRejectsOversizeOrUnknownJSON(t *testing.T) {
	nilRef, nilJSON, err := (OutputCapture{}).Reference()
	if err != nil {
		t.Fatal(err)
	}
	emptyRef, emptyJSON, err := (OutputCapture{Stdout: []byte{}, Stderr: []byte{}}).Reference()
	if err != nil {
		t.Fatal(err)
	}
	if nilRef != emptyRef || !bytes.Equal(nilJSON, emptyJSON) {
		t.Fatalf("empty streams are not canonical: %#v / %#v", nilRef, emptyRef)
	}
	if err := (OutputCapture{Stdout: bytes.Repeat([]byte{'x'}, MaxExecutionOutputBytes+1)}).Validate(); err == nil {
		t.Fatal("oversize output was accepted")
	}
	if _, err := ParseOutputCapture([]byte(`{"stdout":"","stderr":"","extra":true}`)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown output field error = %v", err)
	}
}
