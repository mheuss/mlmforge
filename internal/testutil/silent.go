package testutil

import (
	"net"
	"os"
	"sync"
	"testing"
)

// ClearTimeoutEnv unsets the driver settings that change which connect timeout applies, for one test.
func ClearTimeoutEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"PGCONNECT_TIMEOUT", "PGSERVICE", "PGSERVICEFILE"} {
		// Setenv first so the original value comes back after the test.
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
	}
}

// SilentListener starts a loopback TCP listener that accepts connections and never writes to them.
func SilentListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on the loopback: %v", err)
	}
	var mu sync.Mutex
	// Holding each connection keeps it open until cleanup.
	var held []net.Conn
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range held {
			_ = conn.Close()
		}
	})
	return ln.Addr().String()
}
