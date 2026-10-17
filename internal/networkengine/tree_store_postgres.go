package networkengine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Compile-time check.
var _ TreeStore = (*PostgresTreeStore)(nil)

// PostgresTreeStore is a TreeStore backed by PostgreSQL.
type PostgresTreeStore struct {
	pool *pgxpool.Pool
	// afterVersionRead runs between ProjectedRows' two reads when set.
	afterVersionRead func()
}

// ON CONFLICT targets the primary key, which carries the event ID. A
// redelivered event is skipped and reports zero rows instead of raising, while
// the partial unique indexes still raise. Changing the arbiter changes which
// conflict is silent.
const insertNodeSQL = `INSERT INTO tree_nodes (id, tree_id, user_id, parent_id, sponsor_id, position, depth, enrolled_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (id) DO NOTHING`

const softDeleteNodeSQL = `UPDATE tree_nodes SET removed_at = now(), updated_at = now()
		 WHERE tree_id = $1 AND user_id = $2 AND removed_at IS NULL`

const softDeleteRootSQL = `UPDATE tree_nodes SET removed_at = $3, updated_at = $3
		 WHERE tree_id = $1 AND user_id = $2 AND removed_at IS NULL`

// tombstoneTime reads the clock once inside tx, for every tombstone tx writes.
func tombstoneTime(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var at time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
		return time.Time{}, fmt.Errorf("read the tombstone time: %w", err)
	}
	return at, nil
}

// Partial unique index names on tree_nodes. pgx reports the index name in
// ConstraintName, which is what tells them apart.
const (
	activeUserIndex = "idx_tree_nodes_tree_user"
	activeSlotIndex = "idx_tree_nodes_tree_parent_position_active"
	activeRootIndex = "idx_tree_nodes_tree_root_active"
)

// conflictError maps a pg error naming one of the indexes above to its sentinel,
// and returns nil for anything else so the raw error surfaces.
//
// Matching on ConstraintName rather than SQLSTATE: 23505 covers every unique
// violation on the table and cannot tell the indexes apart.
func conflictError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil
	}
	switch pgErr.ConstraintName {
	case activeUserIndex:
		return ErrActiveUserConflict
	case activeSlotIndex:
		return ErrSlotConflict
	case activeRootIndex:
		return ErrRootConflict
	default:
		return nil
	}
}

// treeNodeSelectColumns is the SELECT column list for tree_nodes queries.
// Order must match the scanTreeNode/scanTreeNodes Scan call.
const treeNodeSelectColumns = `id, tree_id, user_id, parent_id, sponsor_id, position, depth, enrolled_at, created_at, updated_at, removed_at, removed_by_event_id`

const getNodeSQL = `SELECT ` + treeNodeSelectColumns + ` FROM tree_nodes WHERE tree_id = $1 AND user_id = $2 AND removed_at IS NULL`
const getChildrenSQL = `SELECT ` + treeNodeSelectColumns + ` FROM tree_nodes WHERE tree_id = $1 AND parent_id = $2 AND removed_at IS NULL`
const getByTreeSQL = `SELECT ` + treeNodeSelectColumns + ` FROM tree_nodes WHERE tree_id = $1 AND removed_at IS NULL`
const getByTreeDepthOrderedSQL = getByTreeSQL + ` ORDER BY depth ASC, enrolled_at ASC`

// DESC NULLS FIRST is one key doing both jobs: the active row sorts ahead of
// every tombstone, and the newest tombstone sorts ahead of older ones. Two
// keys on the same column cannot do this, because the second can only break
// ties the first already resolved. The id key breaks ties between rows that
// share a removed_at.
const getNodeIncludingRemovedSQL = `SELECT ` + treeNodeSelectColumns +
	` FROM tree_nodes WHERE tree_id = $1 AND user_id = $2
	  ORDER BY removed_at DESC NULLS FIRST, id DESC LIMIT 1`

