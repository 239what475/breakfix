package httpapi

import "testing"

func TestRuntimeClaimArbiterAlternatesFirstChoice(t *testing.T) {
	var arbiter runtimeClaimArbiter
	want := [][2]runtimeClaimLane{
		{runtimeClaimGeneration, runtimeClaimCatalog},
		{runtimeClaimCatalog, runtimeClaimGeneration},
		{runtimeClaimGeneration, runtimeClaimCatalog},
	}
	for index, expected := range want {
		if got := arbiter.order(); got != expected {
			t.Fatalf("claim order %d = %v, want %v", index, got, expected)
		}
	}
}

func TestClaimFirstAvailableFallsBackFromAnEmptyLane(t *testing.T) {
	var arbiter runtimeClaimArbiter
	calls := make([]runtimeClaimLane, 0, 2)
	value, err := claimFirstAvailable(&arbiter, func(lane runtimeClaimLane) (*string, error) {
		calls = append(calls, lane)
		if lane == runtimeClaimGeneration {
			return nil, nil
		}
		result := "catalog"
		return &result, nil
	})
	if err != nil || value == nil || *value != "catalog" {
		t.Fatalf("fallback result = %v, %v", value, err)
	}
	want := []runtimeClaimLane{runtimeClaimGeneration, runtimeClaimCatalog}
	if len(calls) != len(want) {
		t.Fatalf("claim calls = %v, want %v", calls, want)
	}
	for index := range want {
		if calls[index] != want[index] {
			t.Fatalf("claim calls = %v, want %v", calls, want)
		}
	}
}
