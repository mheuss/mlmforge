package main

import (
	"os"
	"path/filepath"
	"testing"

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
		{name: "flag wins", flag: "postgres://flag", env: "postgres://env", want: "postgres://flag"},
		{name: "env is the fallback", env: "postgres://env", want: "postgres://env"},
		{name: "neither is an error", wantE: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", tt.env)

			got, err := resolveDBURL(tt.flag)

			if tt.wantE {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
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
