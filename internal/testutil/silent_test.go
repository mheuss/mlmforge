package testutil

import (
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClearTimeoutEnv_UnsetsTheDriverSettings(t *testing.T) {
	t.Setenv("PGCONNECT_TIMEOUT", "3")
	t.Setenv("PGSERVICE", "x")
	t.Setenv("PGSERVICEFILE", "/nonexistent")

	ClearTimeoutEnv(t)

	for _, name := range []string{"PGCONNECT_TIMEOUT", "PGSERVICE", "PGSERVICEFILE"} {
		_, present := os.LookupEnv(name)
		require.False(t, present, name)
	}
}

func TestSilentListener_AcceptsAndNeverWrites(t *testing.T) {
	addr := SilentListener(t)

	conn, err := net.DialTimeout("tcp", addr, time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = conn.Write([]byte("startup"))
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(200*time.Millisecond)))
	_, err = conn.Read(make([]byte, 1))

	var netErr net.Error
	require.ErrorAs(t, err, &netErr)
	require.True(t, netErr.Timeout(), "read returned %v; want a timeout", err)
}
