package config

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// widthManifest mirrors the parts of width_manifest.json this scan reads.
type widthManifest struct {
	Fields    []manifestEntry `json:"fields"`
	AllowList []manifestEntry `json:"allow_list"`
}

type manifestEntry struct {
	GoStruct string `json:"go_struct"`
	GoField  string `json:"go_field"`
}

// loadManifestFieldSet returns the set of "GoStruct.GoField" keys the width
// manifest covers: every tightened field plus every intentional allow-list entry.
func loadManifestFieldSet(t *testing.T) map[string]bool {
	t.Helper()
	data, err := os.ReadFile("../../engine/testdata/config_contract/width_manifest.json")
	require.NoError(t, err)
	var m widthManifest
	require.NoError(t, json.Unmarshal(data, &m))
	covered := make(map[string]bool, len(m.Fields)+len(m.AllowList))
	for _, f := range m.Fields {
		covered[f.GoStruct+"."+f.GoField] = true
	}
	for _, a := range m.AllowList {
		covered[a.GoStruct+"."+a.GoField] = true
	}
	return covered
}

// tsStruct returns the struct type behind a TypeSpec, if it is one.
func tsStruct(ts *ast.TypeSpec) (*ast.StructType, bool) {
	if ts == nil {
		return nil, false
	}
	st, ok := ts.Type.(*ast.StructType)
	return st, ok
}

// typeContainsInt reports whether an AST field type is, or wraps, a signed
// integer (int, int8, int16, int32, int64) — the width-mismatch class this
// contract guards, including signed mirrors of narrow unsigned Rust types
// (e.g. int16 vs u16). It unwraps a pointer (*int), a slice/array element
// ([]int), a map value (map[K]int), and an inline anonymous struct's fields.
func typeContainsInt(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		switch e.Name {
		case "int", "int8", "int16", "int32", "int64":
			return true
		}
		return false
	case *ast.StarExpr:
		return typeContainsInt(e.X)
	case *ast.ArrayType:
		return typeContainsInt(e.Elt)
	case *ast.MapType:
		return typeContainsInt(e.Value)
	case *ast.StructType:
		for _, f := range e.Fields.List {
			if typeContainsInt(f.Type) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// TestConfigContract_NoUntypedIntFields fails if any struct field in the config
// package is a signed int (int/int8/int16/int32/int64, incl. *int / []int /
// map[K]int / inline struct) that is neither tightened (a width-manifest
// `fields` entry) nor explicitly allow-listed. It parses the
// WHOLE package — not just types.go — so a future int field added to any file
// (translate.go, rules.go, ...) still trips the guard. Uses os.ReadDir + a
// per-file parser.ParseFile (not the deprecated parser.ParseDir, which
// staticcheck SA1019 flags) and skips _test.go so the guard's own fixtures are
// not scanned. See HEU-513 MVF-2 (design 2.2 / Decision #3: package scan).
func TestConfigContract_NoUntypedIntFields(t *testing.T) {
	covered := loadManifestFieldSet(t)
	fset := token.NewFileSet()

	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	for _, e := range entries {
		fname := e.Name()
		if e.IsDir() || !strings.HasSuffix(fname, ".go") || strings.HasSuffix(fname, "_test.go") {
			continue // package sources only
		}
		f, err := parser.ParseFile(fset, fname, nil, 0)
		require.NoError(t, err)
		// Only top-level type declarations are config wire types. Iterating
		// f.Decls (rather than ast.Inspect over the whole tree) skips
		// function-local helper structs — e.g. sortStreamlineLevels's `numbered`
		// (translate.go) — which are transient computation types, not fields of
		// the cross-language config contract.
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := tsStruct(ts)
				if !ok {
					continue
				}
				for _, field := range st.Fields.List {
					if !typeContainsInt(field.Type) {
						continue
					}
					for _, fieldName := range field.Names {
						key := ts.Name.Name + "." + fieldName.Name
						assert.Contains(t, covered, key,
							"%s is an un-tightened signed int — tighten it (add to the width manifest) or allow-list it", key)
					}
				}
			}
		}
	}
}
