package main

import (
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
			cmd.SilenceUsage = true
			target, err := resolveDBURL(*dbURL)
			if err != nil {
				return err
			}
			command, reset, next := "reset-dirty", platform.ResetDirty, upCommand
			if afterFailedDown {
				command, reset, next = afterDownCommandName, platform.ResetAfterFailedDown, downCommand
			}
			res, err := reset(cmd.Context(), target.url, *migrationsPath, nil)
			if err = withoutReleaseErrors(cmd.ErrOrStderr(), "the record was written", err); err != nil {
				return migrateError(command, connectError(err, target))
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), resetText(res, next))
			return nil
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
				cmd.SilenceUsage = true
				target, err := resolveDBURL(*dbURL)
				if err != nil {
					return err
				}
				err = withoutReleaseErrors(cmd.ErrOrStderr(), "migrate up finished", platform.MigrateUp(cmd.Context(), target.url, *migrationsPath, nil))
				return migrateError("up", connectError(err, target))
			},
		},
		&cobra.Command{
			Use:   "down",
			Short: "Roll back the most recent migration",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cmd.SilenceUsage = true
				target, err := resolveDBURL(*dbURL)
				if err != nil {
					return err
				}
				err = withoutReleaseErrors(cmd.ErrOrStderr(), "one migration was rolled back", platform.MigrateDown(cmd.Context(), target.url, *migrationsPath, nil))
				if errors.Is(err, platform.ErrNoChange) {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No migrations to roll back.")
					return nil
				}
				if err != nil {
					return migrateError("down", connectError(err, target))
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Rolled back one migration.")
				return nil
			},
		},
		&cobra.Command{
			Use:   "version",
			Short: "Show current migration version",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cmd.SilenceUsage = true
				target, err := resolveDBURL(*dbURL)
				if err != nil {
					return err
				}
				st, err := platform.MigrateVersion(cmd.Context(), target.url, *migrationsPath, nil)
				if err = withoutReleaseErrors(cmd.ErrOrStderr(), "the record was read", err); err != nil {
					return migrateError("version", connectError(err, target))
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), versionText(st))
				return nil
			},
		},
		resetDirtyCmd,
	)

	return migrateCmd
}
