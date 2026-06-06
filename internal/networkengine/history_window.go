package networkengine

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// BuildHistoryWindow fetches achieved-rank history for the caller-supplied
// prior-period axis (most-recent-first) and pivots it into the sparse
// per-distributor shape evaluate_ranks expects, filtered to distributorIDs.
// The axis is returned unchanged so the caller controls ordering.
//
// A missing (period, user) row stays absent from the map; the engine reads
// that as "not evaluated." A present row with a nil ordinal pivots to a nil
// *uint16, which the engine reads as Unranked. This function builds data only.
// It does not validate the axis against any plan; the BR9 fail-loud guard for
// an empty axis lives engine-side, where the plan is known.
func BuildHistoryWindow(
	ctx context.Context, store QualificationHistoryStore,
	distributorIDs []uuid.UUID, axis []string,
) ([]string, map[string]map[string]*uint16, error) {
	want := make(map[uuid.UUID]struct{}, len(distributorIDs))
	for _, id := range distributorIDs {
		want[id] = struct{}{}
	}
	history := make(map[string]map[string]*uint16)
	for _, period := range axis {
		rows, err := store.GetByPeriod(ctx, period)
		if err != nil {
			return nil, nil, fmt.Errorf("build history window: period %q: %w", period, err)
		}
		for _, r := range rows {
			if _, ok := want[r.UserID]; !ok {
				continue // filter to requested distributors (I1)
			}
			key := r.UserID.String()
			m := history[key]
			if m == nil {
				m = make(map[string]*uint16)
				history[key] = m
			}
			m[period] = r.Ordinal // *uint16; nil == Unranked
		}
	}
	return axis, history, nil
}
