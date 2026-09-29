package platform

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// END is left out: CASE ... END is ordinary SQL.
var partialApplyForm = regexp.MustCompile(`(?i)\b(BEGIN|COMMIT|ROLLBACK|ABORT|CONCURRENTLY|START\s+TRANSACTION)\b`)

// partialApplyForms returns each transaction-control or concurrent form named in sql.
func partialApplyForms(sql string) []string {
	return partialApplyForm.FindAllString(sql, -1)
}

func TestPartialApplyForms_FindsEachNamedFormAsAWholeWord(t *testing.T) {
	assert.Equal(t, []string{"CONCURRENTLY"}, partialApplyForms("CREATE INDEX CONCURRENTLY idx ON t (c);"))
	assert.Equal(t, []string{"begin", "commit"}, partialApplyForms("begin;\nALTER TABLE t ADD c int;\ncommit;"))
	assert.Equal(t, []string{"ROLLBACK", "Abort"}, partialApplyForms("ROLLBACK;\nAbort;"))
	assert.Equal(t, []string{"START TRANSACTION"}, partialApplyForms("START TRANSACTION;"))
	assert.Empty(t, partialApplyForms("-- the beginning of the committed rollbacks\nSELECT 1;"))
}

func TestMigrationFiles_NoUpFileNamesAPartialApplyForm(t *testing.T) {
	dir := FindMigrationsDir(t)
	files, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "found no up migrations under %s", dir)

	for _, file := range files {
		body, err := os.ReadFile(file)
		require.NoError(t, err)
		for _, form := range partialApplyForms(string(body)) {
			t.Errorf("%s contains %q", filepath.Base(file), form)
		}
	}
}
