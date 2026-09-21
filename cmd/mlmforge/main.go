package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/mlmforge/mlmforge/internal/observability"
	"github.com/mlmforge/mlmforge/internal/platform"
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

	if err := newRootCmd().Execute(); err != nil {
		return 1
	}
	return 0
}

// newRootCmd builds the command tree.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "mlmforge",
		Short: "MLMForge compensation engine",
	}

	migrateCmd := &cobra.Command{
		Use:   "migrate",
		Short: "Database migration commands",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	dbURL := migrateCmd.PersistentFlags().String("db-url", "", "PostgreSQL connection URL (or set DATABASE_URL env var)")
	migrationsPath := migrateCmd.PersistentFlags().String("migrations", "./migrations", "Path to migration files")

	migrateCmd.AddCommand(
		&cobra.Command{
			Use:   "up",
			Short: "Apply all pending migrations",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				url, err := resolveDBURL(*dbURL)
				if err != nil {
					return err
				}
				return platform.MigrateUp(url, *migrationsPath)
			},
		},
		&cobra.Command{
			Use:   "down",
			Short: "Roll back the most recent migration",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				url, err := resolveDBURL(*dbURL)
				if err != nil {
					return err
				}
				if err := platform.MigrateDown(url, *migrationsPath); err != nil {
					if errors.Is(err, platform.ErrNoChange) {
						fmt.Println("No migrations to roll back.")
						return nil
					}
					return err
				}
				fmt.Println("Rolled back one migration.")
				return nil
			},
		},
		&cobra.Command{
			Use:   "version",
			Short: "Show current migration version",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				url, err := resolveDBURL(*dbURL)
				if err != nil {
					return err
				}
				v, dirty, err := platform.MigrateVersion(url, *migrationsPath)
				if err != nil {
					return err
				}
				fmt.Printf("Version: %d, Dirty: %v\n", v, dirty)
				return nil
			},
		},
	)

	root.AddCommand(migrateCmd)
	root.AddCommand(newTreeCmd())
	return root
}
