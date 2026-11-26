package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/stretchr/testify/require"
)

// holdProjectionInsert inserts tree's tree_projections row in a transaction it
// leaves open, and returns that transaction.
func holdProjectionInsert(t *testing.T, treeID string) pgx.Tx {
	t.Helper()
	tx := holdTx(t)
	_, err := tx.Exec(context.Background(),
		`INSERT INTO tree_projections (tree_id, projected_version) VALUES ($1, 0)`, treeID)
	require.NoError(t, err)
	return tx
}

func TestTreeAddRoot_ExitsThreeThenTreeLoadHeals(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	t.Setenv("PGOPTIONS", "")
	t.Setenv(statementTimeoutEnv, "")
	t.Setenv(lockTimeoutEnv, "500ms")
	tree, root := testTreeID(1111), testUserID(1)
	conn := []string{"--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", tree}
	held := holdProjectionInsert(t, tree)

	out, err := runTreeCmd(t, append([]string{"add-root", "--user-id", root, "--sponsor-id", root,
		"--tree-type", "unilevel"}, conn...)...)

	require.Equal(t, 3, exitCode(err), "stdout: %s stderr: %s", out.stdout.String(), out.stderr.String())
	rootEvent := appendedEventID(t, out.stdout.String())
	require.Equal(t, "appended event "+rootEvent+" at version 1 to stream tree-"+tree+"; projection returned an error\n",
		out.stdout.String())
	require.Contains(t, out.stderr.String(),
		"The tree has no projection row. The next write or tree load of this tree redelivers it.")
	require.NoError(t, held.Rollback(context.Background()))
	store := networkengine.NewPostgresTreeStore(pool)
	row, err := store.GetNodeIncludingRemoved(t.Context(), tree, root)
	require.NoError(t, err)
	require.Nil(t, row, "the root has a row before the heal")
	_, found, err := store.ProjectedVersion(t.Context(), tree)
	require.NoError(t, err)
	require.False(t, found, "the tree has a projection row before the heal")

	out, err = runTreeCmd(t, append([]string{"load", "--tree-type", "unilevel"}, conn...)...)

	require.NoError(t, err, out.stderr.String())
	require.Equal(t, "redelivered event "+rootEvent+" at version 1\nloaded tree "+tree+" (1 nodes)\n", out.stdout.String())
	require.Empty(t, out.stderr.String())
	requireProjectedVersion(t, store, tree, 1)
}
