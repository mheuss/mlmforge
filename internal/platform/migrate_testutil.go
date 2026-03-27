package platform

import (
	"os"
	"path/filepath"
	"testing"
)

// RunMigrationsForTest applies all migrations and returns a cleanup function
// that rolls them back. Exported for use by other packages' integration tests.
func RunMigrationsForTest(t *testing.T, dbURL string) func() {
	t.Helper()

	migrationsPath := FindMigrationsDir(t)

	err := MigrateUp(dbURL, migrationsPath)
	if err != nil {
		t.Fatalf("MigrateUp failed: %v", err)
	}

	return func() {
		for {
			err := MigrateDown(dbURL, migrationsPath)
			if err != nil {
				break
			}
		}
	}
}

// FindMigrationsDir walks up from the working directory to find migrations/.
func FindMigrationsDir(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}

	for {
		candidate := filepath.Join(dir, "migrations")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("migrations/ directory not found")
		}
		dir = parent
	}
}
