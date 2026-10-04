package platform

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSourceInfo_PlacesVersionsInTheMigrationsDirectory(t *testing.T) {
	dir := FindMigrationsDir(t)
	src, err := source.Open("file://" + dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })
	mg := &migration{source: src, path: dir}

	assert.Equal(t, SourceInfo{Path: dir, InSource: true, Previous: 5, HasPrevious: true, Next: 7, HasNext: true}, mg.sourceInfo(6))
	assert.Equal(t, SourceInfo{Path: dir, InSource: true, Next: 2, HasNext: true}, mg.sourceInfo(1))
	assert.Equal(t, SourceInfo{Path: dir}, mg.sourceInfo(99))
	assert.Equal(t, SourceInfo{Path: dir, Next: 1, HasNext: true}, mg.sourceInfo(-1))
	assert.Equal(t, SourceInfo{Path: dir}, mg.sourceInfo(-2))
}

// sparseMigrations holds versions 2 and 7, each with an up and a down file.
var sparseMigrations = map[string]string{
	"2_a.up.sql": "SELECT 1;", "2_a.down.sql": "SELECT 1;",
	"7_b.up.sql": "SELECT 1;", "7_b.down.sql": "SELECT 1;",
}

// writeMigrations writes each named file into a new directory and returns its path.
func writeMigrations(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	return dir
}

// sourceMigration returns a migration holding only dir's source driver.
func sourceMigration(t *testing.T, dir string) *migration {
	t.Helper()
	src, err := source.Open("file://" + dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })
	return &migration{source: src, path: dir}
}

func TestSourceInfo_TakesTheNextMigrationFromASparseDirectory(t *testing.T) {
	dir := writeMigrations(t, sparseMigrations)
	mg := sourceMigration(t, dir)

	assert.Equal(t, SourceInfo{Path: dir, InSource: true, Next: 7, HasNext: true}, mg.sourceInfo(2))
	assert.Equal(t, SourceInfo{Path: dir, InSource: true, Previous: 2, HasPrevious: true}, mg.sourceInfo(7))
	assert.Equal(t, SourceInfo{Path: dir, Next: 2, HasNext: true}, mg.sourceInfo(-1))
}

func TestSourceInfo_AnEmptyDirectoryHasNothingAfterMinusOne(t *testing.T) {
	dir := t.TempDir()

	assert.Equal(t, SourceInfo{Path: dir}, sourceMigration(t, dir).sourceInfo(-1))
}
