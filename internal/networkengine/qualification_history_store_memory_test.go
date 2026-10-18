package networkengine

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func strPtr(s string) *string { return &s }
func u16Ptr(v uint16) *uint16 { return &v }

func mustParseUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	require.NoError(t, err)
	return id
}

func TestMemoryQualificationHistoryStore(t *testing.T) {
	runQualificationHistoryStoreSuite(t, func(t *testing.T) QualificationHistoryStore {
		return NewMemoryQualificationHistoryStore()
	})
}
