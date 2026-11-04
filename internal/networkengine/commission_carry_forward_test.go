package networkengine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mlmforge/mlmforge/internal/dec"
)

// goldenCarryForward reads the stored-envelope fixture, trimmed so an editor's
// trailing newline does not fail the byte comparison.
func goldenCarryForward(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "carry_forward", "envelope.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// carryForwardFixtureInput is the builder input the golden envelope encodes.
func carryForwardFixtureInput() map[string]map[string]LegVolumesDTO {
	return map[string]map[string]LegVolumesDTO{
		"primary-binary": {
			"11111111-1111-1111-1111-111111111111": {Left: dec.MustParse("120"), Right: dec.MustParse("80.5")},
			"22222222-2222-2222-2222-222222222222": {Left: dec.MustParse("0"), Right: dec.MustParse("15.25")},
		},
		"idle-binary": nil,
	}
}

func TestCarryForwardFromMatchesTheGoldenEnvelope(t *testing.T) {
	got, err := CarryForwardFrom(carryForwardFixtureInput())
	if err != nil {
		t.Fatalf("CarryForwardFrom: %v", err)
	}
	if want := goldenCarryForward(t); string(got) != want {
		t.Fatalf("envelope =\n%s\nwant\n%s", got, want)
	}
}

func TestCarryForwardFromWithNoStructuresReturnsNil(t *testing.T) {
	for _, in := range []map[string]map[string]LegVolumesDTO{nil, {}} {
		got, err := CarryForwardFrom(in)
		if err != nil {
			t.Fatalf("CarryForwardFrom(%v): %v", in, err)
		}
		if got != nil {
			t.Fatalf("CarryForwardFrom(%v) = %s, want nil", in, got)
		}
	}
}

func TestCarryForwardFromStoresANilCarryAsAnEmptyObject(t *testing.T) {
	got, err := CarryForwardFrom(map[string]map[string]LegVolumesDTO{"idle": nil})
	if err != nil {
		t.Fatalf("CarryForwardFrom: %v", err)
	}
	const want = `{"v":1,"structures":{"idle":{"kind":"binary_legs","carry":{}}}}`
	if string(got) != want {
		t.Fatalf("envelope = %s, want %s", got, want)
	}
}

// Reads the fixture file rather than the structs, so a regenerated golden
// cannot hide a changed version or kind.
func TestCarryForwardFixtureCarriesVersionAndKind(t *testing.T) {
	var parsed struct {
		V          int `json:"v"`
		Structures map[string]struct {
			Kind string `json:"kind"`
		} `json:"structures"`
	}
	if err := json.Unmarshal([]byte(goldenCarryForward(t)), &parsed); err != nil {
		t.Fatalf("fixture is not readable JSON: %v", err)
	}
	// Literals, not the constants: a constant changed alongside a regenerated
	// golden would otherwise move both sides together.
	if parsed.V != 1 {
		t.Fatalf("v = %d, want 1", parsed.V)
	}
	if len(parsed.Structures) == 0 {
		t.Fatal("fixture holds no structures")
	}
	for name, entry := range parsed.Structures {
		if entry.Kind != "binary_legs" {
			t.Errorf("structure %q kind = %q, want %q", name, entry.Kind, "binary_legs")
		}
	}
}

func TestLegVolumesCarryMirrorsTheWireType(t *testing.T) {
	got := jsonTags(t, legVolumesCarry{})
	want := jsonTags(t, LegVolumesDTO{})
	if !slices.Equal(got, want) {
		t.Fatalf("legVolumesCarry json tags %v, want %v", got, want)
	}
}

func TestCarryForwardFromRefusesABadStructureName(t *testing.T) {
	for _, name := range []string{"", "bad\xff", "nul\x00byte", "bad�"} {
		if _, err := CarryForwardFrom(map[string]map[string]LegVolumesDTO{name: nil}); err == nil {
			t.Errorf("structure %q: expected an error", name)
		}
	}
}

func TestCarryForwardFromRefusesANonCanonicalKey(t *testing.T) {
	for _, key := range []string{
		"AAAAAAAA-1111-1111-1111-111111111111",
		"{11111111-1111-1111-1111-111111111111}",
		"urn:uuid:11111111-1111-1111-1111-111111111111",
		"11111111111111111111111111111111",
		"not-a-uuid",
		"00000000-0000-0000-0000-000000000000",
	} {
		in := map[string]map[string]LegVolumesDTO{"primary": {key: {}}}
		if _, err := CarryForwardFrom(in); err == nil {
			t.Errorf("key %q: expected an error", key)
		}
	}
}

func TestCarryForwardFromReportsTheFirstBadKeyInSortedOrder(t *testing.T) {
	in := map[string]map[string]LegVolumesDTO{
		"b-structure": {"zzz": {}, "mmm": {}},
		"a-structure": {"yyy": {}, "bbb": {}},
	}
	for range 20 {
		_, err := CarryForwardFrom(in)
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), `"a-structure"`) || !strings.Contains(err.Error(), `"bbb"`) {
			t.Fatalf("err = %v, want it to name structure a-structure and key bbb", err)
		}
	}
}

