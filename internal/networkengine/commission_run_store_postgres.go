package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Compile-time check.
var _ CommissionRunStore = (*PostgresCommissionRunStore)(nil)

// PostgresCommissionRunStore is a CommissionRunStore backed by Postgres.
type PostgresCommissionRunStore struct {
	pool *pgxpool.Pool
}

func NewPostgresCommissionRunStore(pool *pgxpool.Pool) *PostgresCommissionRunStore {
	return &PostgresCommissionRunStore{pool: pool}
}

// activeRunIndex is the partial unique index from migration 000005. pgx
// reports it as the constraint name on a uniqueness violation, which is how
// CreateRun tells "period already has a run" apart from any other error.
const activeRunIndex = "commission_runs_active_period_idx"

const runColumns = `id, period_id, plan_hash, status, carry_forward,
                    started_at, completed_at, voided_at, superseded_by`

const insertRunSQL = `INSERT INTO commission_runs (id, period_id, plan_hash, status)
                      VALUES ($1, $2, $3, 'running')`

const getRunSQL = `SELECT ` + runColumns + ` FROM commission_runs WHERE id = $1`

const getActiveRunSQL = `SELECT ` + runColumns + `
                         FROM commission_runs
                         WHERE period_id = $1 AND status <> 'voided'`

// CreateRun inserts a run, letting the partial unique index arbitrate.
//
// The insert runs on the pool, not inside an explicit transaction. That
// matters: a constraint violation inside a transaction aborts it, and the
// follow-up lookup for the winner would fail with 25P02 rather than answer.
// On the pool each statement is its own transaction, so the lookup runs
// clean.
//
// The conflict path retries once. Between the constraint violation and the
// lookup for the winner, that winner can be voided, which would leave the
// period free and the typed error pointing at nothing. Rather than return
// *LiveRunExistsError with a nil ID — which the interface promises never to
// do — the retry re-attempts the insert, since a vanished winner means the
// period is genuinely available again. A second conflict is reported.
func (s *PostgresCommissionRunStore) CreateRun(ctx context.Context, periodID, planHash string) (uuid.UUID, error) {
	if err := validateRunInput(periodID, planHash); err != nil {
		return uuid.Nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		id := uuid.New()
		_, err := s.pool.Exec(ctx, insertRunSQL, id, periodID, planHash)
		if err == nil {
			return id, nil
		}
		if !isActiveRunConflict(err) {
			return uuid.Nil, fmt.Errorf("create commission run: %w", err)
		}
		existing, lookupErr := s.GetActiveRun(ctx, periodID)
		if lookupErr != nil {
			return uuid.Nil, fmt.Errorf(
				"create commission run: period %q is taken, and reading the winner failed: %w",
				periodID, lookupErr)
		}
		if existing != nil {
			return uuid.Nil, &LiveRunExistsError{PeriodID: periodID, ExistingRunID: existing.ID}
		}
		// The winner disappeared. Loop and try to claim the period.
	}
	return uuid.Nil, fmt.Errorf(
		"create commission run: period %q conflicted twice with no active run to name", periodID)
}

// isActiveRunConflict reports whether err is the partial unique index firing.
// Matches the ConstraintName pattern used by PostgresEventStore.Append.
func isActiveRunConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.ConstraintName == activeRunIndex
}

func (s *PostgresCommissionRunStore) GetRun(ctx context.Context, runID uuid.UUID) (*CommissionRun, error) {
	run, err := scanCommissionRun(s.pool.QueryRow(ctx, getRunSQL, runID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &RunNotFoundError{RunID: runID}
	}
	if err != nil {
		return nil, fmt.Errorf("get commission run: %w", err)
	}
	return run, nil
}

// GetActiveRun returns (nil, nil) when the period has no non-voided run.
// The partial unique index guarantees at most one row matches.
func (s *PostgresCommissionRunStore) GetActiveRun(ctx context.Context, periodID string) (*CommissionRun, error) {
	run, err := scanCommissionRun(s.pool.QueryRow(ctx, getActiveRunSQL, periodID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get active commission run: %w", err)
	}
	return run, nil
}

// scanCommissionRun reads status into a plain string and converts, rather
// than scanning straight into CommissionRunStatus. No other store in this
// codebase scans a named string type, so the driver's handling of one is
// unproven here. The conversion costs nothing and removes the question.
//
// The nullable columns scan into pointer-to-pointer, which is the same shape
// qualification_history uses for its nullable rank and ordinal.
func scanCommissionRun(row pgx.Row) (*CommissionRun, error) {
	var r CommissionRun
	var status string
	err := row.Scan(&r.ID, &r.PeriodID, &r.PlanHash, &status, &r.CarryForward,
		&r.StartedAt, &r.CompletedAt, &r.VoidedAt, &r.SupersededBy)
	if err != nil {
		return nil, err
	}
	r.Status = CommissionRunStatus(status)
	return &r, nil
}

// errPostgresUnimplemented keeps the not-yet-written operations loud until
// Tasks 6 through 8 land. Zero values would be quietly wrong: several suite
// cases assert an EMPTY result set or a nil error, so stubs returning those
// would pass against a store that does nothing — the exact drift the shared
// suite exists to catch.
var errPostgresUnimplemented = errors.New("commission run store: not implemented until HEU-555 tasks 6-8")

// CompleteRun is implemented in Task 7.
func (s *PostgresCommissionRunStore) CompleteRun(_ context.Context, _ uuid.UUID, _ json.RawMessage) error {
	return errPostgresUnimplemented
}

// VoidRun is implemented in Task 8.
func (s *PostgresCommissionRunStore) VoidRun(_ context.Context, _ uuid.UUID) error {
	return errPostgresUnimplemented
}

// ReplaceRun is implemented in Task 8.
func (s *PostgresCommissionRunStore) ReplaceRun(_ context.Context, _ uuid.UUID, _ string) (uuid.UUID, error) {
	return uuid.Nil, errPostgresUnimplemented
}

// SaveResults is implemented in Task 6.
func (s *PostgresCommissionRunStore) SaveResults(_ context.Context, _ uuid.UUID, _ string, _ []CommissionResultInput) error {
	return errPostgresUnimplemented
}

// GetResults is implemented in Task 6.
func (s *PostgresCommissionRunStore) GetResults(_ context.Context, _ uuid.UUID) ([]CommissionResult, error) {
	return nil, errPostgresUnimplemented
}

// GetLiveResults is implemented in Task 7.
func (s *PostgresCommissionRunStore) GetLiveResults(_ context.Context, _ string) ([]CommissionResult, error) {
	return nil, errPostgresUnimplemented
}
