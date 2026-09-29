package main

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestResolveDBURL(t *testing.T) {
	tests := []struct {
		name  string
		flag  string
		env   string
		want  string
		wantE bool
	}{
		{name: "flag wins", flag: "postgres://flag", env: "postgres://env", want: "postgres://flag?connect_timeout=10"},
		{name: "env is the fallback", env: "postgres://env", want: "postgres://env?connect_timeout=10"},
		{name: "neither is an error", wantE: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testutil.ClearTimeoutEnv(t)
			t.Setenv("DATABASE_URL", tt.env)

			got, err := resolveDBURL(tt.flag)

			if tt.wantE {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got.url)
		})
	}
}

func TestResolveDBURL_ConnectTimeout(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		env     map[string]string
		want    string
		timeout timeoutSource
	}{
		{name: "no query", raw: "postgres://u:p@db:5432/app", want: "postgres://u:p@db:5432/app?connect_timeout=10", timeout: timeoutAdded},
		{name: "existing query", raw: "postgres://db/app?sslmode=disable", want: "postgres://db/app?sslmode=disable&connect_timeout=10", timeout: timeoutAdded},
		{name: "trailing question mark", raw: "postgres://db/app?", want: "postgres://db/app?connect_timeout=10", timeout: timeoutAdded},
		{name: "trailing ampersand", raw: "postgres://db/app?sslmode=disable&", want: "postgres://db/app?sslmode=disable&connect_timeout=10", timeout: timeoutAdded},
		{name: "stray question mark in a value", raw: "postgres://db/app?application_name=x?", want: "postgres://db/app?application_name=x?&connect_timeout=10", timeout: timeoutAdded},
		{name: "fragment", raw: "postgres://db/app?sslmode=disable#frag", want: "postgres://db/app?sslmode=disable&connect_timeout=10#frag", timeout: timeoutAdded},
		{name: "postgresql scheme", raw: "postgresql://db/app", want: "postgresql://db/app?connect_timeout=10", timeout: timeoutAdded},
		{name: "encoded userinfo is kept", raw: "postgres://u:p%40ss@db/app?sslmode=disable", want: "postgres://u:p%40ss@db/app?sslmode=disable&connect_timeout=10", timeout: timeoutAdded},
		{name: "operator sets 5", raw: "postgres://db/app?connect_timeout=5", want: "postgres://db/app?connect_timeout=5", timeout: timeoutFromURL},
		{name: "operator sets 0", raw: "postgres://db/app?connect_timeout=0", want: "postgres://db/app?connect_timeout=0", timeout: timeoutFromURL},
		{name: "PGCONNECT_TIMEOUT", raw: "postgres://db/app", env: map[string]string{"PGCONNECT_TIMEOUT": "3"}, want: "postgres://db/app", timeout: timeoutFromEnv},
		{name: "URL wins over PGCONNECT_TIMEOUT", raw: "postgres://db/app?connect_timeout=5", env: map[string]string{"PGCONNECT_TIMEOUT": "3"}, want: "postgres://db/app?connect_timeout=5", timeout: timeoutFromURL},
		{name: "empty value", raw: "postgres://db/app?connect_timeout=", want: "postgres://db/app?connect_timeout=", timeout: timeoutNone},
		{name: "empty value with PGCONNECT_TIMEOUT", raw: "postgres://db/app?connect_timeout=", env: map[string]string{"PGCONNECT_TIMEOUT": "3"}, want: "postgres://db/app?connect_timeout=", timeout: timeoutFromEnv},
		{name: "service in the query", raw: "postgres://db/app?service=x", want: "postgres://db/app?service=x", timeout: timeoutFromService},
		{name: "PGSERVICE", raw: "postgres://db/app", env: map[string]string{"PGSERVICE": "x"}, want: "postgres://db/app", timeout: timeoutFromService},
		{name: "service in the query with PGCONNECT_TIMEOUT", raw: "postgres://db/app?service=x", env: map[string]string{"PGCONNECT_TIMEOUT": "3"}, want: "postgres://db/app?service=x", timeout: timeoutFromEnvOrService},
		{name: "PGSERVICE with PGCONNECT_TIMEOUT", raw: "postgres://db/app", env: map[string]string{"PGSERVICE": "x", "PGCONNECT_TIMEOUT": "3"}, want: "postgres://db/app", timeout: timeoutFromEnvOrService},
		{name: "keyword form", raw: "host=db dbname=app", want: "host=db dbname=app", timeout: timeoutUnknown},
		{name: "another scheme", raw: "mysql://db/app", want: "mysql://db/app", timeout: timeoutUnknown},
		{name: "unparseable", raw: "postgres://u:p@[::1]:5432,[::2]/app", want: "postgres://u:p@[::1]:5432,[::2]/app", timeout: timeoutUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testutil.ClearTimeoutEnv(t)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}

			got, err := resolveDBURL(tt.raw)

			require.NoError(t, err)
			require.Equal(t, tt.want, got.url)
			require.Equal(t, tt.timeout, got.timeout)
		})
	}
}

