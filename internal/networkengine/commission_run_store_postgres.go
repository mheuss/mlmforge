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

// started_at is set explicitly rather than left to the column DEFAULT. The
// default is now(), which is transaction_timestamp() — stamped at BEGIN,
// before ReplaceRun's SELECT ... FOR UPDATE can block on a held run lock.
// Measured 1.95s early behind a bulk write, which puts the replacement's
// start before its predecessor's voided_at and makes the lifecycle read
// backwards in a table that exists to be an audit record. Same reason
// completed_at and voided_at use clock_timestamp().
const insertRunSQL = `INSERT INTO commission_runs (id, period_id, plan_hash, status, started_at)
                      VALUES ($1, $2, $3, 'running', clock_timestamp())`

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

// clock_timestamp(), not now(). now() is transaction_timestamp(), captured
// before the statement blocks on SaveResults' row lock — measured 3 seconds
// early behind a held lock, and behind a real million-row write that gap is
// the whole bulk-write duration. Nothing depends on the value, but an audit
// timestamp that predates rows written during the wait is a bad record.
const completeRunSQL = `UPDATE commission_runs
                        SET status = 'complete', completed_at = clock_timestamp(), carry_forward = $2
                        WHERE id = $1 AND status = 'running'`

// CompleteRun flips the run's visibility. The WHERE clause carries the guard,
// so a concurrent complete or void makes this a zero-row update rather than a
// lost write.
func (s *PostgresCommissionRunStore) CompleteRun(ctx context.Context, runID uuid.UUID, carryForward json.RawMessage) error {
	if err := validateCarryForward(carryForward); err != nil {
		return err
	}
	// cloneRaw, not carryForward directly. pgx encodes only a nil slice as
	// SQL NULL; an empty non-nil one is sent as an empty payload and fails
	// the jsonb insert. cloneRaw collapses empty to nil, which is the
	// normalization the interface promises — nil and empty mean the same
	// thing and both read back as nil.
	tag, err := s.pool.Exec(ctx, completeRunSQL, runID, cloneRaw(carryForward))
	if err != nil {
		return fmt.Errorf("complete commission run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return s.explainRunGuardFailure(ctx, runID)
	}
	return nil
}

// explainRunGuardFailure turns a zero-row update into the right typed error.
// The extra read only happens on the failure path.
//
// Rows-affected alone cannot tell the two apart: an unknown run and a run in
// the wrong state both update zero rows, and the suite asserts each
// separately.
//
// The re-read races the state it is reporting on, and that is fine because
// status is monotone per id: running goes to complete or voided, complete
// goes to voided, and voided is terminal. Ids are never reused — CreateRun
// and ReplaceRun both mint a fresh one. So the read can never come back
// `running` after a genuine guard failure, which is the only self-
// contradictory answer available. The worst reachable divergence is
// reporting `voided` when the update actually lost to a `complete`, still a
// truthful RunNotRunningError. Task 8 owns VoidRun and ReplaceRun; keep
// status monotone there or this reasoning stops holding.
func (s *PostgresCommissionRunStore) explainRunGuardFailure(ctx context.Context, runID uuid.UUID) error {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		var notFound *RunNotFoundError
		if errors.As(err, &notFound) {
			return err // the caller's answer, already typed
		}
		// Any other failure loses the operation's name without this.
		return fmt.Errorf("complete commission run: reading run state after a rejected update: %w", err)
	}
	return &RunNotRunningError{RunID: runID, Status: run.Status}
}

// voidRunSQL never touches superseded_by. Only linkSupersededBySQL does, and
// only from inside ReplaceRun's transaction, where the old run's row is
// locked and its period_id has been read. Keeping the link out of the general
// void path is what makes the same-period rule enforceable in one place.
//
// clock_timestamp() for the same reason as completed_at: now() is the
// transaction timestamp, taken before this statement can block on a
// concurrent SaveResults holding the run's row lock.
const voidRunSQL = `UPDATE commission_runs
                    SET status = 'voided', voided_at = clock_timestamp()
                    WHERE id = $1 AND status <> 'voided'`

