package platform

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/lib/pq"
	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// libpqDocumentedEnv lists the environment variables the PostgreSQL 17 libpq documentation describes.
var libpqDocumentedEnv = []string{
	"PGHOST", "PGSSLNEGOTIATION", "PGHOSTADDR", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD",
	"PGPASSFILE", "PGREQUIREAUTH", "PGCHANNELBINDING", "PGSERVICE", "PGSERVICEFILE", "PGOPTIONS",
	"PGAPPNAME", "PGSSLMODE", "PGREQUIRESSL", "PGSSLCOMPRESSION", "PGSSLCERT", "PGSSLKEY",
	"PGSSLCERTMODE", "PGSSLROOTCERT", "PGSSLCRL", "PGSSLCRLDIR", "PGSSLSNI", "PGREQUIREPEER",
	"PGSSLMINPROTOCOLVERSION", "PGSSLMAXPROTOCOLVERSION", "PGGSSENCMODE", "PGKRBSRVNAME",
	"PGGSSLIB", "PGGSSDELEGATION", "PGCONNECT_TIMEOUT", "PGCLIENTENCODING",
	"PGTARGETSESSIONATTRS", "PGLOADBALANCEHOSTS", "PGDATESTYLE", "PGTZ", "PGGEQO",
	"PGSYSCONFDIR", "PGLOCALEDIR",
}

// refusedURL names a loopback port that refuses connections.
const refusedURL = "postgres://u:p@127.0.0.1:1/db?sslmode=disable"

// libpqPanics reports whether lib/pq panics while building a connector from the environment.
func libpqPanics() (panicked bool) {
	defer func() { panicked = recover() != nil }()
	_, _ = pq.NewConnector("postgres://u@h/d")
	return false
}

func TestUnsupportedEnvNames_MatchWhatLibPQPanicsOn(t *testing.T) {
	names := slices.Clone(libpqDocumentedEnv)
	for _, name := range unsupportedEnvNames {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			testutil.ClearLibPQEnv(t)
			t.Setenv(name, "x")

			assert.Equal(t, slices.Contains(unsupportedEnvNames, name), libpqPanics())
		})
	}
}

func TestRefuseUnsupportedEnv_NamesEachVariableAlone(t *testing.T) {
	for _, name := range unsupportedEnvNames {
		t.Run(name, func(t *testing.T) {
			testutil.ClearLibPQEnv(t)
			t.Setenv(name, "value-7f3")

			err := refuseUnsupportedEnv()

			require.EqualError(t, err, "mlmforge migrate refused to open the database: the environment sets "+name+
				". mlmforge migrate does not accept "+name+". Unset it and run the command again.")
			assert.NotContains(t, err.Error(), "value-7f3")
		})
	}
}

func TestRefuseUnsupportedEnv_AnEmptyValueCounts(t *testing.T) {
	testutil.ClearLibPQEnv(t)
	t.Setenv("PGSERVICE", "")

	var unsupported *UnsupportedEnvError
	require.ErrorAs(t, refuseUnsupportedEnv(), &unsupported)
	assert.Equal(t, []string{"PGSERVICE"}, unsupported.Names)
}

func TestRefuseUnsupportedEnv_NamesEveryVariableSetInListOrder(t *testing.T) {
	testutil.ClearLibPQEnv(t)
	t.Setenv("PGSYSCONFDIR", "sys-value-7f3")
	t.Setenv("PGSERVICE", "svc-value-7f3")
	t.Setenv("PGHOSTADDR", "")

	require.EqualError(t, refuseUnsupportedEnv(), "mlmforge migrate refused to open the database: the environment sets "+
		"PGHOSTADDR, PGSERVICE and PGSYSCONFDIR. mlmforge migrate does not accept these variables. Unset them and run the command again.")
}

func TestUnsupportedEnvError_TwoNamesJoinWithAnd(t *testing.T) {
	err := &UnsupportedEnvError{Names: []string{"PGSERVICE", "PGSYSCONFDIR"}}

	assert.Equal(t, "mlmforge migrate refused to open the database: the environment sets PGSERVICE and PGSYSCONFDIR. "+
		"mlmforge migrate does not accept these variables. Unset them and run the command again.", err.Error())
}

func TestRefuseUnsupportedEnv_NothingSetPasses(t *testing.T) {
	testutil.ClearLibPQEnv(t)

	assert.NoError(t, refuseUnsupportedEnv())
}

func TestMigrateCommands_RefuseAnUnsupportedVariableBeforeOpeningAnything(t *testing.T) {
	calls := map[string]func(dir string) error{
		"up":   func(dir string) error { return MigrateUp(refusedURL, dir) },
		"down": func(dir string) error { return MigrateDown(refusedURL, dir) },
		"version": func(dir string) error {
			_, err := MigrateVersion(refusedURL, dir)
			return err
		},
		"reset-dirty": func(dir string) error {
			_, err := ResetDirty(refusedURL, dir)
			return err
		},
		"reset-dirty --after-failed-down": func(dir string) error {
			_, err := ResetAfterFailedDown(refusedURL, dir)
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			testutil.ClearLibPQEnv(t)
			t.Setenv("PGSERVICE", "svc-value-7f3")

			var unsupported *UnsupportedEnvError
			require.ErrorAs(t, call(FindMigrationsDir(t)), &unsupported)
		})
	}
}

func TestMigrateVersion_RefusesTheEnvironmentBeforeOpeningTheSource(t *testing.T) {
	testutil.ClearLibPQEnv(t)
	t.Setenv("PGSERVICE", "svc-value-7f3")

	_, err := MigrateVersion(refusedURL, filepath.Join(t.TempDir(), "missing"))

	var unsupported *UnsupportedEnvError
	require.ErrorAs(t, err, &unsupported)
}
