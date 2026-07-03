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
	history := make(map[string]map[string]*uint16)
	if len(axis) == 0 || len(distributorIDs) == 0 {
		return axis, history, nil // nothing to fetch; preserves the empty-axis contract
	}

	// Range covering the axis. period_id is lexicographically sortable.
	from, to := axis[0], axis[0]
	inAxis := make(map[string]struct{}, len(axis))
	for _, p := range axis {
		inAxis[p] = struct{}{}
		if p < from {
			from = p
		}
		if p > to {
			to = p
		}
	}

	rows, err := store.GetByUsersAndPeriodRange(ctx, distributorIDs, from, to)
	if err != nil {
		return nil, nil, fmt.Errorf("build history window: users=%d range=%q..%q: %w",
			len(distributorIDs), from, to, err)
	}
	for _, r := range rows {
		// inAxis keeps output identical to the old per-period loop for ANY axis.
		// BETWEEN over-fetches only for a non-contiguous axis, which PriorLabels
		// never produces; this guards arbitrary callers.
		if _, ok := inAxis[r.PeriodID]; !ok {
			continue
		}
		key := r.UserID.String()
		m := history[key]
		if m == nil {
			m = make(map[string]*uint16)
			history[key] = m
		}
		m[r.PeriodID] = r.Ordinal // *uint16; nil == Unranked
	}
	return axis, history, nil
}
