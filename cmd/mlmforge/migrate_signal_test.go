package main

import (
	"bytes"
	"context"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// syncBuffer is a bytes.Buffer safe to write from one goroutine while another reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// keepSignalAlive takes sig off its default disposition for this test, so a missing registration fails the test instead of ending the binary.
func keepSignalAlive(t *testing.T, sig os.Signal) {
	t.Helper()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, sig)
	t.Cleanup(func() { signal.Stop(ch) })
}

func TestMigrateSignalContext_TheFirstSignalCancelsAndPrintsOnce(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			keepSignalAlive(t, sig)
			var stderr syncBuffer
			ctx, stop := migrateSignalContext(context.Background(), &stderr)
			defer stop()

			require.NoError(t, syscall.Kill(syscall.Getpid(), sig))
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
				t.Fatalf("%s did not end the context", sig)
			}
			require.NoError(t, syscall.Kill(syscall.Getpid(), sig))
			time.Sleep(100 * time.Millisecond)

			require.Equal(t, 1, strings.Count(stderr.String(), stoppingText))
		})
	}
}

func TestMigrateSignalContext_StopEndsItAndCanRunTwice(t *testing.T) {
	ctx, stop := migrateSignalContext(context.Background(), &syncBuffer{})

	stop()
	stop()

	require.Error(t, ctx.Err())
}

func TestLockWaitNotice_NamesTheHolderWhenKnown(t *testing.T) {
	var out bytes.Buffer

	lockWaitNotice(&out)(4321)
	lockWaitNotice(&out)(0)

	require.Equal(t, "Waiting for the migration lock. Backend PID 4321 holds it.\nWaiting for the migration lock.\n", out.String())
}
