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

	return migrateCmd
}
