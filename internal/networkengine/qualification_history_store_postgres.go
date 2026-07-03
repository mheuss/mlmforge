package networkengine

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Compile-time check.
var _ QualificationHistoryStore = (*PostgresQualificationHistoryStore)(nil)

// PostgresQualificationHistoryStore is a QualificationHistoryStore backed by Postgres.
type PostgresQualificationHistoryStore struct {
	pool *pgxpool.Pool
}

func NewPostgresQualificationHistoryStore(pool *pgxpool.Pool) *PostgresQualificationHistoryStore {
	return &PostgresQualificationHistoryStore{pool: pool}
}

const qualHistoryColumns = `period_id, user_id, rank, ordinal, evaluated_at`

const getByPeriodSQL = `SELECT ` + qualHistoryColumns + `
                        FROM qualification_history
                        WHERE period_id = $1
                        ORDER BY user_id ASC`

const getByUserAndPeriodRangeSQL = `SELECT ` + qualHistoryColumns + `
                                    FROM qualification_history
                                    WHERE user_id = $1 AND period_id BETWEEN $2 AND $3
                                    ORDER BY period_id ASC`

// Served by qualification_history_user_period_idx (user_id, period_id). Keep
// user_id as the left operand of ANY so the index serves the read. HEU-506's
// planned tenant_id-in-PK change must re-EXPLAIN this to confirm it still does.
const getByUsersAndPeriodRangeSQL = `SELECT ` + qualHistoryColumns + `
                                     FROM qualification_history
                                     WHERE user_id = ANY($1)
                                       AND period_id BETWEEN $2 AND $3
                                     ORDER BY period_id ASC, user_id ASC`

func (s *PostgresQualificationHistoryStore) SaveResult(ctx context.Context, periodID string, entries []QualificationHistoryEntry) error {
	if periodID == "" {
		return fmt.Errorf("save qualification history: period_id must be non-empty")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("save qualification history: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit

	// BR5: complete replacement.
	if _, err := tx.Exec(ctx,
		"DELETE FROM qualification_history WHERE period_id = $1", periodID,
	); err != nil {
		return fmt.Errorf("save qualification history: delete prior rows: %w", err)
	}

	if len(entries) > 0 {
		src := newQualificationHistoryCopySource(periodID, entries)
		if _, err := tx.CopyFrom(ctx,
			pgx.Identifier{"qualification_history"},
			[]string{"period_id", "user_id", "rank", "ordinal"},
			src,
		); err != nil {
			return fmt.Errorf("save qualification history: copy from: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("save qualification history: commit: %w", err)
	}
	return nil
}

// qualificationHistoryCopySource is a pgx.CopyFromSource over a slice of
// entries. Each row emits (period_id, user_id, rank, ordinal). evaluated_at
// is left to the table DEFAULT now().
//
// Ordinal is widened from *uint16 to *int32 on the wire because Postgres
// has no unsigned integer types. The (rank IS NULL) = (ordinal IS NULL)
// invariant is enforced by the table CHECK constraint at COPY time;
// callers go through evaluationResultToHistoryEntries which always
// produces matched pairs.
type qualificationHistoryCopySource struct {
	periodID string
	entries  []QualificationHistoryEntry
	idx      int
	err      error
}

func newQualificationHistoryCopySource(periodID string, entries []QualificationHistoryEntry) *qualificationHistoryCopySource {
	return &qualificationHistoryCopySource{periodID: periodID, entries: entries, idx: -1}
}

func (s *qualificationHistoryCopySource) Next() bool {
	s.idx++
	return s.idx < len(s.entries)
}

func (s *qualificationHistoryCopySource) Values() ([]any, error) {
	e := s.entries[s.idx]
	var ordinal *int32
	if e.Ordinal != nil {
		v := int32(*e.Ordinal)
		ordinal = &v
	}
	return []any{s.periodID, e.UserID, e.Rank, ordinal}, nil
}

func (s *qualificationHistoryCopySource) Err() error { return s.err }

// GetByPeriod returns all rows for a period sorted by user_id ASC.
func (s *PostgresQualificationHistoryStore) GetByPeriod(ctx context.Context, periodID string) ([]QualificationHistoryRow, error) {
	rows, err := s.pool.Query(ctx, getByPeriodSQL, periodID)
	if err != nil {
		return nil, fmt.Errorf("get by period: %w", err)
	}
	defer rows.Close()
	return scanQualificationHistoryRows(rows)
}

// GetByUserAndPeriodRange returns rows for a user across a closed period range
// ordered by period_id ASC. The (user_id, period_id) secondary index serves
// this query as a leftmost-prefix range scan. period_id BETWEEN is inclusive
// on both ends and yields no rows when fromPeriod > toPeriod.
func (s *PostgresQualificationHistoryStore) GetByUserAndPeriodRange(ctx context.Context, userID uuid.UUID, fromPeriod, toPeriod string) ([]QualificationHistoryRow, error) {
	rows, err := s.pool.Query(ctx, getByUserAndPeriodRangeSQL, userID, fromPeriod, toPeriod)
	if err != nil {
		return nil, fmt.Errorf("get by user and period range: %w", err)
	}
	defer rows.Close()
	return scanQualificationHistoryRows(rows)
}

// GetByUsersAndPeriodRange returns the requested users' rows across the inclusive
// [fromPeriod, toPeriod] range, ordered by period_id ASC then user_id ASC. One
// index-served query replaces the per-period fan-out. An empty userIDs slice or an
// inverted range returns no rows without a round-trip.
func (s *PostgresQualificationHistoryStore) GetByUsersAndPeriodRange(ctx context.Context, userIDs []uuid.UUID, fromPeriod, toPeriod string) ([]QualificationHistoryRow, error) {
	if len(userIDs) == 0 || fromPeriod > toPeriod {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, getByUsersAndPeriodRangeSQL, userIDs, fromPeriod, toPeriod)
	if err != nil {
		return nil, fmt.Errorf("get by users and period range: %w", err)
	}
	defer rows.Close()
	return scanQualificationHistoryRows(rows)
}

func scanQualificationHistoryRows(rows pgx.Rows) ([]QualificationHistoryRow, error) {
	var out []QualificationHistoryRow
	for rows.Next() {
		var r QualificationHistoryRow
		var ord *int32
		if err := rows.Scan(&r.PeriodID, &r.UserID, &r.Rank, &ord, &r.EvaluatedAt); err != nil {
			return nil, fmt.Errorf("scan qualification_history row: %w", err)
		}
		if ord != nil {
			// Bounds-check the int32→uint16 narrowing. The Go contract
			// promises ordinal ∈ [1, 65535]; a value outside that range
			// means the row was written by something that didn't honor
			// the contract (manual SQL, future migration bug, etc.).
			// Wrapping silently would corrupt the rank ordering.
			if *ord < 1 || *ord > 65535 {
				return nil, fmt.Errorf("scan qualification_history row: ordinal %d out of range [1, 65535]", *ord)
			}
			v := uint16(*ord)
			r.Ordinal = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
