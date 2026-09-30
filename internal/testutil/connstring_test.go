package testutil

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPasswordWindows_FindsAPartialLeak(t *testing.T) {
	found := PasswordWindows(`invalid port ":Zm9v" after host`, "Zm9vQmFy")

	require.Equal(t, []string{"Zm9v"}, found)
}

func TestPasswordWindows_FindsNothingInUnrelatedText(t *testing.T) {
	require.Empty(t, PasswordWindows("open database pool: pgx refused the connection string.", "Zm9vQmFy/cXV4eHl6"))
}

func TestConnStringCases_EachStringCarriesItsPassword(t *testing.T) {
	for _, tc := range ConnStringCases() {
		require.True(t, strings.Contains(tc.ConnString, tc.Password), tc.Name)
	}
}

func TestConnStringCases_NoPasswordOverlapsTheTextAroundIt(t *testing.T) {
	for _, tc := range ConnStringCases() {
		known := []string{
			tc.WithoutPassword(),
			RefusalText(t, "golang-migrate", "parse"),
			RefusalText(t, "golang-migrate", "scheme"),
			RefusalText(t, "pgx", "parse"),
			RefusalText(t, "pgx", "refused"),
		}
		for _, k := range known {
			require.Empty(t, PasswordWindows(k, tc.Password), "%s overlaps %q", tc.Name, k)
		}
	}
}

func TestIsolatePgxEnv_PointsTheServiceFilesAtAnEmptyDirectory(t *testing.T) {
	IsolatePgxEnv(t)

	require.True(t, strings.HasSuffix(os.Getenv("PGSERVICEFILE"), "pg_service.conf"))
	_, err := os.Stat(os.Getenv("PGSERVICEFILE"))
	require.True(t, os.IsNotExist(err))
	require.NotEmpty(t, os.Getenv("PGSYSCONFDIR"))
}
