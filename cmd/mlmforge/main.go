package main

import (
	"fmt"
	"os"

	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "mlmforge",
		Short: "MLMForge compensation engine",
	}

	migrateCmd := &cobra.Command{
		Use:   "migrate",
		Short: "Database migration commands",
	}

	dbURL := migrateCmd.PersistentFlags().String("db-url", "", "PostgreSQL connection URL (required)")
	migrationsPath := migrateCmd.PersistentFlags().String("migrations", "./migrations", "Path to migration files")

	migrateCmd.AddCommand(
		&cobra.Command{
			Use:   "up",
			Short: "Apply all pending migrations",
			RunE: func(cmd *cobra.Command, args []string) error {
				if *dbURL == "" {
					return fmt.Errorf("--db-url is required")
				}
				return platform.MigrateUp(*dbURL, *migrationsPath)
			},
		},
		&cobra.Command{
			Use:   "down",
			Short: "Roll back the most recent migration",
			RunE: func(cmd *cobra.Command, args []string) error {
				if *dbURL == "" {
					return fmt.Errorf("--db-url is required")
				}
				return platform.MigrateDown(*dbURL, *migrationsPath)
			},
		},
		&cobra.Command{
			Use:   "version",
			Short: "Show current migration version",
			RunE: func(cmd *cobra.Command, args []string) error {
				if *dbURL == "" {
					return fmt.Errorf("--db-url is required")
				}
				v, dirty, err := platform.MigrateVersion(*dbURL, *migrationsPath)
				if err != nil {
					return err
				}
				fmt.Printf("Version: %d, Dirty: %v\n", v, dirty)
				return nil
			},
		},
	)

	root.AddCommand(migrateCmd)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
