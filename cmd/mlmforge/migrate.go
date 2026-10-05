package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/spf13/cobra"
)

// newMigrateCmd builds the migrate command group.
func newMigrateCmd() *cobra.Command {
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

	var afterFailedDown bool
	resetDirtyCmd := &cobra.Command{
		Use:   "reset-dirty",
		Short: "Move a dirty migration record back one migration, or forward one with --after-failed-down",
		Long: "Move a dirty migration record to a clean one.\n\n" +
			"Without --after-failed-down, it moves the record back to the previous migration. Run it only when a failed `mlmforge migrate up` printed the instruction to.\n\n" +
			"With --after-failed-down, it moves the record forward to the next migration. Run it only when a failed `mlmforge migrate down` printed the instruction to.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			command, reset, next := "reset-dirty", platform.ResetDirty, upCommand
			if afterFailedDown {
				command, reset, next = afterDownCommandName, platform.ResetAfterFailedDown, downCommand
			}
			return runMigrateCommand(cmd, command, *dbURL, func(ctx context.Context, url string, wait platform.LockWait) error {
				res, err := reset(ctx, url, *migrationsPath, wait)
				if err = withoutReleaseErrors(cmd.ErrOrStderr(), "the record was written", err); err != nil {
					return err
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), resetText(res, next))
				return nil
			})
		},
	}
	resetDirtyCmd.Flags().BoolVar(&afterFailedDown, "after-failed-down", false,
		"Move the record forward to the next migration. Use it only when a failed migrate down printed the instruction to.")

	migrateCmd.AddCommand(
		&cobra.Command{
			Use:   "up",
			Short: "Apply all pending migrations",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runMigrateCommand(cmd, "up", *dbURL, func(ctx context.Context, url string, wait platform.LockWait) error {
					return withoutReleaseErrors(cmd.ErrOrStderr(), "migrate up finished", platform.MigrateUp(ctx, url, *migrationsPath, wait))
				})
			},
		},
		&cobra.Command{
			Use:   "down",
			Short: "Roll back the most recent migration",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runMigrateCommand(cmd, "down", *dbURL, func(ctx context.Context, url string, wait platform.LockWait) error {
					err := withoutReleaseErrors(cmd.ErrOrStderr(), "one migration was rolled back", platform.MigrateDown(ctx, url, *migrationsPath, wait))
					if errors.Is(err, platform.ErrNoChange) {
						_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No migrations to roll back.")
						return nil
					}
					if err != nil {
						return err
					}
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Rolled back one migration.")
					return nil
				})
			},
		},
		&cobra.Command{
			Use:   "version",
			Short: "Show current migration version",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runMigrateCommand(cmd, "version", *dbURL, func(ctx context.Context, url string, wait platform.LockWait) error {
					st, err := platform.MigrateVersion(ctx, url, *migrationsPath, wait)
					if err = withoutReleaseErrors(cmd.ErrOrStderr(), "the record was read", err); err != nil {
						return err
					}
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), versionText(st))
					return nil
				})
			},
		},
		resetDirtyCmd,
	)

	return migrateCmd
}

// migrateRunner is one migrate leaf command's work against a resolved database URL.
type migrateRunner func(ctx context.Context, dbURL string, wait platform.LockWait) error

// runMigrateCommand resolves the database URL and runs one migrate command under its own signal context.
func runMigrateCommand(cmd *cobra.Command, command, flagURL string, run migrateRunner) error {
	cmd.SilenceUsage = true
	target, err := resolveDBURL(flagURL)
	if err != nil {
		return err
	}
	// Established here rather than on the root command, which would disable the
	// default SIGINT kill for every command in the binary.
	ctx, stop := migrateSignalContext(cmd.Context(), cmd.ErrOrStderr())
	defer stop()
	return migrateError(command, connectError(run(ctx, target.url, lockWaitNotice(cmd.ErrOrStderr())), target))
}
