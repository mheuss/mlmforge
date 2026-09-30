package main

import (
	"fmt"
	"testing"

	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestOpenTreeDeps_ARefusedConnStringHoldsNoPassword(t *testing.T) {
	for _, tc := range testutil.ConnStringCases() {
		if tc.PgxStage == "" {
			continue
		}
		t.Run(tc.Name, func(t *testing.T) {
			testutil.IsolatePgxEnv(t)
			want := "open database pool: " + testutil.RefusalText(t, "pgx", tc.PgxStage)

			deps, err := openTreeDeps(t.Context(), tc.ConnString, testWorker(t))

			require.Nil(t, deps)
			require.Error(t, err)
			testutil.RequireNoPasswordWindow(t, fmt.Sprintf("%v\n%+v", err, err), tc.Password, want, tc.WithoutPassword())
			testutil.RequireNoDriverParseError(t, err)
			require.EqualError(t, err, want)
			var cse *platform.ConnStringError
			require.ErrorAs(t, err, &cse)
			require.Equal(t, "pgx", cse.Driver())
			require.Equal(t, tc.PgxStage, cse.Stage())
		})
	}
}
