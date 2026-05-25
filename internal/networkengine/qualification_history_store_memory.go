package networkengine

import (
	"context"
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

// GetByUserAndPeriodRange is implemented in Task 6. Stub returns nil.
func (s *MemoryQualificationHistoryStore) GetByUserAndPeriodRange(_ context.Context, _ uuid.UUID, _, _ string) ([]QualificationHistoryRow, error) {
	return nil, nil
}
