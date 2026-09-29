package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/require"
)

var waitedPattern = regexp.MustCompile(`waited (\d+\.\d)s`)

// maskWait replaces the observed wait in text and returns it in seconds.
func maskWait(t *testing.T, text string) (string, float64) {
	t.Helper()
	m := waitedPattern.FindStringSubmatch(text)
	require.NotNil(t, m, "no wait in %q", text)
	seconds, err := strconv.ParseFloat(m[1], 64)
	require.NoError(t, err)
	return waitedPattern.ReplaceAllString(text, "waited Ns"), seconds
}

// executeRoot runs one command line through the real command tree.
func executeRoot(args ...string) error {
	root := newRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs(args)
	return root.Execute()
}

// testWorker returns an executable path that the tree commands accept as --worker.
func testWorker(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "network-engine-worker")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	return path
}

// requireNoDriverText fails when text carries the password or the driver's own wording.
func requireNoDriverText(t *testing.T, text, password string) {
	t.Helper()
	require.NotContains(t, text, password)
	for _, raw := range []string{"failed to receive message", "i/o timeout", "context deadline exceeded", "read tcp"} {
		require.NotContains(t, text, raw)
	}
}

func TestMigrateCommands_ReportAConnectTimeout(t *testing.T) {
	for _, sub := range []string{"up", "down", "version", "reset-dirty"} {
		t.Run(sub, func(t *testing.T) {
			testutil.ClearTimeoutEnv(t)
			addr := testutil.SilentListener(t)
			url := "postgres://app:s3cretpw@" + addr + "/app?sslmode=disable&connect_timeout=1"

			start := time.Now()
			err := executeRoot("migrate", sub, "--db-url", url, "--migrations", platform.FindMigrationsDir(t))

			require.Less(t, time.Since(start), 5*time.Second)
			require.Error(t, err)
			text, waited := maskWait(t, err.Error())
			require.Equal(t, "open database: the connection to "+addr+" did not complete; waited Ns (connect_timeout from the URL)", text)
			require.GreaterOrEqual(t, waited, 1.0)
			require.Less(t, waited, 5.0)
			requireNoDriverText(t, err.Error(), "s3cretpw")
		})
	}
}

func TestMigrateVersion_AConnectTimeoutOmitsAQueryPassword(t *testing.T) {
	testutil.ClearTimeoutEnv(t)
	addr := testutil.SilentListener(t)
	url := "postgres://app@" + addr + "/app?sslmode=disable&connect_timeout=1&password=qs3cretpw"

	err := executeRoot("migrate", "version", "--db-url", url, "--migrations", platform.FindMigrationsDir(t))

	require.Error(t, err)
	text, waited := maskWait(t, err.Error())
	require.Equal(t, "open database: the connection to "+addr+" did not complete; waited Ns (connect_timeout from the URL)", text)
	require.Less(t, waited, 5.0)
	requireNoDriverText(t, err.Error(), "qs3cretpw")
}

func TestMigrateVersion_TheAddedDefaultReachesTheDriver(t *testing.T) {
	testutil.ClearTimeoutEnv(t)
	previous := defaultConnectTimeout
	defaultConnectTimeout = 1
	t.Cleanup(func() { defaultConnectTimeout = previous })
	addr := testutil.SilentListener(t)
	url := "postgres://app:s3cretpw@" + addr + "/app?sslmode=disable"

	start := time.Now()
	err := executeRoot("migrate", "version", "--db-url", url, "--migrations", platform.FindMigrationsDir(t))

	require.Less(t, time.Since(start), 5*time.Second)
	require.Error(t, err)
	requireNoDriverText(t, err.Error(), "s3cretpw")
	text, waited := maskWait(t, err.Error())
	require.Equal(t, "open database: the connection to "+addr+" did not complete; waited Ns (connect_timeout=1, added by mlmforge because the URL set none)", text)
	require.GreaterOrEqual(t, waited, 1.0)
	require.Less(t, waited, 5.0)
}

func TestTreeLoad_ReportsAConnectTimeout(t *testing.T) {
	for _, tc := range []struct {
		name     string
		url      func(addr string) string
		password string
	}{
		{name: "password in userinfo", password: "s3cretpw", url: func(addr string) string {
			return "postgres://app:s3cretpw@" + addr + "/app?sslmode=disable&connect_timeout=1"
		}},
		{name: "password in the query", password: "qs3cretpw", url: func(addr string) string {
			return "postgres://app@" + addr + "/app?sslmode=disable&connect_timeout=1&password=qs3cretpw"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testutil.ClearTimeoutEnv(t)
			addr := testutil.SilentListener(t)

			start := time.Now()
			err := executeRoot("tree", "load", "--db-url", tc.url(addr), "--worker", testWorker(t),
				"--tree-id", "t9", "--tree-type", "unilevel")

			require.Less(t, time.Since(start), 5*time.Second)
			require.Error(t, err)
			text, waited := maskWait(t, err.Error())
			require.Equal(t, "reach database: the connection to "+addr+" did not complete; waited Ns (connect_timeout from the URL)", text)
			require.GreaterOrEqual(t, waited, 1.0)
			require.Less(t, waited, 5.0)
			requireNoDriverText(t, err.Error(), tc.password)
		})
	}
}

