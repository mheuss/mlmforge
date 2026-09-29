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

	migrateCmd.AddCommand(
		&cobra.Command{
			Use:   "up",
			Short: "Apply all pending migrations",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cmd.SilenceUsage = true
				url, err := resolveDBURL(*dbURL)
				if err != nil {
					return err
				}
				err = withoutReleaseErrors(cmd.ErrOrStderr(), "migrate up finished", platform.MigrateUp(url, *migrationsPath))
				return migrateError("up", err)
			},
		},
		&cobra.Command{
			Use:   "down",
			Short: "Roll back the most recent migration",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cmd.SilenceUsage = true
				url, err := resolveDBURL(*dbURL)
				if err != nil {
					return err
				}
				err = withoutReleaseErrors(cmd.ErrOrStderr(), "one migration was rolled back", platform.MigrateDown(url, *migrationsPath))
				if errors.Is(err, platform.ErrNoChange) {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No migrations to roll back.")
					return nil
				}
				if err != nil {
					return migrateError("down", err)
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
				url, err := resolveDBURL(*dbURL)
				if err != nil {
					return err
				}
				st, err := platform.MigrateVersion(url, *migrationsPath)
				if err = withoutReleaseErrors(cmd.ErrOrStderr(), "the record was read", err); err != nil {
					return err
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), versionText(st))
				return nil
			},
		},
		&cobra.Command{
			Use:   "reset-dirty",
			Short: "Move a dirty migration record back to the previous migration, clean",
			Long: "Move a dirty migration record back to the previous migration, clean.\n\n" +
				"Run it only after a failed `mlmforge migrate up`. Do not run it after a failed `mlmforge migrate down`.",
			Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cmd.SilenceUsage = true
				url, err := resolveDBURL(*dbURL)
				if err != nil {
					return err
				}
				res, err := platform.ResetDirty(url, *migrationsPath)
				if err = withoutReleaseErrors(cmd.ErrOrStderr(), "the record was written", err); err != nil {
					return migrateError("reset-dirty", err)
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), resetText(res))
				return nil
			},
		},
	)

	return migrateCmd
}
