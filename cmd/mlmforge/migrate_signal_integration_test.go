package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startMigrate runs one migrate subcommand in a goroutine and returns the channel its error arrives on.
// The command is cancelled and awaited when the test ends.
func startMigrate(t *testing.T, stderr io.Writer, dsn, dir string, args ...string) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	root := newRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(stderr)
	root.SetArgs(append(append([]string{"migrate"}, args...), "--db-url", dsn, "--migrations", dir))
	result := make(chan error, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		result <- root.ExecuteContext(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-exited:
		case <-time.After(15 * time.Second):
			t.Errorf("the command was still running 15s after its context was cancelled")
		}
	})
	return result
}

// awaitText polls b until it contains text or 5s pass.
func awaitText(t *testing.T, b *syncBuffer, text string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(b.String(), text) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("stderr never held %q; it held %q", text, b.String())
}

// awaitDone returns the error from done, or fails after limit.
func awaitDone(t *testing.T, done <-chan error, limit time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatalf("the command was still running after %s", limit)
		return nil
	}
}

func TestMigrateUp_ASignalDuringTheLockWaitChangesNothing(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			keepSignalAlive(t, sig)
			dsn := newMigrateDatabase(t)
			holder, release := testutil.HoldAdvisoryLock(t, dsn, platform.MigrateLockNamespace, 0)
			defer release()
			var stderr syncBuffer
			done := startMigrate(t, &stderr, dsn, platform.FindMigrationsDir(t), "up")
			awaitText(t, &stderr, "Waiting for the migration lock.")

			require.NoError(t, syscall.Kill(syscall.Getpid(), sig))
			err := awaitDone(t, done, 2*time.Second)

			require.EqualError(t, err, "Stopped while waiting for the migration lock. Nothing was changed.")
			assert.Equal(t, 1, exitCode(err))
			assert.Equal(t, 1, strings.Count(stderr.String(), fmt.Sprintf("Backend PID %d holds it.", holder)))
			assert.Contains(t, stderr.String(), stoppingText)
			assert.False(t, testutil.TableExists(t, dsn, "schema_migrations"))
		})
	}
}

func TestMigrateUp_ASignalDuringAStalledConnectStopsAtOnce(t *testing.T) {
	keepSignalAlive(t, syscall.SIGINT)
	testutil.ClearTimeoutEnv(t)
	addr, accepted := testutil.AcceptingListener(t)
	var stderr syncBuffer
	done := startMigrate(t, &stderr, "postgres://app:s3cretpw@"+addr+"/app?sslmode=disable&connect_timeout=0", platform.FindMigrationsDir(t), "up")
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("the command never connected to the listener")
	}

	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGINT))
	err := awaitDone(t, done, 2*time.Second)

	require.EqualError(t, err, "Stopped while connecting to the database. Nothing was changed.")
	assert.Equal(t, 1, exitCode(err))
	assert.Contains(t, stderr.String(), stoppingText)
	assert.NotContains(t, stderr.String(), "s3cretpw")
}

func TestMigrateUp_ASignalDuringAFileStopsAfterIt(t *testing.T) {
	keepSignalAlive(t, syscall.SIGTERM)
	dsn := newMigrateDatabase(t)
	var stderr syncBuffer
	done := startMigrate(t, &stderr, dsn, testutil.SlowMigrations(t, 2), "up")
	testutil.WaitForActiveQuery(t, pgContainer.DSN, "slow_one")

	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGTERM))
	err := awaitDone(t, done, 5*time.Second)

	require.EqualError(t, err, "Stopped after migration 1. Run `mlmforge migrate up` again to apply the rest.")
	assert.Equal(t, 1, exitCode(err))
	version, dirty := testutil.ReadRecord(t, dsn)
	assert.Equal(t, platform.Record{Version: 1}, platform.Record{Version: version, Dirty: dirty})
	assert.False(t, testutil.TableExists(t, dsn, "slow_two"))
}

func TestMigrateUp_ASignalDuringTheLastFileSucceeds(t *testing.T) {
	keepSignalAlive(t, syscall.SIGINT)
	dsn := newMigrateDatabase(t)
	var stderr syncBuffer
	done := startMigrate(t, &stderr, dsn, testutil.SlowMigrations(t, 2), "up")
	testutil.WaitForActiveQuery(t, pgContainer.DSN, "slow_two")

	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGINT))
	err := awaitDone(t, done, 5*time.Second)

	require.NoError(t, err)
	assert.Contains(t, stderr.String(), stoppingText)
	version, dirty := testutil.ReadRecord(t, dsn)
	assert.Equal(t, platform.Record{Version: 2}, platform.Record{Version: version, Dirty: dirty})
}

func TestMigrateDown_ASignalDuringItsFileFinishesTheRollback(t *testing.T) {
	keepSignalAlive(t, syscall.SIGINT)
	dsn := newMigrateDatabase(t)
	dir := testutil.SlowMigrations(t, 2)
	require.NoError(t, awaitDone(t, startMigrate(t, io.Discard, dsn, dir, "up"), 10*time.Second))
	var stderr syncBuffer
	done := startMigrate(t, &stderr, dsn, dir, "down")
	testutil.WaitForActiveQuery(t, pgContainer.DSN, "DROP TABLE slow_two")

	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGINT))
	err := awaitDone(t, done, 5*time.Second)

	require.NoError(t, err)
	assert.Contains(t, stderr.String(), stoppingText)
	version, dirty := testutil.ReadRecord(t, dsn)
	assert.Equal(t, platform.Record{Version: 1}, platform.Record{Version: version, Dirty: dirty})
	assert.False(t, testutil.TableExists(t, dsn, "slow_two"))
}
