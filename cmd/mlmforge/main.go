package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/mlmforge/mlmforge/internal/observability"
	"github.com/spf13/cobra"
)

// shutdownTimeout bounds the observability flush on exit. A stuck exporter (e.g.
// the batch log processor flushing to a slow destination) must not hang the CLI
// — the fail-fast intent of Init extends to shutdown.
const shutdownTimeout = 5 * time.Second

func main() {
	// run returns the exit code so the deferred observability shutdown (which
	// flushes buffered telemetry) runs before the process exits — a bare os.Exit
	// in main would skip every defer.
	os.Exit(run())
}

func run() int {
	// Initialize observability before anything else. Fail-fast: if an operator
	// explicitly asked for a log exporter (OTEL_LOGS_EXPORTER=file) and the
	// environment can't deliver it (bad path/permissions), exit rather than run a
	// money-system migration unobserved. Unset env is the normal path and never
	// errors — the pipeline stays dormant. Revisit when a network (OTLP) exporter
	// lands, where reachability at init is a softer failure (HEU-404).
	shutdown, err := observability.Init(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "observability init: %v\n", err)
		return 1
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := shutdown(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "observability shutdown: %v\n", err)
		}
	}()

	return runArgs(newRootCmd(), os.Args[1:])
}

// runArgs executes the command tree and returns the process exit code.
func runArgs(root *cobra.Command, args []string) int {
	root.SetArgs(args)
	return exitCode(root.Execute())
}

// exitCodeError carries an exit code other than 1.
type exitCodeError struct {
	code int
	err  error
}

func (e *exitCodeError) Error() string { return e.err.Error() }
func (e *exitCodeError) Unwrap() error { return e.err }

// exitCode maps a command's error to the process exit code.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var coded *exitCodeError
	if errors.As(err, &coded) {
		return coded.code
	}
	return 1
}

// newRootCmd builds the command tree.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "mlmforge",
		Short: "MLMForge compensation engine",
	}

	root.AddCommand(newMigrateCmd())
	root.AddCommand(newTreeCmd())
	return root
}
