package networkengine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// These tests use raw SQL rather than the store, which does not exist yet.
// The point is to pin the DDL semantics themselves.
func TestCommissionRunsSchema(t *testing.T) {
	if pgContainer == nil {
		t.Skip("postgres container unavailable")
	}
	pool := pgContainer.NewPool(t)
	ctx := context.Background()

	const insert = `INSERT INTO commission_runs
		(id, period_id, plan_hash, status, completed_at)
		VALUES ($1, $2, 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', $3, $4)`

	t.Run("a completed run can be voided without erasing completed_at", func(t *testing.T) {
		id := uuid.New()
		done := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
		if _, err := pool.Exec(ctx, insert, id, "2026-01", "complete", done); err != nil {
			t.Fatalf("insert complete run: %v", err)
		}
		_, err := pool.Exec(ctx,
			`UPDATE commission_runs SET status = 'voided', voided_at = now() WHERE id = $1`, id)
		if err != nil {
			t.Fatalf("void a completed run should succeed, got: %v", err)
		}
		var readBack *time.Time
		if err := pool.QueryRow(ctx,
			`SELECT completed_at FROM commission_runs WHERE id = $1`, id,
		).Scan(&readBack); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if readBack == nil {
			t.Fatal("completed_at was erased by voiding; audit history lost")
		}
		if !readBack.Equal(done) {
			t.Fatalf("completed_at = %v, want %v", readBack, done)
		}
	})

	t.Run("two non-voided runs for one period are rejected", func(t *testing.T) {
		if _, err := pool.Exec(ctx, insert, uuid.New(), "2026-02", "running", nil); err != nil {
			t.Fatalf("first run: %v", err)
		}
		if _, err := pool.Exec(ctx, insert, uuid.New(), "2026-02", "running", nil); err == nil {
			t.Fatal("expected the partial unique index to reject a second active run")
		}
	})

	t.Run("a voided run does not block a new run for the same period", func(t *testing.T) {
		id := uuid.New()
		if _, err := pool.Exec(ctx, insert, id, "2026-03", "running", nil); err != nil {
			t.Fatalf("first run: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`UPDATE commission_runs SET status='voided', voided_at=now() WHERE id=$1`, id); err != nil {
			t.Fatalf("void: %v", err)
		}
		if _, err := pool.Exec(ctx, insert, uuid.New(), "2026-03", "running", nil); err != nil {
			t.Fatalf("replacement run should be allowed after voiding, got: %v", err)
		}
	})

	t.Run("complete without completed_at is rejected", func(t *testing.T) {
		if _, err := pool.Exec(ctx, insert, uuid.New(), "2026-04", "complete", nil); err == nil {
			t.Fatal("expected CHECK to require completed_at on a complete run")
		}
	})

	t.Run("superseded_by on a non-voided run is rejected", func(t *testing.T) {
		other := uuid.New()
		if _, err := pool.Exec(ctx, insert, other, "2026-05", "running", nil); err != nil {
			t.Fatalf("seed: %v", err)
		}
		_, err := pool.Exec(ctx,
			`INSERT INTO commission_runs (id, period_id, plan_hash, status, superseded_by)
			 VALUES ($1, '2026-06', 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'running', $2)`, uuid.New(), other)
		if err == nil {
			t.Fatal("expected CHECK to reject superseded_by on a non-voided run")
		}
	})

	t.Run("a malformed plan_hash is rejected", func(t *testing.T) {
		for _, bad := range []string{
			"deadbeef",        // no prefix
			"sha256:",         // prefix, no digest
			"sha256:anything", // prefix, not hex
			"sha256:abc",      // too short
		} {
			_, err := pool.Exec(ctx,
				`INSERT INTO commission_runs (id, period_id, plan_hash, status)
				 VALUES ($1, '2026-07', $2, 'running')`, uuid.New(), bad)
			if err == nil {
				t.Fatalf("expected CHECK to reject plan_hash %q", bad)
			}
		}
	})

	t.Run("a non-object carry_forward is rejected", func(t *testing.T) {
		_, err := pool.Exec(ctx,
			`INSERT INTO commission_runs (id, period_id, plan_hash, status, carry_forward)
			 VALUES ($1, '2026-08', $2, 'running', $3)`,
			uuid.New(), "sha256:"+strings.Repeat("a", 64), `[1,2]`)
		if err == nil {
			t.Fatal("expected CHECK to require a JSON object in carry_forward")
		}
	})

	t.Run("a null carry_forward is accepted", func(t *testing.T) {
		_, err := pool.Exec(ctx,
			`INSERT INTO commission_runs (id, period_id, plan_hash, status, carry_forward)
			 VALUES ($1, '2026-09', $2, 'running', NULL)`,
			uuid.New(), "sha256:"+strings.Repeat("a", 64))
		if err != nil {
			t.Fatalf("NULL carry_forward should be allowed, got: %v", err)
		}
	})
}

func TestCommissionResultsSchema(t *testing.T) {
	if pgContainer == nil {
		t.Skip("postgres container unavailable")
	}
	pool := pgContainer.NewPool(t)
	ctx := context.Background()

	runID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO commission_runs (id, period_id, plan_hash, status)
		 VALUES ($1, '2026-01', 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'running')`, runID); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	const insert = `INSERT INTO commission_results
		(run_id, structure, earner_id, dollar_amount, detail)
		VALUES ($1, $2, $3, $4, $5)`

	t.Run("a valid result row is accepted", func(t *testing.T) {
		if _, err := pool.Exec(ctx, insert, runID, "primary", uuid.New(), "12.34", `{"v":1}`); err != nil {
			t.Fatalf("valid row rejected: %v", err)
		}
	})

	t.Run("NaN dollar_amount is rejected", func(t *testing.T) {
		if _, err := pool.Exec(ctx, insert, runID, "primary", uuid.New(), "NaN", `{"v":1}`); err == nil {
			t.Fatal("expected CHECK to reject NaN")
		}
	})

	t.Run("a non-object detail is rejected", func(t *testing.T) {
		if _, err := pool.Exec(ctx, insert, runID, "primary", uuid.New(), "1.0", `[1,2]`); err == nil {
			t.Fatal("expected CHECK to require a JSON object in detail")
		}
	})

	t.Run("an empty structure is rejected", func(t *testing.T) {
		if _, err := pool.Exec(ctx, insert, runID, "", uuid.New(), "1.0", `{"v":1}`); err == nil {
			t.Fatal("expected CHECK to reject an empty structure")
		}
	})

	t.Run("a result with no run is rejected", func(t *testing.T) {
		if _, err := pool.Exec(ctx, insert, uuid.New(), "primary", uuid.New(), "1.0", `{"v":1}`); err == nil {
			t.Fatal("expected the foreign key to reject an unknown run_id")
		}
	})
}