const getNodeByRemovalEventSQL = `SELECT ` + treeNodeSelectColumns +
	` FROM tree_nodes WHERE tree_id = $1 AND removed_by_event_id = $2
	  ORDER BY removed_at DESC, id DESC LIMIT 1`

func NewPostgresTreeStore(pool *pgxpool.Pool) *PostgresTreeStore {
	return &PostgresTreeStore{pool: pool}
}

func (s *PostgresTreeStore) InsertNode(ctx context.Context, node TreeNodeRow) error {
	return insertNode(ctx, s.pool, node)
}

// execer is the Exec method the store's writes need.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// insertNode inserts one row through db.
func insertNode(ctx context.Context, db execer, node TreeNodeRow) error {
	tag, err := db.Exec(ctx, insertNodeSQL,
		node.ID, node.TreeID, node.UserID, node.ParentID, node.SponsorID, node.Position, node.Depth, node.EnrolledAt,
	)
	if err != nil {
		if c := conflictError(err); c != nil {
			return fmt.Errorf("%w: tree=%s user=%s id=%s", c, node.TreeID, node.UserID, node.ID)
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: id=%s; this insert was for tree=%s user=%s",
			ErrNodeAlreadyProjected, node.ID, node.TreeID, node.UserID)
	}
	return nil
}

func (s *PostgresTreeStore) DeleteNode(ctx context.Context, treeID, userID string) error {
	_, err := s.pool.Exec(ctx, softDeleteNodeSQL, treeID, userID)
	return err
}