// assertSameCarryForward compares carry-forward content, treating nil and
// empty inner maps as equal and amounts as exact decimals.
func assertSameCarryForward(t *testing.T, got, want map[string]map[string]LegVolumesDTO) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d structures, want %d: %v", len(got), len(want), got)
	}
	for name, wantLegs := range want {
		gotLegs, ok := got[name]
		if !ok {
			t.Fatalf("structure %q missing", name)
		}
		if len(gotLegs) != len(wantLegs) {
			t.Fatalf("structure %q holds %d keys, want %d", name, len(gotLegs), len(wantLegs))
		}
		for key, w := range wantLegs {
			g, ok := gotLegs[key]
			if !ok {
				t.Fatalf("structure %q key %s missing", name, key)
			}
			if !g.Left.Equal(w.Left.Decimal) || !g.Right.Equal(w.Right.Decimal) {
				t.Fatalf("structure %q key %s = left %s right %s, want left %s right %s",
					name, key, g.Left, g.Right, w.Left, w.Right)
			}
		}
	}
}

func TestParseCarryForwardReadsTheGoldenEnvelope(t *testing.T) {
	got, err := ParseCarryForward(json.RawMessage(goldenCarryForward(t)))
	if err != nil {
		t.Fatalf("ParseCarryForward: %v", err)
	}
	assertSameCarryForward(t, got, carryForwardFixtureInput())
	if got["idle-binary"] == nil {
		t.Fatal("idle-binary parsed to a nil map, want a non-nil empty map")
	}
}

func TestParseCarryForwardWithNoInputReturnsNil(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, {}} {
		got, err := ParseCarryForward(raw)
		if err != nil {
			t.Fatalf("ParseCarryForward(%q): %v", raw, err)
		}
		if got != nil {
			t.Fatalf("ParseCarryForward(%q) = %v, want nil", raw, got)
		}
	}
}

