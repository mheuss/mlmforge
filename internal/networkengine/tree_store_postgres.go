package networkengine

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Compile-time check.
var _ TreeStore = (*PostgresTreeStore)(nil)

// PostgresTreeStore is a TreeStore backed by PostgreSQL.
type PostgresTreeStore struct {
	pool *pgxpool.Pool
}

const insertNodeSQL = `INSERT INTO tree_nodes (id, tree_id, user_id, parent_id, sponsor_id, position, depth, enrolled_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

// treeNodeSelectColumns is the SELECT column list for tree_nodes queries.
// Order must match the scanTreeNode/scanTreeNodes Scan call.
const treeNodeSelectColumns = `id, tree_id, user_id, parent_id, sponsor_id, position, depth, enrolled_at, created_at, updated_at, removed_at`

func NewPostgresTreeStore(pool *pgxpool.Pool) *PostgresTreeStore {
	return &PostgresTreeStore{pool: pool}
}

func (s *PostgresTreeStore) InsertNode(ctx context.Context, node TreeNodeRow) error {
	_, err := s.pool.Exec(ctx, insertNodeSQL,
		node.ID, node.TreeID, node.UserID, node.ParentID, node.SponsorID, node.Position, node.Depth, node.EnrolledAt,
	)
	return err
}

func (s *PostgresTreeStore) DeleteNode(ctx context.Context, treeID, userID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE tree_nodes SET removed_at = now(), updated_at = now()
		 WHERE tree_id = $1 AND user_id = $2 AND removed_at IS NULL`,
		treeID, userID,
	)
	return err
}

func (s *PostgresTreeStore) GetNode(ctx context.Context, treeID, userID string) (*TreeNodeRow, error) {
	row := s.pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT %s FROM tree_nodes WHERE tree_id = $1 AND user_id = $2 AND removed_at IS NULL`, treeNodeSelectColumns),
		treeID, userID,
	)
	return scanTreeNode(row)
}

func (s *PostgresTreeStore) GetChildren(ctx context.Context, treeID, parentUserID string) ([]TreeNodeRow, error) {
	rows, err := s.pool.Query(ctx,
		fmt.Sprintf(`SELECT %s FROM tree_nodes WHERE tree_id = $1 AND parent_id = $2 AND removed_at IS NULL`, treeNodeSelectColumns),
		treeID, parentUserID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTreeNodes(rows)
}

func (s *PostgresTreeStore) GetByTree(ctx context.Context, treeID string) ([]TreeNodeRow, error) {
	rows, err := s.pool.Query(ctx,
		fmt.Sprintf(`SELECT %s FROM tree_nodes WHERE tree_id = $1 AND removed_at IS NULL`, treeNodeSelectColumns),
		treeID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTreeNodes(rows)
}

func (s *PostgresTreeStore) GetByTreeDepthOrdered(ctx context.Context, treeID string) ([]TreeNodeRow, error) {
	rows, err := s.pool.Query(ctx,
		fmt.Sprintf(`SELECT %s FROM tree_nodes WHERE tree_id = $1 AND removed_at IS NULL ORDER BY depth ASC, enrolled_at ASC`, treeNodeSelectColumns),
		treeID,
	)
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
		_, err := tx.Exec(ctx, insertNodeSQL,
			node.ID, node.TreeID, node.UserID, node.ParentID, node.SponsorID, node.Position, node.Depth, node.EnrolledAt,
		)
		if err != nil {
			return fmt.Errorf("bulk insert node %s: %w", node.UserID, err)
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
		)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}
