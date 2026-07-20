package config

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// widthManifest mirrors the parts of width_manifest.json this scan reads.
type widthManifest struct {
	Fields    []manifestEntry `json:"fields"`
	AllowList []manifestEntry `json:"allow_list"`
}

type manifestEntry struct {
	GoStruct  string `json:"go_struct"`
	GoField   string `json:"go_field"`
	GoType    string `json:"go_type"`
	GoFixture string `json:"go_fixture"`
	GoPointer string `json:"go_pointer"`
	// OverMax is decoded as int64, not any. encoding/json decodes a bare number
	// into float64, and re-encoding 4294967296 to YAML then emits
	// 4.294967296e+09 — which yaml.v3 rejects as a *type* error rather than a
	// *range* error. The boundary assertion below would still see an error and
	// pass, without ever exercising the field's width.
	OverMax int64 `json:"over_max"`
}

// widthManifestPath locates the manifest relative to the config package.
const widthManifestPath = "../../engine/testdata/config_contract/width_manifest.json"

// loadManifest reads and decodes the width contract manifest.
func loadManifest(t *testing.T) widthManifest {
	t.Helper()
	data, err := os.ReadFile(widthManifestPath)
	require.NoError(t, err)
	var m widthManifest
	require.NoError(t, json.Unmarshal(data, &m))
	require.NotEmpty(t, m.Fields, "width manifest declares no fields")
	return m
}

// loadManifestFieldSet returns the set of "GoStruct.GoField" keys the width
// manifest covers: every tightened field plus every intentional allow-list entry.
func loadManifestFieldSet(t *testing.T) map[string]bool {
	t.Helper()
	m := loadManifest(t)
	covered := make(map[string]bool, len(m.Fields)+len(m.AllowList))
	for _, f := range m.Fields {
		covered[f.GoStruct+"."+f.GoField] = true
	}
	for _, a := range m.AllowList {
		covered[a.GoStruct+"."+a.GoField] = true
	}
	return covered
}

