package main

import (
	"bytes"
	"errors"
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
// A silent skip in CI would report green having started no worker.
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
		// A permission or I/O error is not an absent binary, and skipping on
		// one is indistinguishable from a pass.
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("stat worker binary at %s: %v", abs, err)
		}
		if ci := os.Getenv("CI"); ci != "" {
			t.Fatalf("stat worker binary at %s: %v (CI=%q); build it with 'cargo build --workspace' in engine/", abs, err, ci)
		}
		t.Skipf("stat worker binary at %s: %v", abs, err)
	}
	requireWorkerNotStale(t, abs, info.ModTime())
	return abs
}

// requireWorkerNotStale fails when a Rust source is newer than the binary.
//
// Without this check, a focused run of these cases can exercise a worker built
// before the last change to the engine.
func requireWorkerNotStale(t *testing.T, binPath string, built time.Time) {
	t.Helper()
	root, err := filepath.Abs("../../engine")
	require.NoError(t, err)

	generated := filepath.Join(root, "target")

	var newer string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Matching on the directory name alone would also skip a tests
		// directory under src/, whose files do reach the worker.
		if d.IsDir() {
			if path == generated || isCrateIntegrationTests(path) {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".rs" {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if info.ModTime().After(built) {
			newer = path
		}
		return nil
	})
	require.NoError(t, err)
	if newer != "" {
		t.Fatalf("worker at %s is older than %s; rebuild with 'cargo build --workspace' in engine/ (do not touch the binary)", binPath, newer)
	}
}

// isCrateIntegrationTests reports whether path is a "tests" directory sitting
// beside a Cargo.toml. Those files compile into their own binaries, so the
// worker can never be stale against them.
func isCrateIntegrationTests(path string) bool {
	if filepath.Base(path) != "tests" {
		return false
	}
	_, err := os.Stat(filepath.Join(filepath.Dir(path), "Cargo.toml"))
	return err == nil
}

// testTreeID and testUserID build deterministic UUIDs for test rows.
func testTreeID(n int) string { return fmt.Sprintf("aaaaaaaa-aaaa-aaaa-aaaa-%012d", n) }
func testUserID(n int) string { return fmt.Sprintf("00000000-0000-0000-0000-%012d", n) }

// cmdOutput keeps the two streams apart. One buffer cannot tell a message
// written to stdout from the same message written to stderr.
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
// The rows are seeded through the store rather than through a command.
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

// The node count is what separates this from an empty tree.
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
	// The count is what makes a replayed tree distinguishable from an empty
	// one at a terminal. Root plus two children.
	require.Equal(t, "loaded tree "+tree+" (3 nodes)\n", out.stdout.String())
	require.Empty(t, out.stderr.String())
}

// The rows have to leave Postgres and reach the loader. A tree holding only a
// child and no depth-0 root is rejected by name.
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
	// The kind is the discriminating part, not the shared "engine is unchanged"
	// text.
	require.Contains(t, out.stderr.String(), "data_invalid")
	require.Empty(t, out.stdout.String())
}

// An empty tree is not an error. The command has to report that as success
// rather than inventing a failure.
func TestTreeLoad_AnEmptyTreeIsNotAFailure(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	_ = pgContainer.NewPool(t)

	tree := testTreeID(2)
	out, err := runTreeCmd(t,
		"load",
		"--db-url", pgContainer.DSN,
		"--worker", worker,
		"--tree-id", tree,
		"--tree-type", "unilevel",
	)

	require.NoError(t, err)
	require.Equal(t, "loaded tree "+tree+" (0 nodes)\n", out.stdout.String())
}

// An unsupported tree type is refused, the message reaches stderr, and cobra
// appends no usage.
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
	require.Contains(t, out.stderr.String(), "config_invalid")
	require.Empty(t, out.stdout.String())
	require.NotContains(t, out.stderr.String(), "Usage:")
}
