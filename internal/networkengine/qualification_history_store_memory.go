package networkengine

import (
	"context"

	"github.com/google/uuid"
)

// Compile-time check.
var _ QualificationHistoryStore = (*MemoryQualificationHistoryStore)(nil)

// MemoryQualificationHistoryStore is an in-memory QualificationHistoryStore for tests.
type MemoryQualificationHistoryStore struct{}

// NewMemoryQualificationHistoryStore returns an empty in-memory store.
func NewMemoryQualificationHistoryStore() *MemoryQualificationHistoryStore {
	return &MemoryQualificationHistoryStore{}
}

// SaveResult is a stub. Task 4 adds behavior.
func (s *MemoryQualificationHistoryStore) SaveResult(_ context.Context, _ string, _ []QualificationHistoryEntry) error {
	return nil
}

// GetByUserAndPeriodRange is a stub. Task 4 adds behavior.
func (s *MemoryQualificationHistoryStore) GetByUserAndPeriodRange(_ context.Context, _ uuid.UUID, _, _ string) ([]QualificationHistoryRow, error) {
	return nil, nil
}

// GetByPeriod is a stub. Task 4 adds behavior.
func (s *MemoryQualificationHistoryStore) GetByPeriod(_ context.Context, _ string) ([]QualificationHistoryRow, error) {
	return nil, nil
}