func TestResolveDBURL_AddingTheTimeoutKeepsThePath(t *testing.T) {
	testutil.ClearTimeoutEnv(t)
	for _, raw := range []string{
		"postgres://u:p@db:5432/app",
		"postgres://db/app?sslmode=disable",
		"postgres://db/app?",
		"postgres://db/app?sslmode=disable&",
		"postgres://db/app?application_name=x?",
		"postgres://db/app?sslmode=disable#frag",
		"postgresql://db/app",
		"postgres://u:p%40ss@db/app?sslmode=disable",
		"postgresql://db/app%2Fx",
	} {
		t.Run(raw, func(t *testing.T) {
			got, err := resolveDBURL(raw)
			require.NoError(t, err)

			in, err := url.Parse(raw)
			require.NoError(t, err)
			out, err := url.Parse(got.url)
			require.NoError(t, err)
			require.Equal(t, in.Path, out.Path)
			require.Equal(t, in.RawPath, out.RawPath)
			require.Equal(t, "10", out.Query().Get("connect_timeout"))
			cfg, err := pgconn.ParseConfig(got.url)
			require.NoError(t, err)
			require.Equal(t, 10*time.Second, cfg.ConnectTimeout)
		})
	}
}

func TestResolveWorkerPath_NamesThePathItTried(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "network-engine-worker")
	t.Setenv(workerPathEnv, "")

	_, err := resolveWorkerPath(missing)

	require.Error(t, err)
	require.Contains(t, err.Error(), missing, "the message must name the path that was tried")
	require.Contains(t, err.Error(), "--worker", "the message must name the source it read")
}

func TestResolveWorkerPath_ReadsTheEnvVarWhenTheFlagIsEmpty(t *testing.T) {
	present := filepath.Join(t.TempDir(), "network-engine-worker")
	require.NoError(t, os.WriteFile(present, []byte("#!/bin/sh\n"), 0o755))
	t.Setenv(workerPathEnv, present)

	got, err := resolveWorkerPath("")

	require.NoError(t, err)
	require.Equal(t, present, got)
}

func TestResolveWorkerPath_NamesTheEnvVarWhenThatIsWhereThePathCameFrom(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "network-engine-worker")
	t.Setenv(workerPathEnv, missing)

	_, err := resolveWorkerPath("")

	require.Error(t, err)
	require.Contains(t, err.Error(), missing)
	require.Contains(t, err.Error(), workerPathEnv)
	require.NotContains(t, err.Error(), "--worker")
}

func TestResolveWorkerPath_ResolvesARelativePath(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "network-engine-worker"), []byte("#!/bin/sh\n"), 0o755))
	t.Chdir(dir)
	t.Setenv(workerPathEnv, "")
	cwd, err := os.Getwd()
	require.NoError(t, err)

	got, err := resolveWorkerPath("./network-engine-worker")

	require.NoError(t, err)
	require.True(t, filepath.IsAbs(got), "got %q", got)
	require.Equal(t, filepath.Join(cwd, "network-engine-worker"), got)
}

func TestResolveWorkerPath_FlagWinsOverTheEnvVar(t *testing.T) {
	dir := t.TempDir()
	flagPath := filepath.Join(dir, "from-flag")
	envPath := filepath.Join(dir, "from-env")
	for _, p := range []string{flagPath, envPath} {
		require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755))
	}
	t.Setenv(workerPathEnv, envPath)

	got, err := resolveWorkerPath(flagPath)

	require.NoError(t, err)
	require.Equal(t, flagPath, got)
}

func TestResolveWorkerPath_ErrorsWhenNeitherIsSet(t *testing.T) {
	t.Setenv(workerPathEnv, "")

	_, err := resolveWorkerPath("")

	require.Error(t, err)
	require.Contains(t, err.Error(), workerPathEnv)
}

// A path that exists but cannot be run is a worker-path failure, so it has to
// name the path and the source the way an absent one does.
func TestResolveWorkerPath_RejectsAPathItCannotRun(t *testing.T) {
	dir := t.TempDir()
	notExecutable := filepath.Join(dir, "network-engine-worker")
	require.NoError(t, os.WriteFile(notExecutable, []byte("#!/bin/sh\n"), 0o644))

	for _, tt := range []struct{ name, path string }{
		{"a directory", dir},
		{"a file without the execute bit", notExecutable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(workerPathEnv, "")

			_, err := resolveWorkerPath(tt.path)

			require.Error(t, err)
			require.ErrorContains(t, err, tt.path, "the message must name the path it tried")
			require.ErrorContains(t, err, "--worker", "the message must name the source")
		})
	}
}
