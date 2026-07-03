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
// BR5: prior rows for periodID are dropped before insert so re-evaluation
// semantics match the Postgres DELETE-then-INSERT transaction.
//
// Validation mirrors Postgres so a test that passes here would also pass
// against the real store. Duplicate UserIDs would fail the (period_id,
// user_id) PRIMARY KEY on COPY. Mismatched (rank, ordinal) pairs would
// fail the table CHECK constraint.
func (s *MemoryQualificationHistoryStore) SaveResult(_ context.Context, periodID string, entries []QualificationHistoryEntry) error {
	if periodID == "" {
		return fmt.Errorf("save qualification history: period_id must be non-empty")
	}

	seen := make(map[uuid.UUID]struct{}, len(entries))
	for _, e := range entries {
		if (e.Rank == nil) != (e.Ordinal == nil) {
			return fmt.Errorf("save qualification history: rank and ordinal must both be set or both be nil (user %s)", e.UserID)
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

	now := time.Now()
	for _, e := range entries {
		s.rows[memQualKey{PeriodID: periodID, UserID: e.UserID}] = QualificationHistoryRow{
			PeriodID:    periodID,
			UserID:      e.UserID,
			Rank:        e.Rank,
			Ordinal:     e.Ordinal,
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
			out = append(out, v)
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
		out = append(out, v)
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
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PeriodID != out[j].PeriodID {
			return out[i].PeriodID < out[j].PeriodID
		}
		return out[i].UserID.String() < out[j].UserID.String()
	})
	return out, nil
}
