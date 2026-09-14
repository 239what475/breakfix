package runnable

import (
	"errors"
	"fmt"
	"time"
)

// RevisionReference is the stable Server-owned reference used by consumers
// such as RuntimeEnvironment. Digest remains the immutable authority.
type RevisionReference struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

func (r RevisionReference) Validate() error {
	if err := validateID(r.ID, "runnable_revision_reference.id"); err != nil {
		return err
	}
	if !ValidDigest(r.Digest) {
		return invalid("runnable_revision_reference.digest", "must be a sha256 digest")
	}
	return nil
}

// StoredRevision is the persistence boundary for one complete immutable
// RunnableRevision. ArtifactReference remains embedded in Revision.
type StoredRevision struct {
	Reference RevisionReference `json:"reference"`
	Revision  RunnableRevision  `json:"revision"`
	CreatedAt time.Time         `json:"created_at"`
}

func (r StoredRevision) Validate() error {
	if err := r.Reference.Validate(); err != nil {
		return err
	}
	if err := r.Revision.Validate(); err != nil {
		return err
	}
	digest, err := r.Revision.Digest()
	if err != nil {
		return err
	}
	if r.Reference.Digest != digest || r.CreatedAt.IsZero() {
		return errors.New("stored runnable revision has an invalid digest or creation time")
	}
	return nil
}

type VerificationReportReference struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

func (r VerificationReportReference) Validate() error {
	if err := validateID(r.ID, "verification_report_reference.id"); err != nil {
		return err
	}
	if !ValidDigest(r.Digest) {
		return invalid("verification_report_reference.digest", "must be a sha256 digest")
	}
	return nil
}

// StoredVerificationReport keeps only a complete report and its immutable
// RunnableRevision binding. Cleanup progress is intentionally not part of it.
type StoredVerificationReport struct {
	Reference        VerificationReportReference `json:"reference"`
	Report           VerificationReport          `json:"report"`
	RunnableRevision RunnableRevision            `json:"runnable_revision"`
	CreatedAt        time.Time                   `json:"created_at"`
}

func (r StoredVerificationReport) Validate() error {
	if err := r.Reference.Validate(); err != nil {
		return err
	}
	if err := r.Report.Validate(r.RunnableRevision); err != nil {
		return err
	}
	digest, err := r.Report.Digest(r.RunnableRevision)
	if err != nil {
		return err
	}
	if r.Reference.Digest != digest || r.CreatedAt.IsZero() {
		return errors.New("stored verification report has an invalid digest or creation time")
	}
	return nil
}

func (r VerificationReport) CanonicalJSON(revision RunnableRevision) ([]byte, error) {
	if err := r.Validate(revision); err != nil {
		return nil, err
	}
	return CanonicalJSON(r)
}

func (r VerificationReport) Digest(revision RunnableRevision) (string, error) {
	if err := r.Validate(revision); err != nil {
		return "", fmt.Errorf("verification report digest: %w", err)
	}
	return digest(r)
}
