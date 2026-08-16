// Package toolresult defines the result contract shared by Server tools and
// the local MCP connector. A tool call that reached the application boundary
// is represented as data, even when the operation failed; transport errors
// must not accidentally become a second, incompatible tool protocol.
package toolresult

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
)

type Status string

const (
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	Unknown   Status = "unknown"
)

// Envelope is the wire format returned to an Agent. Data is deliberately raw
// JSON so the same envelope can carry each tool's typed payload without
// coupling this package to Generator or Authoring models.
type Envelope struct {
	Status Status          `json:"status"`
	Data   json.RawMessage `json:"data,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// StatusError marks an operation whose outcome is known at the application
// boundary. It is used by provider adapters when an HTTP response proves that
// a request was rejected, or when a transport failure leaves execution
// ambiguous.
type StatusError struct {
	status Status
	err    error
}

func (e *StatusError) Error() string {
	if e == nil || e.err == nil {
		return "tool operation failed"
	}
	return e.err.Error()
}

func (e *StatusError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func Mark(err error, status Status) error {
	if err == nil {
		return nil
	}
	if status != Failed && status != Unknown {
		return err
	}
	return &StatusError{status: status, err: err}
}

func IsUnknown(err error) bool {
	var statusErr *StatusError
	return errors.As(err, &statusErr) && statusErr.status == Unknown
}

func StatusForError(err error) Status {
	if err == nil {
		return Succeeded
	}
	var statusErr *StatusError
	if errors.As(err, &statusErr) && statusErr.status != "" {
		return statusErr.status
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return Unknown
	}
	var statusCoder interface{ HTTPStatusCode() int }
	if errors.As(err, &statusCoder) {
		status := statusCoder.HTTPStatusCode()
		if status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500 {
			return Unknown
		}
		return Failed
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return Unknown
	}
	return Failed
}

const unknownMessage = "execution state is unknown; inspect the workspace before deciding whether to retry"

func Message(err error) string {
	if StatusForError(err) == Unknown {
		return unknownMessage
	}
	if err == nil {
		return ""
	}
	if public, ok := err.(interface{ ToolMessage() string }); ok && strings.TrimSpace(public.ToolMessage()) != "" {
		return strings.TrimSpace(public.ToolMessage())
	}
	return strings.TrimSpace(err.Error())
}

func Failure(err error) Envelope {
	return WithStatus(Failed, nil, Message(err))
}

func FromError(err error) Envelope {
	return WithStatus(StatusForError(err), nil, Message(err))
}

func WithStatus(status Status, data []byte, message string) Envelope {
	return WithRawData(status, data, message)
}

func WithData(status Status, value any, message string) (Envelope, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return Envelope{}, err
	}
	return WithRawData(status, data, message), nil
}

func WithRawData(status Status, data []byte, message string) Envelope {
	if status != Succeeded && status != Failed && status != Unknown {
		status = Failed
	}
	envelope := Envelope{Status: status}
	if len(data) > 0 && !isJSONNull(data) {
		envelope.Data = append(json.RawMessage(nil), data...)
	}
	if strings.TrimSpace(message) != "" {
		envelope.Error = strings.TrimSpace(message)
	}
	return envelope
}

func Success(value any) (Envelope, error) {
	return WithData(Succeeded, value, "")
}

func Marshal(envelope Envelope) (string, error) {
	payload, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func SuccessJSON(raw []byte) (string, error) {
	if !json.Valid(raw) {
		return "", errors.New("tool returned invalid JSON")
	}
	return Marshal(WithRawData(Succeeded, raw, ""))
}

// IsEnvelopeJSON lets a tool with a typed, non-success outcome pass its
// already-classified result through the common authoring tool wrapper.
func IsEnvelopeJSON(raw []byte) bool {
	var value struct {
		Status Status `json:"status"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	return value.Status == Succeeded || value.Status == Failed || value.Status == Unknown
}

func isJSONNull(value []byte) bool {
	return strings.TrimSpace(string(value)) == "null"
}
