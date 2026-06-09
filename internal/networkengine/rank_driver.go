package networkengine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/mlmforge/mlmforge/internal/config"
	"github.com/mlmforge/mlmforge/internal/period"
)

// RankDriver runs per-period rank evaluation: it sizes and builds the prior
// axis, fetches history, evaluates, and persists. The caller wires the
// EngineClient (LoadPlan + tree) before using the driver.
type RankDriver struct {
	client   *EngineClient
	store    QualificationHistoryStore
	plan     *config.CompensationPlan
	provider PeriodInputProvider
	seq      *period.Sequence
}

// NewRankDriver builds the period sequence from the plan. A missing start_date
// is a loud error (design BR6).
func NewRankDriver(
	client *EngineClient, store QualificationHistoryStore,
	plan *config.CompensationPlan, provider PeriodInputProvider,
) (*RankDriver, error) {
	if client == nil || store == nil || plan == nil || provider == nil {
		return nil, fmt.Errorf("rank driver: client, store, plan, and provider are all required")
	}
	if plan.Period.StartDate == nil || *plan.Period.StartDate == "" {
		return nil, fmt.Errorf("rank driver: plan period start_date is required")
	}
	seq, err := period.NewSequence(plan.Period.Length, *plan.Period.StartDate)
	if err != nil {
		return nil, fmt.Errorf("rank driver: %w", err)
	}
	return &RankDriver{client: client, store: store, plan: plan, provider: provider, seq: seq}, nil
}

// guardAfterStart rejects evaluation of a period before the plan begins (BR9).
func (d *RankDriver) guardAfterStart(asOf time.Time) error {
	if d.seq.IsBeforeStart(asOf) {
		return fmt.Errorf("rank driver: %s is before the plan start period", asOf.Format("2006-01-02"))
	}
	return nil
}

// EvaluatePeriod is implemented in Task 11.
func (d *RankDriver) EvaluatePeriod(ctx context.Context, asOf time.Time) (*EvaluationResultDTO, error) {
	if err := d.guardAfterStart(asOf); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("rank driver: EvaluatePeriod not yet implemented")
}

// distributorIDs parses the request's distributor map keys into sorted UUIDs.
// A non-UUID key is a loud error, named, before any engine call.
func distributorIDs(m map[string]DistributorPrimitivesDTO) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0, len(m))
	for k := range m {
		id, err := uuid.Parse(k)
		if err != nil {
			return nil, fmt.Errorf("rank driver: invalid distributor id %q: %w", k, err)
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids, nil
}
