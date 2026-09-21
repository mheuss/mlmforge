package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/stretchr/testify/require"
)

// workerRelPath is the compiled Rust worker, relative to this package.
const workerRelPath = "../../engine/target/debug/network-engine-worker"

// requireWorker returns the worker path, or skips unless CI is set.
//
// Mirrors the networkengine package's own gate: a silent skip in CI would
// report green having started no worker.
func requireWorker(t *testing.T) string {
	t.Helper()
	path := os.Getenv(workerPathEnv)
	if path == "" {
		path = workerRelPath
	}
	abs, err := filepath.Abs(path)
	require.NoError(t, err)
	info, err := os.Stat(abs)
	if err != nil {
		if ci := os.Getenv("CI"); ci != "" {
			t.Fatalf("worker binary not found at %s and CI=%q; build it with 'cargo build --workspace' in engine/", abs, ci)
		}
		t.Skipf("worker binary not found at %s", abs)
	}
	requireWorkerNotStale(t, abs, info.ModTime())
	return abs
}

// requireWorkerNotStale fails when a Rust source is newer than the binary.
//
// The networkengine package has its own freshness guard and this test package
// cannot reach it. Without one here, a focused run of these cases can exercise
// a worker built before the last change to the engine.
func requireWorkerNotStale(t *testing.T, binPath string, built time.Time) {
	t.Helper()
	root, err := filepath.Abs("../../engine")
	require.NoError(t, err)

	var newest string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// target/ holds sources cargo regenerates during the same build, and
		// a crate's tests/ compiles into its own binary, never into this one.
		if d.IsDir() && (d.Name() == "target" || d.Name() == "tests") {
			return fs.SkipDir
		}
		if d.IsDir() || filepath.Ext(path) != ".rs" {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if info.ModTime().After(built) {
			newest = path
		}
		return nil
	})
	require.NoError(t, err)
	if newest != "" {
		t.Fatalf("worker at %s is older than %s; rebuild with 'cargo build --workspace' in engine/ (do not touch the binary)", binPath, newest)
	}
}

// testTreeID and testUserID build deterministic UUIDs. tree_id, user_id and
// parent_id are UUID columns, so a bare label is rejected by Postgres.
func testTreeID(n int) string { return fmt.Sprintf("aaaaaaaa-aaaa-aaaa-aaaa-%012d", n) }
func testUserID(n int) string { return fmt.Sprintf("00000000-0000-0000-0000-%012d", n) }

// cmdOutput keeps the two streams apart. One buffer cannot tell a message
// written to stdout from the same message written to stderr, and the load
// command uses both.
type cmdOutput struct {
	stdout bytes.Buffer
	stderr bytes.Buffer
}

// runTreeCmd executes one tree subcommand.
func runTreeCmd(t *testing.T, args ...string) (*cmdOutput, error) {
	t.Helper()
	cmd := newTreeCmd()
	out := &cmdOutput{}
	cmd.SetOut(&out.stdout)
	cmd.SetErr(&out.stderr)
	cmd.SetArgs(args)
	return out, cmd.Execute()
}

// seedTree writes a root and n children straight to tree_nodes.
//
// The rows are seeded through the store rather than through a command,
// because nothing in this branch writes a tree event. That is HEU-301's, and
// the mutation commands were split out on 2026-09-19.
func seedTree(t *testing.T, pool *pgxpool.Pool, treeID string, children int) {
	t.Helper()
	store := networkengine.NewPostgresTreeStore(pool)
	root := testUserID(1)
	require.NoError(t, store.InsertNode(t.Context(), networkengine.TreeNodeRow{
		ID: testTreeID(90), TreeID: treeID, UserID: root, SponsorID: &root,
		Depth: 0, EnrolledAt: time.Unix(1, 0).UTC(),
	}))
	for i := range children {
		user := testUserID(i + 2)
		require.NoError(t, store.InsertNode(t.Context(), networkengine.TreeNodeRow{
			ID: testTreeID(100 + i), TreeID: treeID, UserID: user,
			ParentID: &root, SponsorID: &root,
			Depth: 1, EnrolledAt: time.Unix(int64(i+2), 0).UTC(),
		}))
	}
}

// The ticket's symptom, inverted: the tree persistence layer is now reachable
// from the binary, with no test helper in the path.
//
// Success alone does not prove the rows were read. LoadTree short circuits to
// nil on an empty tree and the command prints the same line either way, so
// this case passes with nothing seeded. TestTreeLoad_ReadsTheStoredRows below
// is what distinguishes the two.
func TestTreeLoad_SucceedsForAWellFormedTree(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	tree := testTreeID(1)
	seedTree(t, pool, tree, 2)

	out, err := runTreeCmd(t,
		"load",
		"--db-url", pgContainer.DSN,
		"--worker", worker,
		"--tree-id", tree,
		"--tree-type", "unilevel",
	)

	require.NoError(t, err)
	require.Equal(t, "loaded tree "+tree+"\n", out.stdout.String())
	require.Empty(t, out.stderr.String())
}

// The rows have to leave Postgres and reach the loader. A tree holding only a
// child and no depth-0 root is rejected by name, and that rejection is
// unreachable unless the rows were read: an unread tree short circuits to
// success.
//
// parent_id carries no foreign key, so a child whose parent is absent is
// insertable.
func TestTreeLoad_ReadsTheStoredRows(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	pool := pgContainer.NewPool(t)
	tree := testTreeID(4)

	store := networkengine.NewPostgresTreeStore(pool)
	orphan, absent := testUserID(11), testUserID(12)
	require.NoError(t, store.InsertNode(t.Context(), networkengine.TreeNodeRow{
		ID: testTreeID(110), TreeID: tree, UserID: orphan,
		ParentID: &absent, SponsorID: &absent,
		Depth: 1, EnrolledAt: time.Unix(1, 0).UTC(),
	}))

	out, err := runTreeCmd(t,
		"load",
		"--db-url", pgContainer.DSN,
		"--worker", worker,
		"--tree-id", tree,
		"--tree-type", "unilevel",
	)

	require.Error(t, err)
	require.Contains(t, out.stderr.String(), "engine is unchanged")
	require.Empty(t, out.stdout.String())
}

// An empty tree is not an error. LoadTree short circuits on zero rows, and the
// command has to report that as success rather than inventing a failure.
func TestTreeLoad_AnEmptyTreeIsNotAFailure(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	_ = pgContainer.NewPool(t)

	_, err := runTreeCmd(t,
		"load",
		"--db-url", pgContainer.DSN,
		"--worker", worker,
		"--tree-id", testTreeID(2),
		"--tree-type", "unilevel",
	)

	require.NoError(t, err)
}

// A config error is refused before the store is read, so it must not need a
// reachable database to report cleanly. The message goes to stderr, because
// the command returns it rather than printing it.
func TestTreeLoad_ReportsAConfigRejection(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	_ = pgContainer.NewPool(t)

	out, err := runTreeCmd(t,
		"load",
		"--db-url", pgContainer.DSN,
		"--worker", worker,
		"--tree-id", testTreeID(3),
		"--tree-type", "streamline",
	)

	require.Error(t, err)
	require.Contains(t, out.stderr.String(), "engine is unchanged")
	require.Empty(t, out.stdout.String())
	require.NotContains(t, out.stderr.String(), "Usage:")
}
