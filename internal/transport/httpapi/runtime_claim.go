package httpapi

import "sync/atomic"

// runtimeClaimLane is the durable-work owner that gets the next first-choice
// claim. Each action and cleanup endpoint owns its own arbiter.
type runtimeClaimLane uint8

const (
	runtimeClaimGeneration runtimeClaimLane = iota
	runtimeClaimCatalog
)

type runtimeClaimArbiter struct {
	turn atomic.Uint64
}

func (a *runtimeClaimArbiter) order() [2]runtimeClaimLane {
	if a.turn.Add(1)%2 == 1 {
		return [2]runtimeClaimLane{runtimeClaimGeneration, runtimeClaimCatalog}
	}
	return [2]runtimeClaimLane{runtimeClaimCatalog, runtimeClaimGeneration}
}

// claimFirstAvailable alternates first choice while immediately falling back
// when that lane has no due work. An error is not treated as an empty lane.
func claimFirstAvailable[T any](arbiter *runtimeClaimArbiter, claim func(runtimeClaimLane) (*T, error)) (*T, error) {
	for _, lane := range arbiter.order() {
		value, err := claim(lane)
		if err != nil || value != nil {
			return value, err
		}
	}
	return nil, nil
}
