package networkengine

import (
	"os"
	"testing"

	"github.com/mlmforge/mlmforge/internal/testutil"
)

var pgContainer *testutil.PostgresContainer

func TestMain(m *testing.M) {
	var err error
	pgContainer, err = testutil.StartPostgres()
	testutil.RequirePostgresInCI(err)

	code := m.Run()

	if pgContainer != nil {
		pgContainer.Terminate()
	}
	os.Exit(code)
}
