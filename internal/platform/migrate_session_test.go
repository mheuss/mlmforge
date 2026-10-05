package platform

import (
	"context"
	"errors"
	"net/url"
	"os"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/lib/pq"
	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReleaseSession_RunsEveryStepInOrderAndTagsEachFailure(t *testing.T) {
	var calls []string
	err := releaseSession(
		func() error { calls = append(calls, "unlock"); return errors.New("u") },
		func() (error, error) { calls = append(calls, "migrator"); return errors.New("s"), nil },
		func() error { calls = append(calls, "db"); return errors.New("d") },
	)

	assert.Equal(t, []string{"unlock", "migrator", "db"}, calls)
	releases, rest := SplitRelease(err)
	assert.NoError(t, rest)
	require.Len(t, releases, 3)
	assert.EqualError(t, releases[0], "releasing the mlmforge migration lock failed: u")
	assert.EqualError(t, releases[1], "closing the migration drivers failed: s")
	assert.EqualError(t, releases[2], "closing the database connection failed: d")
}

func TestSessionOptions_PutsTheCheckIntervalFirst(t *testing.T) {
	cases := map[string]struct{ fromURL, fromEnv, want string }{
		"nothing else":               {want: "-c client_connection_check_interval=1000"},
		"options in the URL":         {fromURL: "-c work_mem=64MB", want: "-c client_connection_check_interval=1000 -c work_mem=64MB"},
		"PGOPTIONS only":             {fromEnv: "-c work_mem=32MB", want: "-c client_connection_check_interval=1000 -c work_mem=32MB"},
		"the URL replaces PGOPTIONS": {fromURL: "-c work_mem=64MB", fromEnv: "-c work_mem=32MB", want: "-c client_connection_check_interval=1000 -c work_mem=64MB"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, sessionOptions(tc.fromURL, tc.fromEnv))
		})
	}
}

// driverOption returns the value lib/pq holds for key after parsing driverURL's form of rawURL, or "" if it holds none.
func driverOption(t *testing.T, rawURL, key string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	c, err := pq.NewConnector(driverURL(u))
	require.NoError(t, err)
	opts := reflect.ValueOf(c).Elem().FieldByName("opts")
	require.True(t, opts.IsValid(), "lib/pq's Connector has no opts field to read")
	v := opts.MapIndex(reflect.ValueOf(key))
	if !v.IsValid() {
		return ""
	}
	return v.String()
}

func TestDriverURL_WhatLibPQReceives(t *testing.T) {
	const base = "postgres://u:p@h/d?sslmode=disable"
	cases := map[string]struct {
		rawURL, pgOptions, key, want string
	}{
		"the interval alone":                      {rawURL: base, key: "options", want: "-c client_connection_check_interval=1000"},
		"an empty URL options keeps PGOPTIONS":    {rawURL: base + "&options=", pgOptions: "-c search_path=ops", key: "options", want: "-c client_connection_check_interval=1000 -c search_path=ops"},
		"the URL's options replace PGOPTIONS":     {rawURL: base + "&options=-c%20work_mem%3D64MB", pgOptions: "-c search_path=ops", key: "options", want: "-c client_connection_check_interval=1000 -c work_mem=64MB"},
		"the default application name":            {rawURL: base, key: "application_name", want: "mlmforge-migrate"},
		"the operator's fallback name is kept":    {rawURL: base + "&fallback_application_name=ops", key: "application_name", want: "ops"},
		"an empty fallback name gets the default": {rawURL: base + "&fallback_application_name=", key: "application_name", want: "mlmforge-migrate"},
		"golang-migrate's settings are removed":   {rawURL: base + "&x-multi-statement=true", key: "x-multi-statement", want: ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			testutil.ClearLibPQEnv(t)
			t.Setenv("PGAPPNAME", "")
			require.NoError(t, os.Unsetenv("PGAPPNAME"))
			t.Setenv("PGOPTIONS", tc.pgOptions)

			assert.Equal(t, tc.want, driverOption(t, tc.rawURL, tc.key))
		})
	}
}

// throughProxy returns dsn with its host and port replaced by addr.
func throughProxy(t *testing.T, dsn, addr string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	u.Host = addr
	return u.String()
}

func TestDialSession_ACutConnectionRollsTheFileBack(t *testing.T) {
	cases := map[string]struct {
		options string
		exists  bool
	}{
		"the default check interval":       {exists: false},
		"control: the interval turned off": {options: "&options=-c%20client_connection_check_interval%3D0", exists: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			testutil.ClearLibPQEnv(t)
			t.Setenv("PGOPTIONS", "")
			dsn := newResetDatabase(t)
			target, err := url.Parse(dsn)
			require.NoError(t, err)
			addr, cut := testutil.CuttableProxy(t, target.Host)
			s, err := dialSession(context.Background(), throughProxy(t, dsn, addr)+tc.options)
			require.NoError(t, err)
			defer func() { _ = s.close() }()
			go func() {
				_, _ = s.conn.ExecContext(context.Background(), "SELECT pg_sleep(3); CREATE TABLE cut_one (id int);")
			}()
			testutil.WaitForActiveQuery(t, pgContainer.DSN, "CREATE TABLE cut_one")

			cut()
			testutil.WaitForQueryGone(t, pgContainer.DSN, "CREATE TABLE cut_one")

			assert.Equal(t, tc.exists, testutil.TableExists(t, dsn, "cut_one"))
		})
	}
}

func TestReleaseSession_ReturnsNilWhenEveryStepSucceeds(t *testing.T) {
	err := releaseSession(
		func() error { return nil },
		func() (error, error) { return nil, nil },
		func() error { return nil },
	)

	assert.NoError(t, err)
}

func TestMigrateUp_NamesItsConnection(t *testing.T) {
	cases := map[string]struct{ param, env, want string }{
		"no application_name in the URL": {want: "mlmforge-migrate"},
		"the URL's own application_name": {param: "&application_name=ops-run", want: "ops-run"},
		"PGAPPNAME and none in the URL":  {env: "env-run", want: "env-run"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PGAPPNAME", tc.env)
			if tc.env == "" {
				// An empty PGAPPNAME would name the connection "".
				require.NoError(t, os.Unsetenv("PGAPPNAME"))
			}
			dsn := newResetDatabase(t)
			dir := testutil.SlowMigrations(t, 1)
			done := make(chan error, 1)
			go func() { done <- MigrateUp(context.Background(), dsn+tc.param, dir, nil) }()

			pid := testutil.WaitForActiveQuery(t, pgContainer.DSN, "slow_one")
			admin, err := pgx.Connect(t.Context(), pgContainer.DSN)
			require.NoError(t, err)
			defer func() { _ = admin.Close(context.Background()) }()
			var appName string
			require.NoError(t, admin.QueryRow(t.Context(), "SELECT application_name FROM pg_stat_activity WHERE pid = $1", pid).Scan(&appName))

			assert.Equal(t, tc.want, appName)
			require.NoError(t, <-done)
		})
	}
}

func TestDialSession_HoldsAtMostOneConnection(t *testing.T) {
	s, err := dialSession(context.Background(), newResetDatabase(t))
	require.NoError(t, err)
	defer func() { _ = s.close() }()

	assert.Equal(t, 1, s.db.Stats().MaxOpenConnections)
}
