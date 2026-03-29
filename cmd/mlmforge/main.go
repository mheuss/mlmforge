package main

import (
	"errors"
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

	dbURL := migrateCmd.PersistentFlags().String("db-url", "", "PostgreSQL connection URL (or set DATABASE_URL env var)")
	migrationsPath := migrateCmd.PersistentFlags().String("migrations", "./migrations", "Path to migration files")

	// resolveDBURL returns the database URL from the flag or DATABASE_URL env var.
	resolveDBURL := func() (string, error) {
		if *dbURL != "" {
			return *dbURL, nil
		}
		if env := os.Getenv("DATABASE_URL"); env != "" {
			return env, nil
		}
		return "", fmt.Errorf("--db-url flag or DATABASE_URL env var is required")
	}

	migrateCmd.AddCommand(
		&cobra.Command{
			Use:   "up",
			Short: "Apply all pending migrations",
			RunE: func(cmd *cobra.Command, args []string) error {
				url, err := resolveDBURL()
				if err != nil {
					return err
				}
				return platform.MigrateUp(url, *migrationsPath)
			},
		},
		&cobra.Command{
			Use:   "down",
			Short: "Roll back the most recent migration",
			RunE: func(cmd *cobra.Command, args []string) error {
				url, err := resolveDBURL()
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
			RunE: func(cmd *cobra.Command, args []string) error {
				url, err := resolveDBURL()
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

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
