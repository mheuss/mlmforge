package main

import (
	"os"
	"strings"
	"testing"

	"github.com/mlmforge/mlmforge/internal/testutil"
)

var pgContainer *testutil.PostgresContainer

// migrateHelperEnv holds newline-separated arguments for a helper subprocess to run mlmforge with.
const migrateHelperEnv = "MLMFORGE_TEST_HELPER_ARGS"

func TestMain(m *testing.M) {
	if args := os.Getenv(migrateHelperEnv); args != "" {
		os.Exit(runArgs(newRootCmd(), strings.Split(args, "\n")))
	}

	var err error
	pgContainer, err = testutil.StartPostgres()
	testutil.RequirePostgresInCI(err)

	code := m.Run()

	if pgContainer != nil {
		pgContainer.Terminate()
	}
	os.Exit(code)
}