// VoidRun marks a run voided, without a replacement. Voiding an
// already-voided run is a no-op returning nil, so a retry after an uncertain
// commit is safe. Only a missing run is an error.
func (s *PostgresCommissionRunStore) VoidRun(ctx context.Context, runID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, voidRunSQL, runID)
	if err != nil {
		return fmt.Errorf("void commission run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Either the run is missing or it was already voided. Only the
		// first is an error.
		if _, err := s.GetRun(ctx, runID); err != nil {
			var notFound *RunNotFoundError
			if errors.As(err, &notFound) {
				return err // the caller's answer, already typed
			}
			// Without this a transient read failure gets logged under
			// GetRun's name rather than VoidRun's.
			return fmt.Errorf("void commission run: reading run state after a no-op update: %w", err)
		}
		return nil // already voided, so the retry is a no-op
	}
	return nil
}

const lockRunForReplaceSQL = `SELECT period_id, status FROM commission_runs
                              WHERE id = $1 FOR UPDATE`

const linkSupersededBySQL = `UPDATE commission_runs SET superseded_by = $2 WHERE id = $1`

// ReplaceRun voids oldRunID and opens its replacement for the same period,
// in one transaction. This is ADR-013 scenario 2.
//
// The statement order is forced by two constraints pulling in opposite
// directions. The partial unique index on (period_id) WHERE status <>
// 'voided' means the replacement cannot be inserted while the old run is
// still active, so the void must come first. The superseded_by foreign key
// means the old run cannot point at the replacement before it exists, so the
// link must come last. Hence: lock, void, insert, link.
func (s *PostgresCommissionRunStore) ReplaceRun(ctx context.Context, oldRunID uuid.UUID, planHash string) (uuid.UUID, error) {
	// Before the transaction opens, so bad input never starts one. Only the
	// hash: period_id is inherited from the run being replaced rather than
	// supplied by the caller.
	if err := validatePlanHashOnly(planHash); err != nil {
		return uuid.Nil, err
	}

	// READ COMMITTED explicitly, not the session default. The loser of a
	// concurrent replace relies on EvalPlanQual: its SELECT ... FOR UPDATE
	// re-reads the row after the winner commits and sees 'voided', which is
	// what produces *RunNotRunningError. Under REPEATABLE READ that same
	// statement raises 40001 instead, and the typed error the interface
	// promises turns into a generic wrapped failure.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return uuid.Nil, fmt.Errorf("replace commission run: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit

	// 1. Lock the old run. The lock serializes concurrent replacements, and
	//    reading period_id from the locked row is what keeps the replacement
	//    in the same period without a trigger.
	var periodID string
	var rawStatus string // plain string then convert, matching scanCommissionRun
	if err := tx.QueryRow(ctx, lockRunForReplaceSQL, oldRunID).Scan(&periodID, &rawStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, &RunNotFoundError{RunID: oldRunID}
		}
		return uuid.Nil, fmt.Errorf("replace commission run: lock old run: %w", err)
	}
	// An allow-list, not "reject voided". Equivalent today under the status
	// CHECK, but a status added later would otherwise be silently accepted
	// and voided, contradicting this function's own contract.
	status := CommissionRunStatus(rawStatus)
	if status != RunStatusRunning && status != RunStatusComplete {
		// Allowed is set because ReplaceRun takes a complete run too. A bare
		// "not running" would send an operator looking for the wrong state.
		return uuid.Nil, &RunNotRunningError{
			RunID:   oldRunID,
			Status:  status,
			Allowed: []CommissionRunStatus{RunStatusRunning, RunStatusComplete},
		}
	}

	// 2. Void the old run. The index needs this to happen before the insert.
	//    voidRunSQL never writes superseded_by; step 4 does that.
	if _, err := tx.Exec(ctx, voidRunSQL, oldRunID); err != nil {
		return uuid.Nil, fmt.Errorf("replace commission run: void old run: %w", err)
	}

	// 3. Insert the replacement for the same period.
	newID := uuid.New()
	if _, err := tx.Exec(ctx, insertRunSQL, newID, periodID, planHash); err != nil {
		return uuid.Nil, fmt.Errorf("replace commission run: insert replacement: %w", err)
	}

	// 4. Link, now that the target exists. The foreign key needs this last.
	if _, err := tx.Exec(ctx, linkSupersededBySQL, oldRunID, newID); err != nil {
		return uuid.Nil, fmt.Errorf("replace commission run: link superseded_by: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("replace commission run: commit: %w", err)
	}
	return newID, nil
}

