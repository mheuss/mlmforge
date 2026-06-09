package networkengine

import "context"

// PeriodInputs is the per-period rank-evaluation input the driver forwards into
// EvaluateRanks. It does not carry history; the driver builds that.
type PeriodInputs struct {
	Distributors  map[string]DistributorPrimitivesDTO
	VolumeSources []VolumeSourceDTO
}

// PeriodInputProvider supplies the distributor inputs for a given period. It is
// the seam where a real volume/order source plugs in later. The driver owns
// period sequencing, axis, and persistence; the provider owns inputs.
type PeriodInputProvider interface {
	InputsFor(ctx context.Context, periodID string) (PeriodInputs, error)
}

// MemoryPeriodInputProvider is an in-memory PeriodInputProvider for tests and
// initial population. A missing period yields zero PeriodInputs (no distributors).
type MemoryPeriodInputProvider struct {
	byPeriod map[string]PeriodInputs
}

func NewMemoryPeriodInputProvider() *MemoryPeriodInputProvider {
	return &MemoryPeriodInputProvider{byPeriod: make(map[string]PeriodInputs)}
}

func (p *MemoryPeriodInputProvider) Set(periodID string, in PeriodInputs) {
	if p.byPeriod == nil { // zero-value safe: &MemoryPeriodInputProvider{} works too
		p.byPeriod = make(map[string]PeriodInputs)
	}
	p.byPeriod[periodID] = in
}

func (p *MemoryPeriodInputProvider) InputsFor(_ context.Context, periodID string) (PeriodInputs, error) {
	return p.byPeriod[periodID], nil // nil-safe read: zero PeriodInputs for unknown periods
}
