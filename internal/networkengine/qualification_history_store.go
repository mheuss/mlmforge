package networkengine

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// QualificationHistoryEntry is the write-shape for one EvaluatedRank.
// PeriodID is supplied as an argument to SaveResult, not duplicated here.
//
// Rank and Ordinal are both nil for Unranked. The store enforces the
// invariant (Rank IS NULL) == (Ordinal IS NULL).
type QualificationHistoryEntry struct {
	UserID  uuid.UUID
	Rank    *string
	Ordinal *uint16
}

// QualificationHistoryRow is the read-shape for one persisted EvaluatedRank.
// Rank and Ordinal are nil for Unranked.
type QualificationHistoryRow struct {
	PeriodID    string
	UserID      uuid.UUID
	Rank        *string
	Ordinal     *uint16
	EvaluatedAt time.Time
}

// QualificationHistoryStore persists per-period rank evaluation results.
//
// The Rust engine is stateless on history. The Go orchestrator writes
// results to this store after a successful evaluate_ranks call when the
// caller supplied WithPersistence on EvaluateRanks.
//
// period_id is opaque to the store and compared lexicographically. Callers
// must use zero-padded, sortable strings (e.g., "2026-05", "2026-W21").
// Mixed widths like "2026-1" vs "2026-10" sort incorrectly. The memory
// implementation has a unit test that documents this failure mode.
//
// Read semantics: a missing row means "not evaluated for this period." An
// explicit row with a NULL rank means "evaluated, did not qualify"
// (Unranked). Consumers (HEU-446) rely on this distinction.
type QualificationHistoryStore interface {
	// SaveResult replaces all rows for periodID with entries, atomically.
	// Prior rows for periodID not present in entries are removed (BR5).
	// Implementations must run DELETE-then-INSERT (or equivalent set
	// replacement) inside one transaction.
	SaveResult(ctx context.Context, periodID string,
		entries []QualificationHistoryEntry) error

	// GetByUserAndPeriodRange returns one distributor's evaluated rank for
	// every period in [fromPeriod, toPeriod] inclusive that has a row.
	// Sorted by period_id ASC. Missing periods are omitted (no synthetic
	// Unranked) per BR7.
	GetByUserAndPeriodRange(ctx context.Context,
		userID uuid.UUID, fromPeriod, toPeriod string,
	) ([]QualificationHistoryRow, error)

	// GetByUsersAndPeriodRange returns the requested distributors' evaluated
	// ranks for every period in [fromPeriod, toPeriod] inclusive that has a
	// row. Sorted by period_id ASC, then user_id ASC (a deterministic superset
	// of GetByUserAndPeriodRange's period-only order). Missing (user, period)
	// pairs are omitted. An empty userIDs slice or fromPeriod > toPeriod yields
	// no rows.
	GetByUsersAndPeriodRange(ctx context.Context,
		userIDs []uuid.UUID, fromPeriod, toPeriod string,
	) ([]QualificationHistoryRow, error)

	// GetByPeriod returns every distributor's evaluated rank for a single
	// period. Sorted by user_id ASC.
	GetByPeriod(ctx context.Context, periodID string,
	) ([]QualificationHistoryRow, error)
}