// TestConfigContract_FieldsMatchAndRejectOverMax asserts, for every tightened
// field in the width manifest, that (a) the Go field's declared type still
// matches the manifest's go_type, and (b) an over-max value at the field's
// go_pointer is rejected by the real two-pass decode.
//
// The two-pass decode matters: commission fields sit behind CommissionRaw and
// only decode into their typed struct during resolveCommissions, so
// unmarshalling the plan alone would never exercise them. The decode stops
// before business validation deliberately — an error then proves the field's
// TYPE rejected the value, not that some business rule happened to cap it.
func TestConfigContract_FieldsMatchAndRejectOverMax(t *testing.T) {
	structs := configStructRegistry()
	for _, f := range loadManifest(t).Fields {
		t.Run(f.GoStruct+"."+f.GoField, func(t *testing.T) {
			typ, ok := structs[f.GoStruct]
			require.True(t, ok, "%s is not in configStructRegistry", f.GoStruct)
			sf, ok := typ.FieldByName(f.GoField)
			require.True(t, ok, "%s.%s not found", f.GoStruct, f.GoField)
			assert.Equal(t, f.GoType, sf.Type.String(), "%s.%s width", f.GoStruct, f.GoField)

			// Boundary: mutate one field of a real authoring plan to over_max,
			// then run the actual two-pass decode and assert it rejects.
			raw := loadAuthoringFixture(t, f.GoFixture)

			// Pristine-first. Without this, a fixture that breaks for any
			// unrelated reason makes the rejection below true for the wrong
			// reason, and the boundary half silently stops testing anything
			// while still reporting green.
			require.NoError(t, loadAndResolve(raw),
				"fixture %s must load cleanly before mutation", f.GoFixture)

			mutated := setJSONPointer(t, raw, f.GoPointer, f.OverMax)
			err := loadAndResolve(mutated)
			require.Error(t, err,
				"%s.%s must reject %d through the loader", f.GoStruct, f.GoField, f.OverMax)

			// Pin the rejection to the value itself, which makes the OverMax
			// int64 decision executable rather than merely documented: decode
			// it as any and the uint32 entries re-encode to 4.294967296e+09,
			// which yaml.v3 rejects as a type error naming that float — not
			// this integer — so this assertion would catch the regression.
			assert.Contains(t, err.Error(), strconv.FormatInt(f.OverMax, 10),
				"%s.%s rejection must name the over-max value, not fail for an unrelated reason",
				f.GoStruct, f.GoField)
		})
	}
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

// configStructRegistry maps each config struct named by the width manifest to
// its reflect.Type.
//
// It covers the manifest's structs rather than every struct in the package:
// nothing verifies a hand-maintained full list — Task 12's completeness scan
// checks that int fields appear in the manifest, not that this registry is
// exhaustive — so a full list would rot silently. A manifest entry naming a
// struct that is missing here fails loudly in
// TestConfigContract_FieldsMatchAndRejectOverMax instead.
func configStructRegistry() map[string]reflect.Type {
	structs := []any{
		UnilevelCommission{},
		MatrixCommission{},
		StairstepCommission{},
		GenerationCommission{},
		StreamlineCommission{},
		MatrixStructureParams{},
		ActiveLegTier{},
		MatchingBonusConfig{},
		LeadershipDevelopmentBonusConfig{},
		FastStartBonusConfig{},
		LifestyleTier{},
		PassUpConfig{},
		PeriodConfig{},
		BoardCyclingConfig{},
		StreamConfig{},
		GracePeriod{},
		HoldingTankConfig{},
	}
	registry := make(map[string]reflect.Type, len(structs))
	for _, s := range structs {
		typ := reflect.TypeOf(s)
		registry[typ.Name()] = typ
	}
	return registry
}

// loadAuthoringFixture reads the authoring-shape YAML plan named by a manifest
// entry's go_fixture (testdata/valid/<name>.yaml).
func loadAuthoringFixture(t *testing.T, name string) []byte {
	t.Helper()
	return readFixture(t, filepath.Join("valid", name+".yaml"))
}

// loadAndResolve runs the same two passes the production loader runs — YAML
// unmarshal into CompensationPlan, then resolveCommissions — and stops before
// business validation, returning the first error. Stopping early is deliberate:
// an error here proves the field's TYPE rejected the value rather than a
// business rule happening to cap it.
func loadAndResolve(yamlBytes []byte) error {
	var plan CompensationPlan
	if err := yaml.Unmarshal(yamlBytes, &plan); err != nil {
		return err
	}
	return resolveCommissions(&plan)
}

// setJSONPointer decodes YAML, sets value at an RFC 6901 JSON pointer, and
// re-encodes. Every token must already resolve — a stale manifest pointer fails
// the test loudly rather than leaving the document unmutated, which would let
// the boundary assertion pass without testing anything.
//
// Mapping tokens require string-tagged YAML keys, which yaml.v3 decodes into
// map[string]any. A numeric-looking key must therefore be quoted in the fixture
// ("1:" not 1:), or yaml.v3 yields map[any]any and the walk fails loudly rather
// than resolving. Every rate map in the current fixtures quotes its keys.
func setJSONPointer(t *testing.T, yamlBytes []byte, pointer string, value any) []byte {
	t.Helper()
	require.True(t, strings.HasPrefix(pointer, "/"), "pointer %q must start with /", pointer)

	var doc any
	require.NoError(t, yaml.Unmarshal(yamlBytes, &doc))

	tokens := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	for i, tok := range tokens {
		// RFC 6901 unescaping: ~1 before ~0, so an encoded ~1 stays literal.
		tokens[i] = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
	}

	node := doc
	for _, tok := range tokens[:len(tokens)-1] {
		node = pointerChild(t, node, tok, pointer)
	}
	setPointerLeaf(t, node, tokens[len(tokens)-1], pointer, value)

	out, err := yaml.Marshal(doc)
	require.NoError(t, err)
	return out
}

// pointerChild resolves one intermediate JSON-pointer token against a decoded
// YAML node.
func pointerChild(t *testing.T, node any, token, pointer string) any {
	t.Helper()
	switch n := node.(type) {
	case map[string]any:
		child, ok := n[token]
		require.True(t, ok, "pointer %s: key %q not found", pointer, token)
		return child
	case []any:
		return n[pointerIndex(t, n, token, pointer)]
	default:
		t.Fatalf("pointer %s: cannot descend through %T at %q", pointer, node, token)
		return nil
	}
}

// setPointerLeaf writes value at the final pointer token. The target must
// already exist, so a stale pointer cannot silently create a new key.
func setPointerLeaf(t *testing.T, node any, token, pointer string, value any) {
	t.Helper()
	switch n := node.(type) {
	case map[string]any:
		_, ok := n[token]
		require.True(t, ok, "pointer %s: key %q not found", pointer, token)
		n[token] = value
	case []any:
		n[pointerIndex(t, n, token, pointer)] = value
	default:
		t.Fatalf("pointer %s: cannot set %q on %T", pointer, token, node)
	}
}

// pointerIndex parses an array-index token and bounds-checks it.
func pointerIndex(t *testing.T, node []any, token, pointer string) int {
	t.Helper()
	idx, err := strconv.Atoi(token)
	require.NoError(t, err, "pointer %s: %q is not an array index", pointer, token)
	require.True(t, idx >= 0 && idx < len(node),
		"pointer %s: index %d out of range (len %d)", pointer, idx, len(node))
	return idx
}
