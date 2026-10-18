package networkengine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func newTestPostgresQualificationHistoryStore(t *testing.T) *PostgresQualificationHistoryStore {
	t.Helper()
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	pool := pgContainer.NewPool(t)
	return NewPostgresQualificationHistoryStore(pool)
}

func TestPostgresQualificationHistoryStore(t *testing.T) {
	runQualificationHistoryStoreSuite(t, func(t *testing.T) QualificationHistoryStore {
		return newTestPostgresQualificationHistoryStore(t)
	})
}

func TestPostgresQualificationHistoryStore_CheckConstraint_EmptyPeriodID(t *testing.T) {
	// The Go store rejects empty period_id before issuing SQL, so this CHECK
	// only matters as defense against non-app writers. Exercise it via a raw
	// INSERT to confirm the schema enforces it.
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	pool := pgContainer.NewPool(t)
	ctx := context.Background()

	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	_, err := pool.Exec(ctx,
		"INSERT INTO qualification_history (period_id, user_id, rank, ordinal) VALUES ($1, $2, $3, $4)",
		"", userA, "silver", 2,
	)
	require.Error(t, err, "CHECK (period_id <> '') should reject empty period_id")
}

func TestPostgresQualificationHistoryStore_CheckConstraint_OrdinalRange(t *testing.T) {
	// The Go store can never write an out-of-range ordinal (uint16 + mapper
	// rejects 0), so this CHECK is defense against non-app writers. Verify
	// via raw INSERT for both ends of the range.
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	pool := pgContainer.NewPool(t)
	ctx := context.Background()

	userA := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	userB := mustParseUUID(t, "00000000-0000-0000-0000-000000000002")

	// ordinal = 0 is rejected.
	_, err := pool.Exec(ctx,
		"INSERT INTO qualification_history (period_id, user_id, rank, ordinal) VALUES ($1, $2, $3, $4)",
		"2026-05", userA, "silver", 0,
	)
	require.Error(t, err, "CHECK should reject ordinal = 0")

	// ordinal = 70000 (> uint16 max) is rejected.
	_, err = pool.Exec(ctx,
		"INSERT INTO qualification_history (period_id, user_id, rank, ordinal) VALUES ($1, $2, $3, $4)",
		"2026-05", userB, "silver", 70000,
	)
	require.Error(t, err, "CHECK should reject ordinal > 65535")
}