func (s *PostgresTreeStore) DeleteNodeAndResponsor(
	ctx context.Context,
	treeID, userID, removalEventID string,
	moved []Responsored,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := deleteNodeAndResponsor(ctx, tx, treeID, userID, removalEventID, moved); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// deleteNodeAndResponsor runs the soft delete and the re-sponsor writes in tx.
func deleteNodeAndResponsor(
	ctx context.Context,
	tx pgx.Tx,
	treeID, userID, removalEventID string,
	moved []Responsored,
) error {
	// The stamp rides the soft delete's own statement, so it lands with the
	// tombstone or not at all. removed_at IS NULL can only match the row that
	// was active, so it cannot reach an earlier placement's tombstone.
	tag, err := tx.Exec(ctx,
		`UPDATE tree_nodes SET removed_at = now(), updated_at = now(), removed_by_event_id = $3
		 WHERE tree_id = $1 AND user_id = $2 AND removed_at IS NULL`,
		treeID, userID, removalEventID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf(
			"soft delete for user %s in tree %s matched %d active rows; %d re-sponsor writes not applied",
			userID, treeID, tag.RowsAffected(), len(moved))
	}

	for _, m := range moved {
		// removed_at IS NULL: a user removed and re-placed has a historical
		// row too, and the partial unique index is what keeps them apart.
		tag, err := tx.Exec(ctx,
			`UPDATE tree_nodes SET sponsor_id = $1, updated_at = now()
			 WHERE tree_id = $2 AND user_id = $3 AND removed_at IS NULL`,
			m.NewSponsorID, treeID, m.UserID,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf(
				"re-sponsoring %s in tree %s updated %d active rows, expected 1",
				m.UserID, treeID, tag.RowsAffected())
		}
	}
	return nil
}

func (s *PostgresTreeStore) GetNode(ctx context.Context, treeID, userID string) (*TreeNodeRow, error) {
	row := s.pool.QueryRow(ctx, getNodeSQL, treeID, userID)
	return scanTreeNode(row)
}

func (s *PostgresTreeStore) GetNodeIncludingRemoved(ctx context.Context, treeID, userID string) (*TreeNodeRow, error) {
	row := s.pool.QueryRow(ctx, getNodeIncludingRemovedSQL, treeID, userID)
	return scanTreeNode(row)
}

func (s *PostgresTreeStore) GetNodeByRemovalEvent(ctx context.Context, treeID, removalEventID string) (*TreeNodeRow, error) {
	row := s.pool.QueryRow(ctx, getNodeByRemovalEventSQL, treeID, removalEventID)
	return scanTreeNode(row)
}

func (s *PostgresTreeStore) GetChildren(ctx context.Context, treeID, parentUserID string) ([]TreeNodeRow, error) {
	rows, err := s.pool.Query(ctx, getChildrenSQL, treeID, parentUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTreeNodes(rows)
}

func (s *PostgresTreeStore) GetByTree(ctx context.Context, treeID string) ([]TreeNodeRow, error) {
	rows, err := s.pool.Query(ctx, getByTreeSQL, treeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTreeNodes(rows)
}

func (s *PostgresTreeStore) GetByTreeDepthOrdered(ctx context.Context, treeID string) ([]TreeNodeRow, error) {
	rows, err := s.pool.Query(ctx, getByTreeDepthOrderedSQL, treeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTreeNodes(rows)
}

func (s *PostgresTreeStore) BulkInsert(ctx context.Context, nodes []TreeNodeRow) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin bulk insert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, node := range nodes {
		tag, err := tx.Exec(ctx, insertNodeSQL,
			node.ID, node.TreeID, node.UserID, node.ParentID, node.SponsorID, node.Position, node.Depth, node.EnrolledAt,
		)
		if err != nil {
			if c := conflictError(err); c != nil {
				return fmt.Errorf("bulk insert node %s: %w", node.UserID, c)
			}
			return fmt.Errorf("bulk insert node %s: %w", node.UserID, err)
		}
		// InsertNode reads a skipped row as convergence. A bulk load cannot:
		// the rows are a whole tree, and one already present means the batch
		// is built from the wrong picture. The deferred rollback discards it.
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("bulk insert node %s: %w", node.UserID, ErrNodeAlreadyProjected)
		}
	}

	return tx.Commit(ctx)
}

// scanTreeNode scans a single tree node row. Returns nil if no row found.
func scanTreeNode(row pgx.Row) (*TreeNodeRow, error) {
	var n TreeNodeRow
	err := row.Scan(
		&n.ID, &n.TreeID, &n.UserID, &n.ParentID, &n.SponsorID,
		&n.Position, &n.Depth, &n.EnrolledAt, &n.CreatedAt, &n.UpdatedAt, &n.RemovedAt,
		&n.RemovedByEventID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// scanTreeNodes scans multiple tree node rows.
func scanTreeNodes(rows pgx.Rows) ([]TreeNodeRow, error) {
	var nodes []TreeNodeRow
	for rows.Next() {
		var n TreeNodeRow
		err := rows.Scan(
			&n.ID, &n.TreeID, &n.UserID, &n.ParentID, &n.SponsorID,
			&n.Position, &n.Depth, &n.EnrolledAt, &n.CreatedAt, &n.UpdatedAt, &n.RemovedAt,
			&n.RemovedByEventID,
		)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// DO NOTHING leaves the transaction usable where a unique violation would
// abort it.
const (
	createProjectionSQL = `INSERT INTO tree_projections (tree_id, projected_version) VALUES ($1, 0)
		 ON CONFLICT (tree_id) DO NOTHING`
	lockProjectionSQL = `SELECT projected_version FROM tree_projections WHERE tree_id = $1 FOR UPDATE`
	setProjectionSQL  = `UPDATE tree_projections SET projected_version = $2, updated_at = clock_timestamp()
		 WHERE tree_id = $1`
)

func (s *PostgresTreeStore) ProjectedVersion(ctx context.Context, treeID string) (int64, bool, error) {
	var version int64
	err := s.pool.QueryRow(ctx, `SELECT projected_version FROM tree_projections WHERE tree_id = $1`, treeID).
		Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return version, true, nil
}

func (s *PostgresTreeStore) ProjectedRows(ctx context.Context, treeID string) (int64, bool, []TreeNodeRow, error) {
	// One snapshot for both reads. A projection committing between two
	// separate reads would pair an old version with newer rows.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return 0, false, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var version int64
	found := true
	err = tx.QueryRow(ctx, `SELECT projected_version FROM tree_projections WHERE tree_id = $1`, treeID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		found = false
	} else if err != nil {
		return 0, false, nil, err
	}
	if s.afterVersionRead != nil {
		s.afterVersionRead()
	}
	rows, err := tx.Query(ctx, getByTreeDepthOrderedSQL, treeID)
	if err != nil {
		return 0, false, nil, err
	}
	nodes, err := scanTreeNodes(rows)
	rows.Close()
	if err != nil {
		return 0, false, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, nil, err
	}
	return version, found, nodes, nil
}

func (s *PostgresTreeStore) ProjectInsert(ctx context.Context, node TreeNodeRow, eventVersion int64) error {
	return s.project(ctx, node.TreeID, eventVersion, func(tx pgx.Tx) error {
		return insertNode(ctx, tx, node)
	})
}

const (
	tombstoneRemovedSQL = `UPDATE tree_nodes SET removed_at = $4, updated_at = $4, removed_by_event_id = $3
		 WHERE tree_id = $1 AND user_id = ANY($2::text[]::uuid[]) AND removed_at IS NULL`
	responsorRemovedSQL = `UPDATE tree_nodes AS n SET sponsor_id = m.new_sponsor_id, updated_at = now()
		 FROM unnest($2::text[]::uuid[], $3::text[]::uuid[]) AS m(user_id, new_sponsor_id)
		 WHERE n.tree_id = $1 AND n.user_id = m.user_id AND n.removed_at IS NULL`
	relocateSurvivorsSQL = `UPDATE tree_nodes AS n SET parent_id = r.parent_id, position = r.position, depth = r.depth, updated_at = now()
		 FROM unnest($2::text[]::uuid[], $3::text[]::uuid[], $4::int[], $5::int[]) AS r(user_id, parent_id, position, depth)
		 WHERE n.tree_id = $1 AND n.user_id = r.user_id AND n.removed_at IS NULL`
)

func (s *PostgresTreeStore) ProjectRemoval(
	ctx context.Context,
	treeID string,
	r RemovalProjection,
	removalEventID string,
	eventVersion int64,
) error {
	if len(r.Users) == 0 {
		return fmt.Errorf("project removal in tree %s: the removal names no users", treeID)
	}
	return s.project(ctx, treeID, eventVersion, func(tx pgx.Tx) error {
		return projectRemoval(ctx, tx, treeID, r, removalEventID)
	})
}

// projectRemoval runs a removal's three writes in tx.
func projectRemoval(ctx context.Context, tx pgx.Tx, treeID string, r RemovalProjection, removalEventID string) error {
	// Tombstones first. A relocation run before them collides with the slot a
	// removed row still holds.
	at, err := tombstoneTime(ctx, tx)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, tombstoneRemovedSQL, treeID, r.Users, removalEventID, at)
	if err != nil {
		return fmt.Errorf("tombstone removed users in tree %s: %w", treeID, err)
	}
	if tag.RowsAffected() != int64(len(r.Users)) {
		return fmt.Errorf("tombstoning removed users %v in tree %s matched %d of %d active rows",
			r.Users, treeID, tag.RowsAffected(), len(r.Users))
	}

	if len(r.Responsored) > 0 {
		users := make([]string, len(r.Responsored))
		sponsors := make([]string, len(r.Responsored))
		for i, m := range r.Responsored {
			users[i], sponsors[i] = m.UserID, m.NewSponsorID
		}
		tag, err := tx.Exec(ctx, responsorRemovedSQL, treeID, users, sponsors)
		if err != nil {
			return fmt.Errorf("re-sponsor recruits in tree %s: %w", treeID, err)
		}
		if tag.RowsAffected() != int64(len(r.Responsored)) {
			return fmt.Errorf("re-sponsoring %d recruits in tree %s updated %d active rows",
				len(r.Responsored), treeID, tag.RowsAffected())
		}
	}

	if len(r.Relocated) > 0 {
		users := make([]string, len(r.Relocated))
		parents := make([]string, len(r.Relocated))
		positions := make([]int, len(r.Relocated))
		depths := make([]int, len(r.Relocated))
		for i, m := range r.Relocated {
			users[i], parents[i], positions[i], depths[i] = m.UserID, m.ParentID, m.Position, m.Depth
		}
		tag, err := tx.Exec(ctx, relocateSurvivorsSQL, treeID, users, parents, positions, depths)
		if err != nil {
			if c := conflictError(err); c != nil {
				return fmt.Errorf("relocate survivors in tree %s: %w: %w", treeID, c, err)
			}
			return fmt.Errorf("relocate survivors in tree %s: %w", treeID, err)
		}
		if tag.RowsAffected() != int64(len(r.Relocated)) {
			return fmt.Errorf("relocating %d survivors in tree %s updated %d active rows",
				len(r.Relocated), treeID, tag.RowsAffected())
		}
	}
	return nil
}

const softDeleteRejectedSQL = `UPDATE tree_nodes SET removed_at = $3, updated_at = $3
		 WHERE tree_id = $1 AND id = $2 AND removed_at IS NULL`

func (s *PostgresTreeStore) ProjectRejection(ctx context.Context, treeID, rejectedEventID string, eventVersion int64) error {
	return s.project(ctx, treeID, eventVersion, func(tx pgx.Tx) error {
		at, err := tombstoneTime(ctx, tx)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, softDeleteRejectedSQL, treeID, rejectedEventID, at)
		return err
	})
}

// project runs write in one transaction that refuses an event below the tree's
// projected version, and records eventVersion when it is higher.
func (s *PostgresTreeStore) project(ctx context.Context, treeID string, eventVersion int64, write func(pgx.Tx) error) error {
	if err := checkEventVersion(treeID, eventVersion); err != nil {
		return err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Create the row, then lock it, so two first projections of one tree queue
	// on it. Without the lock, a lower version could commit after a higher one
	// and move the version back.
	if _, err := tx.Exec(ctx, createProjectionSQL, treeID); err != nil {
		return err
	}
	var projected int64
	if err := tx.QueryRow(ctx, lockProjectionSQL, treeID).Scan(&projected); err != nil {
		return err
	}
	if eventVersion < projected {
		return &ProjectionRefusedError{TreeID: treeID, EventVersion: eventVersion, ProjectedVersion: projected}
	}
	if err := write(tx); err != nil {
		return err
	}
	if eventVersion > projected {
		if _, err := tx.Exec(ctx, setProjectionSQL, treeID, eventVersion); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PostgresTreeStore) UndoRootProjection(ctx context.Context, treeID, userID string, eventVersion int64) error {
	if err := checkEventVersion(treeID, eventVersion); err != nil {
		return err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var projected int64
	err = tx.QueryRow(ctx, lockProjectionSQL, treeID).Scan(&projected)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("tree %s has no projection row; the root row for %s at version %d was not deleted",
			treeID, userID, eventVersion)
	}
	if err != nil {
		return err
	}
	at, err := tombstoneTime(ctx, tx)
	if err != nil {
		return err
	}
	if projected != eventVersion {
		return fmt.Errorf("tree %s has projected version %d, not %d; the root row for %s was not deleted",
			treeID, projected, eventVersion, userID)
	}
	tag, err := tx.Exec(ctx, softDeleteRootSQL, treeID, userID, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("soft delete for root %s in tree %s matched %d active rows; the projected version was not changed",
			userID, treeID, tag.RowsAffected())
	}
	if _, err := tx.Exec(ctx, setProjectionSQL, treeID, eventVersion-1); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
