// Package publication contains the small shared contract used by the Server
// finalizers that make generated or catalog content visible.
package publication

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

type Category string

const (
	CategoryUnknown       Category = ""
	CategoryDeterministic Category = "deterministic"
	CategoryTransient     Category = "transient"
)

const (
	maxDiagnosticMessage = 1024
	retryDelay           = 5 * time.Second
)

func (c Category) Valid() bool {
	return c == CategoryDeterministic || c == CategoryTransient
}

// Failure is the typed boundary between a finalizer and durable retry policy.
// Callers must classify the error explicitly; the persistence layer never
// infers a category from log text.
type Failure struct {
	category Category
	err      error
}

func (e *Failure) Error() string {
	if e == nil || e.err == nil {
		return "publication finalizer failed"
	}
	return e.err.Error()
}

func (e *Failure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *Failure) Category() Category {
	if e == nil {
		return CategoryUnknown
	}
	return e.category
}

func wrap(category Category, err error) error {
	if err == nil {
		return nil
	}
	if !category.Valid() {
		return fmt.Errorf("invalid publication error category %q", category)
	}
	return &Failure{category: category, err: err}
}

func Deterministic(err error) error { return wrap(CategoryDeterministic, err) }

func Transient(err error) error { return wrap(CategoryTransient, err) }

func CategoryOf(err error) Category {
	if err == nil {
		return CategoryUnknown
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		return CategoryUnknown
	}
	return failure.Category()
}

func IsDeterministic(err error) bool { return CategoryOf(err) == CategoryDeterministic }

func IsTransient(err error) bool { return CategoryOf(err) == CategoryTransient }

// Diagnostic is the durable, user-visible finalizer failure record. A
// deterministic failure has no retry time; a transient failure always does.
type Diagnostic struct {
	Category        Category
	LastError       string
	LastAttemptedAt time.Time
	NextRetryAt     *time.Time
}

func NewDiagnostic(err error, attemptedAt time.Time) (Diagnostic, error) {
	category := CategoryOf(err)
	if !category.Valid() {
		return Diagnostic{}, errors.New("publication finalizer error is not typed")
	}
	if attemptedAt.IsZero() {
		return Diagnostic{}, errors.New("publication finalizer attempt time is required")
	}
	diagnostic := Diagnostic{
		Category:        category,
		LastError:       SanitizeError(err),
		LastAttemptedAt: attemptedAt.UTC(),
	}
	if category == CategoryTransient {
		next := RetryAt(attemptedAt)
		diagnostic.NextRetryAt = &next
	}
	if err := diagnostic.Validate(); err != nil {
		return Diagnostic{}, err
	}
	return diagnostic, nil
}

func (d Diagnostic) Validate() error {
	if !d.Category.Valid() || strings.TrimSpace(d.LastError) == "" || d.LastAttemptedAt.IsZero() {
		return errors.New("publication diagnostic requires category, error, and attempt time")
	}
	if len([]rune(d.LastError)) > maxDiagnosticMessage {
		return errors.New("publication diagnostic error is too long")
	}
	switch d.Category {
	case CategoryDeterministic:
		if d.NextRetryAt != nil {
			return errors.New("deterministic publication diagnostic must not retry")
		}
	case CategoryTransient:
		if d.NextRetryAt == nil || d.NextRetryAt.IsZero() {
			return errors.New("transient publication diagnostic requires retry time")
		}
	default:
		return errors.New("publication diagnostic category is invalid")
	}
	return nil
}

func RetryAt(attemptedAt time.Time) time.Time {
	return attemptedAt.UTC().Add(retryDelay)
}

// SanitizeError bounds durable diagnostics and removes control characters so
// an arbitrary provider error cannot corrupt logs or the authoring response.
func SanitizeError(err error) string {
	message := "publication finalizer failed"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		message = err.Error()
	}
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
	message = strings.Join(strings.Fields(message), " ")
	if message == "" {
		message = "publication finalizer failed"
	}
	runes := []rune(message)
	if len(runes) > maxDiagnosticMessage {
		message = string(runes[:maxDiagnosticMessage-3]) + "..."
	}
	return message
}
