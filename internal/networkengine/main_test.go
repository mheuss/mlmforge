package networkengine

import (
	"fmt"
	"os"
	"testing"

	"github.com/mlmforge/mlmforge/internal/testutil"
)

var pgContainer *testutil.PostgresContainer

func TestMain(m *testing.M) {
	var err error
	pgContainer, err = testutil.StartPostgres()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Postgres container unavailable: %v\n", err)
	}

	code := m.Run()

	if pgContainer != nil {
		pgContainer.Terminate()
	}
	os.Exit(code)
}
