package testutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// connectFor opens a connection to dsn that closes when the test ends.
func connectFor(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// SlowMigrations writes two migrations whose files each sleep for seconds before they create or drop a table, and returns the directory.
func SlowMigrations(t *testing.T, seconds int) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"1_slow_one.up.sql":   fmt.Sprintf("SELECT pg_sleep(%d); CREATE TABLE slow_one (id int);", seconds),
		"1_slow_one.down.sql": fmt.Sprintf("SELECT pg_sleep(%d); DROP TABLE slow_one;", seconds),
		"2_slow_two.up.sql":   fmt.Sprintf("SELECT pg_sleep(%d); CREATE TABLE slow_two (id int);", seconds),
		"2_slow_two.down.sql": fmt.Sprintf("SELECT pg_sleep(%d); DROP TABLE slow_two;", seconds),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// WaitForActiveQuery polls pg_stat_activity until a backend runs a query containing text, and returns its PID.
func WaitForActiveQuery(t *testing.T, dsn, text string) int {
	t.Helper()
	conn := connectFor(t, dsn)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var pid int
		err := conn.QueryRow(context.Background(),
			"SELECT pid FROM pg_stat_activity WHERE state = 'active' AND pid <> pg_backend_pid() AND strpos(query, $1) > 0 LIMIT 1",
			text).Scan(&pid)
		if err == nil {
			return pid
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no backend ran a query containing %q within 10s", text)
	return 0
}

// HoldAdvisoryLock takes the advisory lock (key1, key2) on a new connection to dsn, and returns that backend's PID and a function that releases it.
func HoldAdvisoryLock(t *testing.T, dsn string, key1, key2 int32) (int, func()) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect the lock holder: %v", err)
	}
	var once sync.Once
	release := func() { once.Do(func() { _ = conn.Close(context.Background()) }) }
	t.Cleanup(release)
	var pid int
	if err := conn.QueryRow(context.Background(), "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatalf("read the holder's PID: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1, $2)", key1, key2); err != nil {
		t.Fatalf("take advisory lock (%d, %d): %v", key1, key2, err)
	}
	return pid, release
}

// TryAdvisoryLock reports whether a new connection to dsn gets the advisory lock (key1, key2) at once, and closes that connection.
func TryAdvisoryLock(t *testing.T, dsn string, key1, key2 int32) bool {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var granted bool
	if err := conn.QueryRow(context.Background(), "SELECT pg_try_advisory_lock($1, $2)", key1, key2).Scan(&granted); err != nil {
		t.Fatalf("try advisory lock (%d, %d): %v", key1, key2, err)
	}
	return granted
}

// ReadRecord returns the row in dsn's schema_migrations, or -1 and false when the table has no row.
func ReadRecord(t *testing.T, dsn string) (int, bool) {
	t.Helper()
	var version int
	var dirty bool
	err := connectFor(t, dsn).QueryRow(context.Background(), "SELECT version, dirty FROM schema_migrations LIMIT 1").Scan(&version, &dirty)
	if errors.Is(err, pgx.ErrNoRows) {
		return -1, false
	}
	if err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	return version, dirty
}

// TableExists reports whether dsn's public schema has a table named name.
func TableExists(t *testing.T, dsn, name string) bool {
	t.Helper()
	var exists bool
	if err := connectFor(t, dsn).QueryRow(context.Background(),
		"SELECT to_regclass('public.' || $1) IS NOT NULL", name).Scan(&exists); err != nil {
		t.Fatalf("look up table %s: %v", name, err)
	}
	return exists
}

// AcceptingListener starts a loopback listener that accepts connections and never writes to them.
// It returns the address and a channel that closes when the first connection is accepted.
func AcceptingListener(t *testing.T) (string, <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on the loopback: %v", err)
	}
	accepted := make(chan struct{})
	done := make(chan struct{})
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		defer close(done)
		first := true
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
			if first {
				close(accepted)
				first = false
			}
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range held {
			_ = conn.Close()
		}
	})
	return ln.Addr().String(), accepted
}

// WaitForQueryGone polls pg_stat_activity until no backend's current or last query contains text.
func WaitForQueryGone(t *testing.T, dsn, text string) {
	t.Helper()
	conn := connectFor(t, dsn)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := conn.QueryRow(context.Background(),
			"SELECT count(*) FROM pg_stat_activity WHERE pid <> pg_backend_pid() AND strpos(query, $1) > 0", text).Scan(&n); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if n == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("a backend's query still contained %q after 10s", text)
}

// CuttableProxy forwards loopback connections to target, and returns its address and a function that closes every connection it carries.
func CuttableProxy(t *testing.T, target string) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on the loopback: %v", err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	keep := func(c net.Conn) {
		mu.Lock()
		defer mu.Unlock()
		conns = append(conns, c)
	}
	cut := func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	}
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", target)
			if err != nil {
				_ = client.Close()
				continue
			}
			keep(client)
			keep(server)
			pipe := func(dst, src net.Conn) {
				_, _ = io.Copy(dst, src)
				_ = dst.Close()
				_ = src.Close()
			}
			go pipe(server, client)
			go pipe(client, server)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		cut()
	})
	return ln.Addr().String(), cut
}

// RequireNoAdvisoryLocks fails unless dsn's database holds no advisory lock within 3s.
func RequireNoAdvisoryLocks(t *testing.T, dsn string) {
	t.Helper()
	requireCountReachesZero(t, dsn, "advisory locks",
		"SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND database = (SELECT oid FROM pg_database WHERE datname = current_database())")
}

// RequireNoSessionsNamed fails unless dsn's database has no backend with application name name within 3s.
func RequireNoSessionsNamed(t *testing.T, dsn, name string) {
	t.Helper()
	requireCountReachesZero(t, dsn, "sessions named "+name,
		"SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND application_name = $1", name)
}

// requireCountReachesZero polls query until it returns 0 or 3s pass.
func requireCountReachesZero(t *testing.T, dsn, what, query string, args ...any) {
	t.Helper()
	conn := connectFor(t, dsn)
	var n int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := conn.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", what, err)
		}
		if n == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%d %s remained after 3s", n, what)
}