func TestParseCarryForwardRefusesMalformedEnvelopes(t *testing.T) {
	const key = "11111111-1111-1111-1111-111111111111"
	const leg = `{"left":1,"right":2}`
	entry := func(body string) string {
		return `{"v":1,"structures":{"primary":` + body + `}}`
	}
	withLeg := func(legJSON string) string {
		return entry(`{"kind":"binary_legs","carry":{"` + key + `":` + legJSON + `}}`)
	}
	cases := []struct{ name, raw string }{
		{"JSON null", `null`},
		{"JSON null with whitespace", ` null `},
		{"an array", `[]`},
		{"a number", `1`},
		{"trailing data", `{"v":1,"structures":{}} {}`},
		{"v missing", `{"structures":{}}`},
		{"v null", `{"v":null,"structures":{}}`},
		{"v 0", `{"v":0,"structures":{}}`},
		{"v 2", `{"v":2,"structures":{}}`},
		{"v a string", `{"v":"1","structures":{}}`},
		{"structures missing", `{"v":1}`},
		{"structures null", `{"v":1,"structures":null}`},
		{"unknown envelope field", `{"v":1,"structures":{},"extra":1}`},
		{"entry null", entry(`null`)},
		{"kind missing", entry(`{"carry":{}}`)},
		{"kind null", entry(`{"kind":null,"carry":{}}`)},
		{"kind board_cycles", entry(`{"kind":"board_cycles","counts":{}}`)},
		{"kind unknown", entry(`{"kind":"matrix_legs","carry":{}}`)},
		{"carry missing", entry(`{"kind":"binary_legs"}`)},
		{"carry null", entry(`{"kind":"binary_legs","carry":null}`)},
		{"unknown entry field", entry(`{"kind":"binary_legs","carry":{},"extra":1}`)},
		{"leg null", withLeg(`null`)},
		{"leg empty", withLeg(`{}`)},
		{"left missing", withLeg(`{"right":2}`)},
		{"right missing", withLeg(`{"left":1}`)},
		{"left null", withLeg(`{"left":null,"right":2}`)},
		{"right null", withLeg(`{"left":1,"right":null}`)},
		{"left a string", withLeg(`{"left":"1","right":2}`)},
		{"unknown leg field", withLeg(`{"left":1,"right":2,"extra":1}`)},
		{"empty structure name", `{"v":1,"structures":{"":{"kind":"binary_legs","carry":{}}}}`},
		{"NUL in structure name", `{"v":1,"structures":{"a\u0000b":{"kind":"binary_legs","carry":{}}}}`},
		{"invalid UTF-8 in structure name", "{\"v\":1,\"structures\":{\"bad\xff\":{\"kind\":\"binary_legs\",\"carry\":{}}}}"},
		{"non-canonical key", entry(`{"kind":"binary_legs","carry":{"AAAAAAAA-1111-1111-1111-111111111111":` + leg + `}}`)},
		{"non-UUID key", entry(`{"kind":"binary_legs","carry":{"nope":` + leg + `}}`)},
		{"lone surrogate in structure name", `{"v":1,"structures":{"a\ud800":{"kind":"binary_legs","carry":{}}}}`},
		{"lone surrogate in key", entry(`{"kind":"binary_legs","carry":{"\ud800":` + leg + `}}`)},
		{"nil UUID key", entry(`{"kind":"binary_legs","carry":{"00000000-0000-0000-0000-000000000000":` + leg + `}}`)},
		{"duplicate envelope field", `{"v":1,"structures":{"primary":{"kind":"binary_legs","carry":{}}},"structures":{}}`},
		{"duplicate structure name", `{"v":1,"structures":{"a":{"kind":"binary_legs","carry":{}},"a":{"kind":"binary_legs","carry":{}}}}`},
		{"duplicate entry field", entry(`{"kind":"binary_legs","carry":{},"carry":{}}`)},
		{"duplicate carry key", entry(`{"kind":"binary_legs","carry":{"` + key + `":` + leg + `,"` + key + `":` + leg + `}}`)},
		{"duplicate leg field", withLeg(`{"left":9,"left":1,"right":1}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCarryForward(json.RawMessage(tc.raw))
			if err == nil {
				t.Fatalf("ParseCarryForward(%s) = %v, want an error", tc.raw, got)
			}
		})
	}
}

func TestParseCarryForwardReportsTheFirstBadKeyInSortedOrder(t *testing.T) {
	raw := json.RawMessage(`{"v":1,"structures":{` +
		`"b":{"kind":"binary_legs","carry":{"zzz":{"left":1,"right":1}}},` +
		`"a":{"kind":"binary_legs","carry":{"yyy":{"left":1,"right":1},"bbb":{"left":1,"right":1}}}}}`)
	for range 20 {
		_, err := ParseCarryForward(raw)
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), `structure "a"`) || !strings.Contains(err.Error(), `"bbb"`) {
			t.Fatalf("err = %v, want it to name structure a and key bbb", err)
		}
	}
}
