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
// Not safe for concurrent use; intended for single-threaded tests and initial population.
type MemoryPeriodInputProvider struct {
	byPeriod map[string]PeriodInputs
}

// NewMemoryPeriodInputProvider returns an empty in-memory provider.
func NewMemoryPeriodInputProvider() *MemoryPeriodInputProvider {
	return &MemoryPeriodInputProvider{byPeriod: make(map[string]PeriodInputs)}
}

// Set stores in under periodID, overwriting any prior value. Safe on a zero-value receiver.
func (p *MemoryPeriodInputProvider) Set(periodID string, in PeriodInputs) {
	if p.byPeriod == nil { // zero-value safe: &MemoryPeriodInputProvider{} works too
		p.byPeriod = make(map[string]PeriodInputs)
	}
	p.byPeriod[periodID] = in
}

// InputsFor returns the inputs set for periodID, or a zero PeriodInputs if none. Never errors.
func (p *MemoryPeriodInputProvider) InputsFor(_ context.Context, periodID string) (PeriodInputs, error) {
	return p.byPeriod[periodID], nil // nil-safe read: zero PeriodInputs for unknown periods
}
