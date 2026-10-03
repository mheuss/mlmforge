package platform

import (
	"errors"
	"fmt"
	"testing"

	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestMigrateVersion_ARefusedConnStringHoldsNoPassword(t *testing.T) {
	for _, tc := range testutil.ConnStringCases() {
		if tc.MigrateStage == "" {
			continue
		}
		t.Run(tc.Name, func(t *testing.T) {
			testutil.ClearTimeoutEnv(t)
			testutil.ClearLibPQEnv(t)
			want := "open database: " + testutil.RefusalText(t, "golang-migrate", tc.MigrateStage)

			_, err := MigrateVersion(tc.ConnString, FindMigrationsDir(t))

			require.Error(t, err)
			testutil.RequireNoPasswordWindow(t, fmt.Sprintf("%v\n%+v", err, err), tc.Password, want, tc.WithoutPassword())
			testutil.RequireNoDriverParseError(t, err)
			require.EqualError(t, err, want)
			var cse *ConnStringError
			require.ErrorAs(t, err, &cse)
			require.Equal(t, "golang-migrate", cse.Driver())
			require.Equal(t, tc.MigrateStage, cse.Stage())
		})
	}
}

func TestMigrateVersion_APostgresURLReachesTheDriver(t *testing.T) {
	for _, url := range []string{
		"postgres://app@127.0.0.1:1/app?sslmode=disable",
		"postgresql://app@127.0.0.1:1/app?sslmode=disable",
	} {
		t.Run(url, func(t *testing.T) {
			testutil.ClearTimeoutEnv(t)
			testutil.ClearLibPQEnv(t)

			_, err := MigrateVersion(url, FindMigrationsDir(t))

			require.ErrorContains(t, err, "dial tcp 127.0.0.1:1")
			var cse *ConnStringError
			require.False(t, errors.As(err, &cse), "expected a dial error for a valid URL; the error chain holds a *ConnStringError: %v", err)
		})
	}
}

func TestMigrateVersion_AStringMigrateAcceptsReachesTheDialWithNoPassword(t *testing.T) {
	selected := 0
	for _, tc := range testutil.ConnStringCases() {
		if tc.MigrateStage != "" {
			continue
		}
		selected++
		t.Run(tc.Name, func(t *testing.T) {
			testutil.ClearTimeoutEnv(t)
			testutil.ClearLibPQEnv(t)

			_, err := MigrateVersion(tc.ConnString, FindMigrationsDir(t))

			require.Error(t, err)
			testutil.RequireNoPasswordWindow(t, fmt.Sprintf("%v\n%+v", err, err), tc.Password, tc.WithoutPassword())
			require.ErrorContains(t, err, "dial tcp 127.0.0.1:1")
		})
	}
	require.Equal(t, 4, selected, "cases migrate accepts")
}

func TestMigrateVersion_ARefusedEnvironmentWithARefusedStringHoldsNoPassword(t *testing.T) {
	testutil.ClearTimeoutEnv(t)
	testutil.ClearLibPQEnv(t)
	t.Setenv("PGCLIENTENCODING", "LATIN1")
	password := "pq3cretpwXYZ"
	connString := "postgres://app:" + password + "@127.0.0.1:1/app?p%3D%27a=z%3D"
	want := "open database: client_encoding must be absent or 'UTF8'"

	_, err := MigrateVersion(connString, FindMigrationsDir(t))

	require.Error(t, err)
	testutil.RequireNoPasswordWindow(t, fmt.Sprintf("%v\n%+v", err, err), password, want, "postgres://app:@127.0.0.1:1/app?p%3D%27a=z%3D")
	require.EqualError(t, err, want)
}

func TestMigrateVersion_AStringThatReachedTheDriverStillDoes(t *testing.T) {
	for _, tc := range testutil.ReachesDriverCases() {
		t.Run(tc.Name, func(t *testing.T) {
			testutil.ClearTimeoutEnv(t)
			testutil.ClearLibPQEnv(t)
			for name, value := range tc.Env {
				t.Setenv(name, value)
			}

			_, err := MigrateVersion(tc.ConnString, FindMigrationsDir(t))

			require.ErrorContains(t, err, tc.Dial+":")
			var cse *ConnStringError
			require.False(t, errors.As(err, &cse), "expected a dial error; the error chain holds a *ConnStringError: %v", err)
		})
	}
}
