package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A bare END is left out: CASE ... END is ordinary SQL.
var partialApplyForm = regexp.MustCompile(`(?i)\b(BEGIN|COMMIT|ROLLBACK|ABORT|CONCURRENTLY|CALL|START\s+TRANSACTION|END\s+(?:TRANSACTION|WORK)|PREPARE\s+TRANSACTION)\b`)

// partialApplyForms returns each transaction-control, procedure-call or concurrent form named in sql.
func partialApplyForms(sql string) []string {
	return partialApplyForm.FindAllString(sql, -1)
}

func TestPartialApplyForms_FindsEachNamedFormAsAWholeWord(t *testing.T) {
	assert.Equal(t, []string{"CONCURRENTLY"}, partialApplyForms("CREATE INDEX CONCURRENTLY idx ON t (c);"))
	assert.Equal(t, []string{"begin", "commit"}, partialApplyForms("begin;\nALTER TABLE t ADD c int;\ncommit;"))
	assert.Equal(t, []string{"ROLLBACK", "Abort"}, partialApplyForms("ROLLBACK;\nAbort;"))
	assert.Equal(t, []string{"START TRANSACTION"}, partialApplyForms("START TRANSACTION;"))
	assert.Equal(t, []string{"CALL"}, partialApplyForms("CALL refresh_totals();"))
	assert.Equal(t, []string{"END TRANSACTION", "end work"}, partialApplyForms("END TRANSACTION;\nend work;"))
	assert.Equal(t, []string{"PREPARE TRANSACTION"}, partialApplyForms("PREPARE TRANSACTION 'x';"))
	assert.Empty(t, partialApplyForms("SELECT CASE WHEN a THEN 1 END FROM t;\nSELECT recall, callback FROM t;"))
	assert.Empty(t, partialApplyForms("-- the beginning of the committed rollbacks\nSELECT 1;"))
}

func TestMigrationFiles_NoFileNamesAPartialApplyForm(t *testing.T) {
	dir := FindMigrationsDir(t)
	var files []string
	for _, pattern := range []string{"*.up.sql", "*.down.sql"} {
		matched, err := filepath.Glob(filepath.Join(dir, pattern))
		require.NoError(t, err)
		require.NotEmpty(t, matched, "found no %s files under %s", pattern, dir)
		files = append(files, matched...)
	}

	for _, file := range files {
		body, err := os.ReadFile(file)
		require.NoError(t, err)
		for _, form := range partialApplyForms(string(body)) {
			t.Errorf("%s contains %q", filepath.Base(file), form)
		}
	}
}

func TestMigrationFiles_EveryVersionHasAnUpAndADownFile(t *testing.T) {
	for _, problem := range unpairedVersions(t, FindMigrationsDir(t)) {
		t.Error(problem)
	}
}

// unpairedVersions returns a message for each version in dir that has an up file and no down file, or the reverse.
func unpairedVersions(t *testing.T, dir string) []string {
	t.Helper()
	up, down := migrationVersions(t, dir, "up"), migrationVersions(t, dir, "down")
	var problems []string
	for version := range up {
		if !down[version] {
			problems = append(problems, fmt.Sprintf("migration %s has an up file and no down file", version))
		}
	}
	for version := range down {
		if !up[version] {
			problems = append(problems, fmt.Sprintf("migration %s has a down file and no up file", version))
		}
	}
	slices.Sort(problems)
	return problems
}

// migrationVersions returns the version prefix of every migration file in dir for one direction.
func migrationVersions(t *testing.T, dir, direction string) map[string]bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*."+direction+".sql"))
	require.NoError(t, err)
	versions := map[string]bool{}
	for _, file := range files {
		version, _, _ := strings.Cut(filepath.Base(file), "_")
		versions[version] = true
	}
	return versions
}

func TestUnpairedVersions_NamesEachVersionMissingADirection(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"1_a.up.sql", "1_a.down.sql", "2_b.up.sql", "3_c.down.sql"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("SELECT 1;"), 0o600))
	}

	assert.Equal(t, []string{
		"migration 2 has an up file and no down file",
		"migration 3 has a down file and no up file",
	}, unpairedVersions(t, dir))
}

// migrationFileName matches a version, a name, a direction and any extension.
var migrationFileName = regexp.MustCompile(`^([0-9]+)_(.*)\.(down|up)\.(.*)$`)

// nonSQLMigrationFiles returns the name of each migration file in dir that does not end in .sql.
func nonSQLMigrationFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var files []string
	for _, entry := range entries {
		name := entry.Name()
		if migrationFileName.MatchString(name) && !strings.HasSuffix(name, ".sql") {
			files = append(files, name)
		}
	}
	return files
}

func TestMigrationFiles_EveryMigrationFileEndsInSQL(t *testing.T) {
	for _, name := range nonSQLMigrationFiles(t, FindMigrationsDir(t)) {
		t.Errorf("%s is a migration file that does not end in .sql", name)
	}
}

func TestNonSQLMigrationFiles_NamesEachMigrationFileNotEndingInSQL(t *testing.T) {
	assert.Equal(t, []string{"000002_b.down.txt", "000002_b.up.txt"},
		nonSQLMigrationFiles(t, filepath.Join("testdata", "non-sql-migrations")))
}
