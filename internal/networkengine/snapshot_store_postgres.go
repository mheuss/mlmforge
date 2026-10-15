package networkengine

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Compile-time check.
var _ SnapshotStore = (*PostgresSnapshotStore)(nil)

// PostgresSnapshotStore is a SnapshotStore backed by PostgreSQL.
type PostgresSnapshotStore struct {
	pool *pgxpool.Pool
}

func NewPostgresSnapshotStore(pool *pgxpool.Pool) *PostgresSnapshotStore {
	return &PostgresSnapshotStore{pool: pool}
}

// The WHERE clause is what keeps a put from lowering the version.
const putSnapshotSQL = `INSERT INTO tree_snapshots (tree_id, version, tree_type, data) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (tree_id) DO UPDATE
		    SET version = excluded.version, tree_type = excluded.tree_type,
		        data = excluded.data, taken_at = now()
		  WHERE excluded.version > tree_snapshots.version`

func (s *PostgresSnapshotStore) Get(ctx context.Context, treeID string) (Snapshot, bool, error) {
	snap := Snapshot{TreeID: treeID}
	err := s.pool.QueryRow(ctx, `SELECT version, tree_type, data FROM tree_snapshots WHERE tree_id = $1`, treeID).
		Scan(&snap.Version, &snap.TreeType, &snap.Data)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, err
	}
	return snap, true, nil
}

func (s *PostgresSnapshotStore) Put(ctx context.Context, snap Snapshot) error {
	_, err := s.pool.Exec(ctx, putSnapshotSQL, snap.TreeID, snap.Version, snap.TreeType, snap.Data)
	return err
}

func (s *PostgresSnapshotStore) Delete(ctx context.Context, treeID string, version int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM tree_snapshots WHERE tree_id = $1 AND version = $2`, treeID, version)
	return err
}
