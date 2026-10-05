package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/mlmforge/mlmforge/internal/platform"
)

// stoppingText is printed when the first SIGINT or SIGTERM arrives.
const stoppingText = "Stopping. A migration that is already running will finish first. Send the signal again to stop now. That can leave the record dirty."

// migrateSignalContext returns a context that ends on the first SIGINT or SIGTERM, and a function that stops watching.
func migrateSignalContext(parent context.Context, stderr io.Writer) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case <-signals:
			// Stop registration so the next signal ends the process.
			signal.Stop(signals)
			_, _ = fmt.Fprintln(stderr, stoppingText)
			cancel()
		case <-done:
		}
	}()
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			close(done)
			<-exited
			signal.Stop(signals)
			cancel()
		})
	}
}

// lockWaitNotice returns a LockWait that prints one waiting line to w.
func lockWaitNotice(w io.Writer) platform.LockWait {
	return func(holderPID int) {
		if holderPID == 0 {
			_, _ = fmt.Fprintln(w, "Waiting for the migration lock.")
			return
		}
		_, _ = fmt.Fprintf(w, "Waiting for the migration lock. Backend PID %d holds it.\n", holderPID)
	}
}
