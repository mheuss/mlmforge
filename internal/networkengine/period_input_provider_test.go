package networkengine

import (
	"context"
	"testing"
)

// Compile-time assertion: MemoryPeriodInputProvider implements PeriodInputProvider.
var _ PeriodInputProvider = (*MemoryPeriodInputProvider)(nil)

func TestMemoryPeriodInputProvider_ReturnsSetInputs(t *testing.T) {
	p := NewMemoryPeriodInputProvider()
	in := PeriodInputs{
		Distributors: map[string]DistributorPrimitivesDTO{
			"dist-1": {PersonalVolume: 100.0, Status: "active"},
		},
	}

	p.Set("2026-05", in)

	got, err := p.InputsFor(context.Background(), "2026-05")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := got.Distributors["dist-1"]; !ok {
		t.Errorf("expected distributor key %q to be present, got %v", "dist-1", got.Distributors)
	}
}

func TestMemoryPeriodInputProvider_UnknownPeriodIsEmpty(t *testing.T) {
	p := NewMemoryPeriodInputProvider()

	got, err := p.InputsFor(context.Background(), "2026-99")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Distributors != nil {
		t.Errorf("expected nil Distributors for unknown period, got %v", got.Distributors)
	}
}

func TestMemoryPeriodInputProvider_ZeroValueSafe(t *testing.T) {
	p := &MemoryPeriodInputProvider{}
	in := PeriodInputs{
		Distributors: map[string]DistributorPrimitivesDTO{
			"dist-1": {PersonalVolume: 50.0},
		},
	}

	p.Set("2026-05", in)

	got, err := p.InputsFor(context.Background(), "2026-05")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := got.Distributors["dist-1"]; !ok {
		t.Errorf("expected distributor key %q after zero-value Set, got %v", "dist-1", got.Distributors)
	}
}
