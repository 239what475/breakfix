// Package runtimeenvironment contains the product-neutral RuntimeEnvironment
// state and lifecycle rules. Provider reconcilers consume its decisions but do
// not reinterpret content policy or update Server-owned spec fields.
package runtimeenvironment

import (
	"errors"
	"fmt"
	"strings"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

type Decision string

const (
	DecisionNone      Decision = "none"
	DecisionProvision Decision = "provision"
	DecisionReset     Decision = "reset"
	DecisionDrain     Decision = "drain"
	DecisionReap      Decision = "reap"
)

// Plan is the resolved runtime definition for one environment. Content-bound
// environments carry a complete immutable runnable revision; blank
// environments carry the controller-installed plan and never a revision.
type Plan struct {
	Revision runnable.RunnableRevision
	Blank    *runnable.BlankRuntimePlan
}

// Profile is the runtime profile either plan arm freezes.
func (p Plan) Profile() runnable.RuntimeProfile {
	if p.Blank != nil {
		return p.Blank.Profile
	}
	return p.Revision.Spec.RuntimeProfile
}

// Lifecycle is the lifecycle policy either plan arm freezes.
func (p Plan) Lifecycle() runnable.LifecyclePolicy {
	if p.Blank != nil {
		return p.Blank.Lifecycle
	}
	return p.Revision.Spec.LifecyclePolicy
}

// Runtime is the provider runtime either plan arm selects.
func (p Plan) Runtime() runnable.Runtime {
	return p.Profile().Runtime
}

// Digest is the revision fence for either plan arm.
func (p Plan) Digest() (string, error) {
	if p.Blank != nil {
		return p.Blank.Digest()
	}
	return p.Revision.Digest()
}

func ValidateSpec(environment runtimev2.RuntimeEnvironment, plan Plan) error {
	if plan.Blank == nil {
		ref := environment.Spec.RunnableRevisionRef
		if ref == nil || strings.TrimSpace(ref.ID) == "" || !runnable.ValidDigest(ref.Digest) {
			return errors.New("runtime environment has an invalid runnable revision reference")
		}
		if err := plan.Revision.Validate(); err != nil {
			return fmt.Errorf("runtime environment runnable revision: %w", err)
		}
		digest, err := plan.Revision.Digest()
		if err != nil {
			return err
		}
		if ref.Digest != digest {
			return errors.New("runtime environment reference does not match runnable revision")
		}
	} else {
		if environment.Spec.BlankRuntime == nil || environment.Spec.BlankRuntime.Provider != string(plan.Runtime()) {
			return errors.New("runtime environment blank runtime does not match the installed plan")
		}
		if err := plan.Blank.Validate(); err != nil {
			return fmt.Errorf("runtime environment blank runtime plan: %w", err)
		}
	}
	if environment.Spec.Purpose != runtimev2.PurposeLearning && environment.Spec.Purpose != runtimev2.PurposeVerification {
		return errors.New("runtime environment has an invalid purpose")
	}
	if environment.Spec.Lease.RenewedAt.IsZero() || environment.Spec.ResetNonce < 0 {
		return errors.New("runtime environment has invalid lease or reset controls")
	}
	if environment.Spec.Lease.ReleaseAt != nil && environment.Spec.Lease.ReleaseAt.IsZero() {
		return errors.New("runtime environment has an invalid release time")
	}
	profileDigest, err := plan.Profile().Digest()
	if err != nil {
		return err
	}
	if environment.Status.Runtime.ProfileDigest != "" && environment.Status.Runtime.ProfileDigest != profileDigest {
		return errors.New("runtime environment status is bound to another profile")
	}
	return nil
}

// ExpiresAt resolves lifecycle policy precisely once from immutable revision
// data and Server-controlled lease state. It returns the earlier of maximum
// lifetime and idle deadline, with an already requested release taking effect
// at its explicit timestamp.
func ExpiresAt(createdAt time.Time, lease runtimev2.LeaseSpec, policy runnable.LifecyclePolicy) (time.Time, error) {
	if createdAt.IsZero() || lease.RenewedAt.IsZero() {
		return time.Time{}, errors.New("runtime environment expiry requires creation and renewal timestamps")
	}
	if err := policy.Validate(); err != nil {
		return time.Time{}, err
	}
	deadline := createdAt.UTC().Add(time.Duration(policy.MaxLifetimeSeconds) * time.Second)
	idleDeadline := lease.RenewedAt.UTC().Add(time.Duration(policy.IdleTTLSeconds) * time.Second)
	expires := deadline
	if idleDeadline.Before(expires) {
		expires = idleDeadline
	}
	if lease.ReleaseAt != nil && lease.ReleaseAt.Time.Before(expires) {
		expires = lease.ReleaseAt.UTC()
	}
	return expires, nil
}

func Decide(environment runtimev2.RuntimeEnvironment, plan Plan, now time.Time, observedResetNonce int64) (Decision, error) {
	if err := ValidateSpec(environment, plan); err != nil {
		return DecisionNone, err
	}
	expiresAt, err := ExpiresAt(environment.CreationTimestamp.Time, environment.Spec.Lease, plan.Lifecycle())
	if err != nil {
		return DecisionNone, err
	}
	if environment.Status.Phase == runtimev2.PhaseReleased {
		return DecisionNone, nil
	}
	if environment.Status.Operation != "" && environment.Status.Operation != runtimev2.OperationNone && environment.Status.Operation != runtimev2.OperationResetting {
		return DecisionNone, fmt.Errorf("runtime environment has unsupported operation %q", environment.Status.Operation)
	}
	if !now.UTC().Before(expiresAt) || environment.Status.Phase == runtimev2.PhaseDraining {
		if environment.Status.Phase == runtimev2.PhaseDraining {
			return DecisionReap, nil
		}
		return DecisionDrain, nil
	}
	if environment.Status.Phase == runtimev2.PhaseReady && environment.Spec.ResetNonce > observedResetNonce {
		return DecisionReset, nil
	}
	if environment.Status.Operation == runtimev2.OperationResetting {
		if environment.Status.Phase != runtimev2.PhaseReady && environment.Status.Phase != runtimev2.PhaseProvisioning {
			return DecisionNone, fmt.Errorf("runtime environment reset is invalid in phase %q", environment.Status.Phase)
		}
		return DecisionProvision, nil
	}
	switch environment.Status.Phase {
	case "", runtimev2.PhasePending, runtimev2.PhaseProvisioning:
		return DecisionProvision, nil
	case runtimev2.PhaseReady, runtimev2.PhaseFailed:
		return DecisionNone, nil
	default:
		return DecisionNone, fmt.Errorf("runtime environment has unsupported phase %q", environment.Status.Phase)
	}
}

func CanTransition(from, to runtimev2.EnvironmentPhase) bool {
	if from == to {
		return true
	}
	switch from {
	case "", runtimev2.PhasePending:
		return to == runtimev2.PhaseProvisioning || to == runtimev2.PhaseDraining || to == runtimev2.PhaseFailed
	case runtimev2.PhaseProvisioning:
		return to == runtimev2.PhaseReady || to == runtimev2.PhaseDraining || to == runtimev2.PhaseFailed
	case runtimev2.PhaseReady:
		return to == runtimev2.PhaseDraining || to == runtimev2.PhaseFailed
	case runtimev2.PhaseFailed:
		return to == runtimev2.PhaseDraining
	case runtimev2.PhaseDraining:
		return to == runtimev2.PhaseReleased
	default:
		return false
	}
}