func TestTreeLoad_ReportsEveryHostAndTheCombinedWait(t *testing.T) {
	testutil.ClearTimeoutEnv(t)
	first, second := testutil.SilentListener(t), testutil.SilentListener(t)
	url := "postgres://app:s3cretpw@" + first + "," + second + "/app?sslmode=disable&connect_timeout=1"

	err := executeRoot("tree", "load", "--db-url", url, "--worker", testWorker(t),
		"--tree-id", "t9", "--tree-type", "unilevel")

	require.Error(t, err)
	text, waited := maskWait(t, err.Error())
	require.Equal(t, "reach database: the connection to "+first+","+second+" did not complete; waited Ns (connect_timeout from the URL)", text)
	require.GreaterOrEqual(t, waited, 2.0)
	require.Less(t, waited, 5.0)
}

func TestConnectError_NamesEachTimeoutSource(t *testing.T) {
	cte := &platform.ConnectTimeoutError{Hosts: "db:5432", Waited: 1500 * time.Millisecond, Err: errors.New("driver")}
	for source, want := range map[timeoutSource]string{
		timeoutAdded:            "(connect_timeout=10, added by mlmforge because the URL set none)",
		timeoutFromURL:          "(connect_timeout from the URL)",
		timeoutFromEnv:          "(PGCONNECT_TIMEOUT)",
		timeoutFromService:      "(no connect_timeout added; a service is named)",
		timeoutFromEnvOrService: "(PGCONNECT_TIMEOUT or the named service)",
		timeoutNone:             "(no connect timeout applies)",
		timeoutUnknown:          "(no connect_timeout added; the URL was not changed)",
	} {
		err := connectError(cte, dbTarget{timeout: source})

		require.Equal(t, "the connection to db:5432 did not complete; waited 1.5s "+want, err.Error())
		require.ErrorIs(t, err, cte)
	}
}

func TestConnectError_PassesOtherErrorsThrough(t *testing.T) {
	other := errors.New("open database: connection refused")

	require.Same(t, other, connectError(other, dbTarget{timeout: timeoutAdded}))
	require.NoError(t, connectError(nil, dbTarget{timeout: timeoutAdded}))
}

func TestConnectError_LeavesAnHEU830ErrorAsItRendered(t *testing.T) {
	applyErr := &platform.ApplyError{
		Err:        errors.New("migration failed: detail"),
		After:      platform.RecordRead{Record: platform.Record{Version: 6, Dirty: true}},
		Source:     platform.SourceInfo{Path: "/m", InSource: true, Previous: 5, HasPrevious: true},
		BodyFailed: true,
	}
	target := dbTarget{url: "postgres://db/app", timeout: timeoutAdded}

	require.Equal(t, migrateError("up", applyErr).Error(), migrateError("up", connectError(applyErr, target)).Error())
}

func TestConnectError_InsertsTheSourceAfterTheTimeoutUnderEveryPrefix(t *testing.T) {
	cte := &platform.ConnectTimeoutError{
		Hosts:  "db:5432",
		Waited: 10 * time.Second,
		Err:    errors.New("failed to connect to `user=app` with password s3cretpw"),
	}
	tree := "aaaaaaaa-aaaa-aaaa-aaaa-000000000001"
	lockErr := fmt.Errorf("lock tree %s: %w", tree, fmt.Errorf("open the lock connection for tree %s: %w", tree, cte))

	err := connectError(lockErr, dbTarget{timeout: timeoutAdded})

	require.Equal(t, "lock tree "+tree+": open the lock connection for tree "+tree+
		": the connection to db:5432 did not complete; waited 10.0s (connect_timeout=10, added by mlmforge because the URL set none)", err.Error())
	require.ErrorIs(t, err, cte)
	requireNoDriverText(t, err.Error(), "s3cretpw")
}

func TestConnectError_KeepsTextThatFollowsTheTimeout(t *testing.T) {
	cte := &platform.ConnectTimeoutError{Hosts: "db:5432", Waited: time.Second, Err: errors.New("driver")}
	wrapped := fmt.Errorf("add root: %w; the command's context ended and no append was confirmed", cte)

	err := connectError(wrapped, dbTarget{timeout: timeoutFromURL})

	require.Equal(t, "add root: the connection to db:5432 did not complete; waited 1.0s (connect_timeout from the URL); "+
		"the command's context ended and no append was confirmed", err.Error())
}

func TestConnectError_AppendsTheSourceWhenTheTimeoutTextIsNotEmbedded(t *testing.T) {
	cte := &platform.ConnectTimeoutError{Hosts: "db:5432", Waited: time.Second, Err: errors.New("driver")}
	opaque := &operatorError{text: "the tree lock was not acquired", err: cte}

	err := connectError(opaque, dbTarget{timeout: timeoutFromEnv})

	require.Equal(t, "the tree lock was not acquired (PGCONNECT_TIMEOUT)", err.Error())
	require.ErrorIs(t, err, cte)
}
