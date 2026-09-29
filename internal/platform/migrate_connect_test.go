package platform

import (
	"testing"
	"time"

	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestMigrateVersion_ReportsAConnectTimeout(t *testing.T) {
	testutil.ClearTimeoutEnv(t)
	addr := testutil.SilentListener(t)

	_, err := MigrateVersion("postgres://app:s3cret@"+addr+"/app?sslmode=disable&connect_timeout=1", FindMigrationsDir(t))

	var cte *ConnectTimeoutError
	require.ErrorAs(t, err, &cte)
	require.Equal(t, addr, cte.Hosts)
	require.GreaterOrEqual(t, cte.Waited, time.Second)
	require.Less(t, cte.Waited, 2*time.Second)
	require.ErrorContains(t, err, "open database: the connection to "+addr+" did not complete")
	require.NotContains(t, err.Error(), "s3cret")
}
