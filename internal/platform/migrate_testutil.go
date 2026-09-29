package platform

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// RunMigrationsForTest applies all migrations and returns a cleanup function
// that rolls them back. Exported for use by other packages' integration tests.
func RunMigrationsForTest(t *testing.T, dbURL string) func() {
	t.Helper()

	migrationsPath := FindMigrationsDir(t)

	releases, err := SplitRelease(MigrateUp(dbURL, migrationsPath))
	for _, release := range releases {
		t.Logf("MigrateUp: %v", release)
	}
	if err != nil {
		t.Fatalf("MigrateUp failed: %v", err)
	}

	return func() {
		for {
			releases, err := SplitRelease(MigrateDown(dbURL, migrationsPath))
			for _, release := range releases {
				t.Logf("MigrateDown: %v", release)
			}
			if err == nil {
				continue
			}
			if errors.Is(err, ErrNoChange) {
				return
			}
			t.Errorf("MigrateDown failed during cleanup: %v", err)
			return
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
