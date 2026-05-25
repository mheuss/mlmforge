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
func (s *MemoryQualificationHistoryStore) SaveResult(_ context.Context, periodID string, entries []QualificationHistoryEntry) error {
	if periodID == "" {
		return fmt.Errorf("save qualification history: period_id must be non-empty")
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
