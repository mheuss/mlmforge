package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

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
	// Two attempts, not more. Each retry is triggered by a winner vanishing
	// between the conflict and the lookup. One retry covers that race; a
	// second would mean it happened twice in a row, which points at a caller
	// voiding runs in a loop rather than at a race worth absorbing.
	for range 2 {
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

// copyChunkSize bounds one CopyFrom call. A million-row write arrives as
// twenty chunks inside one transaction rather than as a single statement.
const copyChunkSize = 50_000

const deleteStructureResultsSQL = `DELETE FROM commission_results
                                   WHERE run_id = $1 AND structure = $2`

const lockRunSQL = `SELECT status FROM commission_runs WHERE id = $1 FOR UPDATE`

// SaveResults replaces every row for (runID, structure) inside one
// transaction. The run is locked first so it cannot complete or be voided
// while rows are landing under it.
func (s *PostgresCommissionRunStore) SaveResults(ctx context.Context, runID uuid.UUID, structure string, results []CommissionResultInput) error {
	// Validate before opening a transaction, so a malformed batch never
	// starts one. The copy source repeats the checks as a last line of
	// defense on the write path itself.
	if err := validateResultInputs(structure, results); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("save commission results: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit

	// Plain string then convert, matching scanCommissionRun.
	var rawStatus string
	if err := tx.QueryRow(ctx, lockRunSQL, runID).Scan(&rawStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &RunNotFoundError{RunID: runID}
		}
		return fmt.Errorf("save commission results: lock run: %w", err)
	}
	if status := CommissionRunStatus(rawStatus); status != RunStatusRunning {
		return &RunNotRunningError{RunID: runID, Status: status}
	}

	if _, err := tx.Exec(ctx, deleteStructureResultsSQL, runID, structure); err != nil {
		return fmt.Errorf("save commission results: clear prior rows: %w", err)
	}

	columns := []string{"run_id", "structure", "earner_id", "dollar_amount", "detail"}
	for start := 0; start < len(results); start += copyChunkSize {
		end := min(start+copyChunkSize, len(results))
		src := newCommissionResultCopySource(runID, structure, results[start:end])
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"commission_results"}, columns, src); err != nil {
			return fmt.Errorf("save commission results: copy from: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("save commission results: commit: %w", err)
	}
	return nil
}

// commissionResultCopySource is a pgx.CopyFromSource over result inputs.
// It follows qualificationHistoryCopySource, the reference adapter for bulk
// writes in this package.
//
// DollarAmount is formatted with strconv.FormatFloat(v, 'f', -1, 64), the
// shortest decimal string that round-trips the float exactly. Passing the
// float64 directly would let pgx choose its own textual form, and NUMERIC
// exists here precisely so the stored value is not at the mercy of float
// formatting.
type commissionResultCopySource struct {
	runID     uuid.UUID
	structure string
	results   []CommissionResultInput
	idx       int
}

func newCommissionResultCopySource(runID uuid.UUID, structure string, results []CommissionResultInput) *commissionResultCopySource {
	return &commissionResultCopySource{runID: runID, structure: structure, results: results, idx: -1}
}

func (s *commissionResultCopySource) Next() bool {
	s.idx++
	return s.idx < len(s.results)
}

// Values re-checks what validateResultInputs already rejected. This is the
// bypass guard: the copy path is where bytes reach the money table, and a
// future caller reaching it another way must fail loudly rather than write a
// silent default. Every branch here aborts the CopyFrom, which rolls back the
// whole transaction.
func (s *commissionResultCopySource) Values() ([]any, error) {
	r := s.results[s.idx]
	if math.IsNaN(r.DollarAmount) || math.IsInf(r.DollarAmount, 0) {
		return nil, fmt.Errorf("earner %s: dollar_amount must be finite, got %v", r.EarnerID, r.DollarAmount)
	}
	// Not defaulted to {}. detail is NOT NULL with a jsonb_typeof = 'object'
	// CHECK, and substituting an empty object here would turn a caller bug
	// into a persisted row that looks deliberate.
	if len(r.Detail) == 0 {
		return nil, fmt.Errorf("earner %s: detail must be a JSON object, got nothing", r.EarnerID)
	}
	return []any{
		s.runID,
		s.structure,
		r.EarnerID,
		strconv.FormatFloat(r.DollarAmount, 'f', -1, 64),
		r.Detail,
	}, nil
}

func (s *commissionResultCopySource) Err() error { return nil }

const getResultsSQL = `SELECT id, run_id, structure, earner_id,
                              dollar_amount::text, detail
                       FROM commission_results
                       WHERE run_id = $1
                       ORDER BY id ASC`

// GetResults returns a run's rows ordered by id ascending, whatever the run's
// status. A voided run's results stay readable for audit.
func (s *PostgresCommissionRunStore) GetResults(ctx context.Context, runID uuid.UUID) ([]CommissionResult, error) {
	// Resolve the run first so an unknown id is *RunNotFoundError rather than
	// an empty slice. A run with no rows is a different answer.
	if _, err := s.GetRun(ctx, runID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, getResultsSQL, runID)
	if err != nil {
		return nil, fmt.Errorf("get commission results: %w", err)
	}
	defer rows.Close()
	return scanCommissionResults(rows)
}

// scanCommissionResults reads dollar_amount as text and parses it, rather
// than letting the driver map NUMERIC to a float. The text is the exact
// stored decimal, so ParseFloat reproduces the original float64.
func scanCommissionResults(rows pgx.Rows) ([]CommissionResult, error) {
	var out []CommissionResult
	for rows.Next() {
		var r CommissionResult
		var amount string
		if err := rows.Scan(&r.ID, &r.RunID, &r.Structure, &r.EarnerID, &amount, &r.Detail); err != nil {
			return nil, fmt.Errorf("scan commission_results row: %w", err)
		}
		v, err := strconv.ParseFloat(amount, 64)
		if err != nil {
			return nil, fmt.Errorf("scan commission_results row: dollar_amount %q: %w", amount, err)
		}
		r.DollarAmount = v
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetLiveResults is implemented in Task 7.
func (s *PostgresCommissionRunStore) GetLiveResults(_ context.Context, _ string) ([]CommissionResult, error) {
	return nil, errPostgresUnimplemented
}