// copyChunkSize bounds the row count of one CopyFrom statement. It is not a
// memory control: pgx streams CopyFrom through a 64 KB pipe, so a single call
// over a million rows never buffers the batch either way.
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
	// READ COMMITTED explicitly, for the same reason as ReplaceRun: the
	// FOR UPDATE below depends on re-reading the row after a concurrent
	// writer commits, which REPEATABLE READ turns into a 40001 instead.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
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
// shortest decimal string that round-trips the float exactly.
//
// pgx's own float64-to-numeric encoder happens to call the same function
// today, so this is not a correction of the driver. It pins the behavior in
// this package rather than depending on a pgx internal that could change in a
// patch release, and it keeps NaN and infinities from being quietly encoded
// as numeric NaN/Infinity and pushed down to the table CHECK.
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

// Values re-checks a subset of what validateResultInputs rejects.
// This is the bypass guard: the copy path is where bytes reach the money
// table, and a future caller building this source directly must fail loudly
// rather than write a silent default.
//
// Note the returned error does not reach the caller as-is. pgx sends CopyFail
// and the server answers with its own error, so SaveResults surfaces a
// *pgconn.PgError carrying this text. Loud enough for a guard, but errors.As
// on a typed error here would not work, which is why these are plain.
func (s *commissionResultCopySource) Values() ([]any, error) {
	r := s.results[s.idx]
	// The one field with no database backstop. dollar_amount and detail both
	// have CHECK constraints behind them; earner_id has none, so a nil UUID
	// reaching here would be written into the money table unopposed.
	if r.EarnerID == uuid.Nil {
		return nil, fmt.Errorf("row %d: earner id must not be nil", s.idx)
	}
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
		// ParseFloat accepts "NaN", "Infinity" and "+Inf". The table CHECK
		// should mean no such row exists, but the write path guards this twice
		// and the read path guarded it zero times. One such value silently
		// destroys every SUM over the run, so refuse it rather than return it.
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf(
				"scan commission_results row %d: dollar_amount %q is not finite", r.ID, amount)
		}
		r.DollarAmount = v
		out = append(out, r)
	}
	return out, rows.Err()
}

// getLiveResultsSQL resolves the period's completed, non-voided run and reads
// its rows in one statement. Two statements would let a replacement land
// between them, and a paged reader that re-resolved between pages could
// return rows from two runs.
//
// status = 'complete' carries the non-voided condition implicitly: a run is
// in exactly one state, so a voided run is excluded by the same predicate.
const getLiveResultsSQL = `SELECT r.id, r.run_id, r.structure, r.earner_id,
                                  r.dollar_amount::text, r.detail
                           FROM commission_results r
                           JOIN commission_runs run ON run.id = r.run_id
                           WHERE run.period_id = $1 AND run.status = 'complete'
                           ORDER BY r.id ASC`

// GetLiveResults returns the period's current results, empty when the period
// has no run, its run is still running, or its run is voided.
func (s *PostgresCommissionRunStore) GetLiveResults(ctx context.Context, periodID string) ([]CommissionResult, error) {
	rows, err := s.pool.Query(ctx, getLiveResultsSQL, periodID)
	if err != nil {
		return nil, fmt.Errorf("get live commission results: %w", err)
	}
	defer rows.Close()
	return scanCommissionResults(rows)
}
