package networkengine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// These tests use raw SQL rather than the store, which does not exist yet.
// The point is to pin the DDL semantics themselves.
func TestCommissionRunsSchema(t *testing.T) {
	if pgContainer == nil {
		t.Skip("postgres container unavailable")
	}
	pool := pgContainer.NewPool(t)
	ctx := context.Background()

	// Subtests share one pool and one truncate, so rows accumulate across
	// them. Every subtest must use a period_id no other subtest touches, or
	// it will collide with the partial unique index and fail for a reason
	// unrelated to what it tests. Periods in use below: 2026-01 .. 2026-19.
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

	// The seeded run shares this row's period on purpose. Point at another
	// period and the row would violate the composite foreign key as well as
	// the CHECK named here, so the test would stay green if the CHECK were
	// dropped. Same period leaves the CHECK as the only possible rejector.
	t.Run("superseded_by on a non-voided run is rejected", func(t *testing.T) {
		other := uuid.New()
		if _, err := pool.Exec(ctx, insert, other, "2026-05", "voided", nil); err == nil {
			t.Fatal("a voided seed needs voided_at; this insert should have failed")
		}
		// Seed the period's one active run, then try to supersede from a
		// second row in the same period that is still running.
		if _, err := pool.Exec(ctx,
			`INSERT INTO commission_runs (id, period_id, plan_hash, status, voided_at)
			 VALUES ($1, '2026-05', $2, 'voided', now())`,
			other, "sha256:"+strings.Repeat("a", 64)); err != nil {
			t.Fatalf("seed: %v", err)
		}
		_, err := pool.Exec(ctx,
			`INSERT INTO commission_runs (id, period_id, plan_hash, status, superseded_by)
			 VALUES ($1, '2026-05', $2, 'running', $3)`,
			uuid.New(), "sha256:"+strings.Repeat("a", 64), other)
		if err == nil {
			t.Fatal("expected CHECK to reject superseded_by on a non-voided run")
		}
		// 23514 is check_violation. Without this the composite foreign key or
		// the partial unique index could satisfy the assertion instead.
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("want a CHECK violation (23514), got %v", err)
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

	// A run cannot supersede itself. The composite foreign key below enforces
	// the same-period rule, but it is structurally blind to a self-reference:
	// a row's own (id, period_id) trivially exists, so the FK is satisfied.
	// That is why this needs its own CHECK.
	t.Run("a run superseding itself is rejected", func(t *testing.T) {
		id := uuid.New()
		_, err := pool.Exec(ctx,
			`INSERT INTO commission_runs (id, period_id, plan_hash, status, voided_at, superseded_by)
			 VALUES ($1, '2026-10', $2, 'voided', now(), $1)`,
			id, "sha256:"+strings.Repeat("a", 64))
		if err == nil {
			t.Fatal("expected CHECK to reject a run superseding itself; the chain would cycle")
		}
	})

	t.Run("superseded_by must reference an existing run", func(t *testing.T) {
		_, err := pool.Exec(ctx,
			`INSERT INTO commission_runs (id, period_id, plan_hash, status, voided_at, superseded_by)
			 VALUES ($1, '2026-11', $2, 'voided', now(), $3)`,
			uuid.New(), "sha256:"+strings.Repeat("a", 64), uuid.New())
		if err == nil {
			t.Fatal("expected the self-referencing foreign key to reject an unknown run")
		}
	})

	// Both directions of CHECK ((status = 'voided') = (voided_at IS NOT NULL)).
	t.Run("voided_at and voided status must agree", func(t *testing.T) {
		t.Run("voided without voided_at is rejected", func(t *testing.T) {
			if _, err := pool.Exec(ctx, insert, uuid.New(), "2026-12", "voided", nil); err == nil {
				t.Fatal("expected CHECK to require voided_at on a voided run")
			}
		})
		t.Run("voided_at on a non-voided run is rejected", func(t *testing.T) {
			_, err := pool.Exec(ctx,
				`INSERT INTO commission_runs (id, period_id, plan_hash, status, voided_at)
				 VALUES ($1, '2026-13', $2, 'running', now())`,
				uuid.New(), "sha256:"+strings.Repeat("a", 64))
			if err == nil {
				t.Fatal("expected CHECK to reject voided_at on a running run")
			}
		})
	})

	t.Run("completed_at on a running run is rejected", func(t *testing.T) {
		done := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
		if _, err := pool.Exec(ctx, insert, uuid.New(), "2026-16", "running", done); err == nil {
			t.Fatal("expected CHECK to reject a completion time on a run still running")
		}
	})

	// The same-period rule the original design called "not expressible as a
	// foreign key". The composite reference against UNIQUE (id, period_id)
	// does express it: id is the primary key, so requiring (superseded_by,
	// period_id) to exist forces the referenced run into this row's period.
	t.Run("superseding a run in another period is rejected", func(t *testing.T) {
		other := uuid.New()
		if _, err := pool.Exec(ctx, insert, other, "2026-17", "running", nil); err != nil {
			t.Fatalf("seed the other period's run: %v", err)
		}
		_, err := pool.Exec(ctx,
			`INSERT INTO commission_runs (id, period_id, plan_hash, status, voided_at, superseded_by)
			 VALUES ($1, '2026-18', $2, 'voided', now(), $3)`,
			uuid.New(), "sha256:"+strings.Repeat("a", 64), other)
		if err == nil {
			t.Fatal("expected the composite foreign key to reject a replacement from another period")
		}
		// Pin the mechanism, not just the rejection. Every other constraint on
		// this row shape is satisfied, but a future CHECK could start
		// rejecting it for an unrelated reason and leave this green while the
		// foreign key silently stopped working. 23503 is foreign_key_violation.
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
			t.Fatalf("want a foreign key violation (23503), got %v", err)
		}
	})

	t.Run("superseding a run in the same period is accepted", func(t *testing.T) {
		replacement := uuid.New()
		if _, err := pool.Exec(ctx, insert, replacement, "2026-19", "running", nil); err != nil {
			t.Fatalf("seed the replacement: %v", err)
		}
		// The superseded row is voided, so it does not contend for the
		// period's single active slot.
		_, err := pool.Exec(ctx,
			`INSERT INTO commission_runs (id, period_id, plan_hash, status, voided_at, superseded_by)
			 VALUES ($1, '2026-19', $2, 'voided', now(), $3)`,
			uuid.New(), "sha256:"+strings.Repeat("a", 64), replacement)
		if err != nil {
			t.Fatalf("a same-period replacement must be allowed, got: %v", err)
		}
	})

	t.Run("an unknown status is rejected", func(t *testing.T) {
		if _, err := pool.Exec(ctx, insert, uuid.New(), "2026-14", "pending", nil); err == nil {
			t.Fatal("expected CHECK to reject a status outside running/complete/voided")
		}
	})

	t.Run("an empty period_id is rejected", func(t *testing.T) {
		if _, err := pool.Exec(ctx, insert, uuid.New(), "", "running", nil); err == nil {
			t.Fatal("expected CHECK to reject an empty period_id")
		}
	})

	// The interesting half of the unique index: a *complete* run still holds
	// the period. This is what forces ReplaceRun to void before inserting.
	t.Run("a complete run blocks a new run for the same period", func(t *testing.T) {
		done := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
		if _, err := pool.Exec(ctx, insert, uuid.New(), "2026-15", "complete", done); err != nil {
			t.Fatalf("seed complete run: %v", err)
		}
		if _, err := pool.Exec(ctx, insert, uuid.New(), "2026-15", "running", nil); err == nil {
			t.Fatal("expected the partial unique index to reject a run alongside a complete one")
		}
	})
}

// GetActiveRun's WHERE clause duplicates this index predicate, and QueryRow
// silently takes the first row of whatever comes back. If HEU-506's
// multi-tenant scoping changes the index without changing that query,
// GetActiveRun starts picking an arbitrary row instead of failing. The index
// name is already pinned indirectly — rename it and isActiveRunConflict stops
// matching — but nothing pinned the predicate.
func TestCommissionRunsActivePeriodIndex(t *testing.T) {
	if pgContainer == nil {
		t.Skip("postgres container unavailable")
	}
	pool := pgContainer.NewPool(t)

	var def string
	err := pool.QueryRow(context.Background(),
		`SELECT indexdef FROM pg_indexes WHERE indexname = $1`,
		"commission_runs_active_period_idx",
	).Scan(&def)
	if err != nil {
		t.Fatalf("read index definition: %v", err)
	}
	if !strings.Contains(def, "UNIQUE") {
		t.Errorf("index must be UNIQUE to arbitrate CreateRun, got: %s", def)
	}
	if !strings.Contains(def, "(period_id)") {
		t.Errorf("index must key on period_id alone, got: %s", def)
	}
	if !strings.Contains(def, "status <> 'voided'") {
		t.Errorf("index predicate must match GetActiveRun's WHERE clause, got: %s", def)
	}
}

// GetResults and GetLiveResults both read WHERE run_id = $1 ORDER BY id ASC.
// Without an index leading on run_id and carrying id, Postgres walks the
// whole primary key index and filters, so the cost scales with total table
// size rather than with the run being read. Rows are never deleted here, so
// that gap only widens.
func TestCommissionResultsRunIndex(t *testing.T) {
	if pgContainer == nil {
		t.Skip("postgres container unavailable")
	}
	pool := pgContainer.NewPool(t)

	var def string
	err := pool.QueryRow(context.Background(),
		`SELECT indexdef FROM pg_indexes WHERE indexname = $1`,
		"commission_results_run_id_idx",
	).Scan(&def)
	if err != nil {
		t.Fatalf("read index definition: %v", err)
	}
	if !strings.Contains(def, "(run_id, id)") {
		t.Errorf("index must lead on run_id and carry id so the read needs no sort, got: %s", def)
	}
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

	// Negatives are legal. Clawbacks are the reason, and the finite-range
	// CHECK narrows this column's domain, so a future tightening to
	// dollar_amount >= 0 has to fail here rather than pass silently.
	t.Run("a valid result row is accepted", func(t *testing.T) {
		for _, good := range []string{"12.34", "-12.34", "0"} {
			if _, err := pool.Exec(ctx, insert, runID, "primary", uuid.New(), good, `{"v":1}`); err != nil {
				t.Fatalf("valid dollar_amount %q rejected: %v", good, err)
			}
		}
	})

	// Every non-finite numeric, not just NaN. Postgres 14+ accepts Infinity
	// in a NUMERIC column, and strconv.FormatFloat(math.Inf(1), 'f', -1, 64)
	// emits "+Inf" — the exact text this design routes float64 amounts
	// through. One such row makes SUM over the whole run return NaN forever.
	t.Run("a non-finite dollar_amount is rejected", func(t *testing.T) {
		for _, bad := range []string{"NaN", "Infinity", "-Infinity", "+Inf", "-Inf"} {
			_, err := pool.Exec(ctx, insert, runID, "primary", uuid.New(), bad, `{"v":1}`)
			if err == nil {
				t.Fatalf("expected CHECK to reject dollar_amount %q", bad)
			}
		}
	})

	t.Run("a non-object detail is rejected", func(t *testing.T) {
		if _, err := pool.Exec(ctx, insert, runID, "primary", uuid.New(), "1.0", `[1,2]`); err == nil {
			t.Fatal("expected CHECK to require a JSON object in detail")
		}
	})

	// Go rejects this twice, but neither guard is on the path a manual SQL
	// writer takes into the money table.
	t.Run("a nil earner_id is rejected", func(t *testing.T) {
		if _, err := pool.Exec(ctx, insert, runID, "primary", uuid.Nil, "1.0", `{"v":1}`); err == nil {
			t.Fatal("expected CHECK to reject the all-zero earner id")
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
