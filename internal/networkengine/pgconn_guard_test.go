package networkengine

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const pgconnImportPath = "github.com/jackc/pgx/v5/pgconn"

// findPgconnImports returns each non-test Go file in dir that imports pgconn
// and is not a Postgres store implementation.
func findPgconnImports(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") ||
			strings.HasSuffix(name, "_postgres.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			return nil, err
		}
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return nil, err
			}
			if path == pgconnImportPath {
				found = append(found, name)
				break
			}
		}
	}
	return found, nil
}

func TestFindPgconnImports_NamesEachImporterOutsideThePostgresStores(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	write("writer.go", "package networkengine\n\nimport \"github.com/jackc/pgx/v5/pgconn\"\n\nvar _ pgconn.PgError\n")
	write("aliased.go", "package networkengine\n\nimport (\n\t\"errors\"\n\tpg \"github.com/jackc/pgx/v5/pgconn\"\n)\n\nvar _ = errors.New\nvar _ pg.PgError\n")
	write("dot.go", "package networkengine\n\nimport . \"github.com/jackc/pgx/v5/pgconn\"\n\nvar _ PgError\n")
	write("blank.go", "package networkengine\n\nimport _ \"github.com/jackc/pgx/v5/pgconn\"\n")
	write("plain.go", "package networkengine\n\nimport \"github.com/jackc/pgx/v5\"\n\nvar _ pgx.Rows\n")
	write("tree_store_postgres.go", "package networkengine\n\nimport \"github.com/jackc/pgx/v5/pgconn\"\n\nvar _ pgconn.PgError\n")
	write("writer_test.go", "package networkengine\n\nimport \"github.com/jackc/pgx/v5/pgconn\"\n\nvar _ pgconn.PgError\n")

	found, err := findPgconnImports(dir)

	require.NoError(t, err)
	assert.Equal(t, []string{"aliased.go", "blank.go", "dot.go", "writer.go"}, found)
}

func TestPgconnStaysInThePostgresStores(t *testing.T) {
	found, err := findPgconnImports(filepath.Join(moduleRoot(t), "internal", "networkengine"))

	require.NoError(t, err)
	assert.Empty(t, found, "non-test files in internal/networkengine that import pgconn and are not *_postgres.go")
}
