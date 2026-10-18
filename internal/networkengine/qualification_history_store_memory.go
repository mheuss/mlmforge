package networkengine

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Compile-time check.
var _ QualificationHistoryStore = (*MemoryQualificationHistoryStore)(nil)

type memQualKey struct {
	PeriodID string
	UserID   uuid.UUID
}

// MemoryQualificationHistoryStore is an in-memory QualificationHistoryStore for tests.
// Safe for concurrent reads; SaveResult takes a write lock.
type MemoryQualificationHistoryStore struct {
	mu   sync.RWMutex
	rows map[memQualKey]QualificationHistoryRow
}

// NewMemoryQualificationHistoryStore returns an empty in-memory store.
func NewMemoryQualificationHistoryStore() *MemoryQualificationHistoryStore {
	return &MemoryQualificationHistoryStore{rows: map[memQualKey]QualificationHistoryRow{}}
}

// SaveResult replaces all rows for periodID with entries.
func (s *MemoryQualificationHistoryStore) SaveResult(ctx context.Context, periodID string, entries []QualificationHistoryEntry) error {
	if periodID == "" {
		return fmt.Errorf("save qualification history: period_id must be non-empty")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("save qualification history: %w", err)
	}

	seen := make(map[uuid.UUID]struct{}, len(entries))
	for _, e := range entries {
		if (e.Rank == nil) != (e.Ordinal == nil) {
			return fmt.Errorf("save qualification history: rank and ordinal must both be set or both be nil (user %s)", e.UserID)
		}
		if e.Ordinal != nil && *e.Ordinal == 0 {
			return fmt.Errorf("save qualification history: ordinal must be at least 1 (user %s)", e.UserID)
		}
		if _, dup := seen[e.UserID]; dup {
			return fmt.Errorf("save qualification history: duplicate user_id %s for period %s", e.UserID, periodID)
		}
		seen[e.UserID] = struct{}{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// BR5: complete replacement — drop all prior rows for this period.
	for k := range s.rows {
		if k.PeriodID == periodID {
			delete(s.rows, k)
		}
	}

	now := time.Now().Round(0).Truncate(time.Microsecond)
	for _, e := range entries {
		s.rows[memQualKey{PeriodID: periodID, UserID: e.UserID}] = QualificationHistoryRow{
			PeriodID:    periodID,
			UserID:      e.UserID,
			Rank:        clonePtr(e.Rank),
			Ordinal:     clonePtr(e.Ordinal),
			EvaluatedAt: now,
		}
	}
	return nil
}

// GetByPeriod returns every distributor's evaluated rank for periodID,
// sorted by user_id ASC. uuid.UUID.String() is a stable lexicographic
// ordering on the canonical form, matching the SQL ORDER BY user_id ASC
// semantics the Postgres store will produce.
func (s *MemoryQualificationHistoryStore) GetByPeriod(_ context.Context, periodID string) ([]QualificationHistoryRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []QualificationHistoryRow
	for k, v := range s.rows {
		if k.PeriodID == periodID {
			out = append(out, cloneHistoryRow(v))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UserID.String() < out[j].UserID.String()
	})
	return out, nil
}

// GetByUserAndPeriodRange returns userID's evaluated ranks across the inclusive
// [fromPeriod, toPeriod] range, sorted by period_id ASC. Inverted ranges
// (fromPeriod > toPeriod) return an empty result per the interface contract.
func (s *MemoryQualificationHistoryStore) GetByUserAndPeriodRange(_ context.Context, userID uuid.UUID, fromPeriod, toPeriod string) ([]QualificationHistoryRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if fromPeriod > toPeriod {
		return nil, nil
	}
	var out []QualificationHistoryRow
	for k, v := range s.rows {
		if k.UserID != userID {
			continue
		}
		if k.PeriodID < fromPeriod || k.PeriodID > toPeriod {
			continue
		}
		out = append(out, cloneHistoryRow(v))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].PeriodID < out[j].PeriodID
	})
	return out, nil
}

// GetByUsersAndPeriodRange returns the requested users' rows across the inclusive
// [fromPeriod, toPeriod] range, sorted by period_id ASC then user_id ASC. An empty
// userIDs slice or an inverted range (fromPeriod > toPeriod) yields no rows without
// taking the read lock. Missing (user, period) pairs are omitted, matching the
// single-user GetByUserAndPeriodRange contract widened to a set.
func (s *MemoryQualificationHistoryStore) GetByUsersAndPeriodRange(_ context.Context, userIDs []uuid.UUID, fromPeriod, toPeriod string) ([]QualificationHistoryRow, error) {
	if len(userIDs) == 0 || fromPeriod > toPeriod {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	want := make(map[uuid.UUID]struct{}, len(userIDs))
	for _, id := range userIDs {
		want[id] = struct{}{}
	}
	var out []QualificationHistoryRow
	for k, v := range s.rows {
		if _, ok := want[k.UserID]; !ok {
			continue
		}
		if k.PeriodID < fromPeriod || k.PeriodID > toPeriod {
			continue
		}
		out = append(out, cloneHistoryRow(v))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PeriodID != out[j].PeriodID {
			return out[i].PeriodID < out[j].PeriodID
		}
		return out[i].UserID.String() < out[j].UserID.String()
	})
	return out, nil
}

// clonePtr returns a pointer to a copy of *p, or nil for nil.
func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// cloneHistoryRow returns r with its own copies of Rank and Ordinal.
func cloneHistoryRow(r QualificationHistoryRow) QualificationHistoryRow {
	r.Rank, r.Ordinal = clonePtr(r.Rank), clonePtr(r.Ordinal)
	return r
}
