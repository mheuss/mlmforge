package main

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrateUp_ASecondSignalEndsTheProcess(t *testing.T) {
	dsn := newMigrateDatabase(t)
	dir := testutil.SlowMigrations(t, 5)
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), migrateHelperEnv+"="+strings.Join([]string{"migrate", "up", "--db-url", dsn, "--migrations", dir}, "\n"))
	var stderr syncBuffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	testutil.WaitForActiveQuery(t, pgContainer.DSN, "slow_one")

	require.NoError(t, cmd.Process.Signal(syscall.SIGINT))
	awaitText(t, &stderr, stoppingText)
	select {
	case err := <-exited:
		t.Fatalf("the first SIGINT ended the process: %v", err)
	default:
	}
	require.NoError(t, cmd.Process.Signal(syscall.SIGINT))
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("the process was still running 1s after the second SIGINT")
	}

	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	require.True(t, ok)
	require.True(t, status.Signaled(), "exit status %v", cmd.ProcessState)
	assert.Equal(t, syscall.SIGINT, status.Signal())
	assert.Contains(t, stderr.String(), stoppingText)
	testutil.WaitForQueryGone(t, pgContainer.DSN, "CREATE TABLE slow_one")
	version, dirty := testutil.ReadRecord(t, dsn)
	assert.Equal(t, platform.Record{Version: 1, Dirty: true}, platform.Record{Version: version, Dirty: dirty})
	assert.False(t, testutil.TableExists(t, dsn, "slow_one"), "the killed file must roll back")
}
