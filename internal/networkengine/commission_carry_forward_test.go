package networkengine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mlmforge/mlmforge/internal/dec"
	"github.com/shopspring/decimal"
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

// assertRefusalNames fails unless err is non-nil and its text contains want.
func assertRefusalNames(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Errorf("got no error, want one containing %q", want)
		return
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want it to contain %q", err, want)
	}
}

func TestCarryForwardFromRefusesABadStructureName(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"", "structure name must be non-empty"},
		{"bad\xff", `structure "bad\xff" is not valid UTF-8`},
		{"nul\x00byte", `structure "nul\x00byte" contains a NUL byte`},
		{"bad\uFFFD", "structure \"bad\uFFFD\" contains U+FFFD"},
	} {
		_, err := CarryForwardFrom(map[string]map[string]LegVolumesDTO{tc.name: nil})
		assertRefusalNames(t, err, tc.want)
	}
}

func TestCarryForwardFromRefusesANonCanonicalKey(t *testing.T) {
	for _, tc := range []struct{ key, want string }{
		{"AAAAAAAA-1111-1111-1111-111111111111", `key "AAAAAAAA-1111-1111-1111-111111111111" is not canonical`},
		{"{11111111-1111-1111-1111-111111111111}", `key "{11111111-1111-1111-1111-111111111111}" is not canonical`},
		{"urn:uuid:11111111-1111-1111-1111-111111111111", `key "urn:uuid:11111111-1111-1111-1111-111111111111" is not canonical`},
		{"11111111111111111111111111111111", `key "11111111111111111111111111111111" is not canonical`},
		{"not-a-uuid", `key "not-a-uuid" is not a UUID`},
	} {
		in := map[string]map[string]LegVolumesDTO{"primary": {tc.key: {}}}
		_, err := CarryForwardFrom(in)
		assertRefusalNames(t, err, tc.want)
	}
}

func TestCarryForwardFromRefusesTheNilUUID(t *testing.T) {
	in := map[string]map[string]LegVolumesDTO{"primary": {uuid.Nil.String(): {}}}
	_, err := CarryForwardFrom(in)
	assertRefusalNames(t, err, `key "00000000-0000-0000-0000-000000000000" is the nil UUID`)
}

func TestCarryForwardFromRefusesAnAmountTheReaderCannotParse(t *testing.T) {
	const key = "11111111-1111-1111-1111-111111111111"
	tooLong := dec.Amount{Decimal: decimal.New(1, 64)}
	for name, leg := range map[string]LegVolumesDTO{
		"left":  {Left: tooLong, Right: dec.MustParse("1")},
		"right": {Left: dec.MustParse("1"), Right: tooLong},
	} {
		in := map[string]map[string]LegVolumesDTO{"primary": {key: leg}}
		_, err := CarryForwardFrom(in)
		assertRefusalNames(t, err, `structure "primary": key "`+key+`" `+name+`:`)
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
	const primary = `structure "primary"`
	const atKey = primary + ` key "` + key + `"`
	cases := []struct{ name, raw, want string }{
		{"JSON null", `null`, "envelope is null"},
		{"JSON null with whitespace", ` null `, "envelope is null"},
		{"an array", `[]`, "envelope is not a JSON object"},
		{"a number", `1`, "envelope is not a JSON object"},
		{"whitespace only", `   `, "envelope is not a JSON object"},
		{"whitespace only with a newline and a tab", "\n\t ", "envelope is not a JSON object"},
		{"truncated", `{"v":1,"structures":{}`, "envelope is not a JSON object"},
		{"a member value that is not JSON", `{"v":}`, `envelope member "v": `},
		{"trailing data", `{"v":1,"structures":{}} {}`, "envelope has data after the object"},
		{"v missing", `{"structures":{}}`, `envelope field "v" is missing or null`},
		{"v null", `{"v":null,"structures":{}}`, `envelope field "v" is missing or null`},
		{"v 0", `{"v":0,"structures":{}}`, "version 0 has no decoder"},
		{"v 2", `{"v":2,"structures":{}}`, "version 2 has no decoder"},
		{"v a string", `{"v":"1","structures":{}}`, "v is not an integer"},
		{"structures missing", `{"v":1}`, `envelope field "structures" is missing or null`},
		{"structures null", `{"v":1,"structures":null}`, `envelope field "structures" is missing or null`},
		{"structures an array", `{"v":1,"structures":[]}`, "structures is not a JSON object"},
		{"unknown envelope field", `{"v":1,"structures":{},"extra":1}`, `envelope has unknown field "extra"`},
		{"entry null", entry(`null`), primary + " is null"},
		{"entry a number", entry(`1`), primary + " is not a JSON object"},
		{"entry an array", entry(`[]`), primary + " is not a JSON object"},
		{"kind missing", entry(`{"carry":{}}`), primary + " has no kind"},
		{"kind null", entry(`{"kind":null,"carry":{}}`), primary + " has no kind"},
		{"kind a number", entry(`{"kind":1,"carry":{}}`), primary + " kind is not a string"},
		{"kind an array", entry(`{"kind":["binary_legs"],"carry":{}}`), primary + " kind is not a string"},
		{"kind board_cycles", entry(`{"kind":"board_cycles","counts":{}}`), primary + ` has kind "board_cycles"`},
		{"kind unknown", entry(`{"kind":"matrix_legs","carry":{}}`), primary + ` has kind "matrix_legs"`},
		{"carry missing", entry(`{"kind":"binary_legs"}`), primary + ` field "carry" is missing or null`},
		{"carry null", entry(`{"kind":"binary_legs","carry":null}`), primary + ` field "carry" is missing or null`},
		{"carry an array", entry(`{"kind":"binary_legs","carry":[]}`), primary + " carry is not a JSON object"},
		{"unknown entry field", entry(`{"kind":"binary_legs","carry":{},"extra":1}`), primary + ` has unknown field "extra"`},
		{"leg null", withLeg(`null`), atKey + " is null"},
		{"leg an array", withLeg(`[]`), atKey + " is not a JSON object"},
		{"leg empty", withLeg(`{}`), atKey + ` field "left" is missing or null`},
		{"left missing", withLeg(`{"right":2}`), atKey + ` field "left" is missing or null`},
		{"right missing", withLeg(`{"left":1}`), atKey + ` field "right" is missing or null`},
		{"left null", withLeg(`{"left":null,"right":2}`), atKey + ` field "left" is missing or null`},
		{"right null", withLeg(`{"left":1,"right":null}`), atKey + ` field "right" is missing or null`},
		{"left a string", withLeg(`{"left":"1","right":2}`), atKey + " left: "},
		{"right a string", withLeg(`{"left":1,"right":"2"}`), atKey + " right: "},
		{"unknown leg field", withLeg(`{"left":1,"right":2,"extra":1}`), atKey + ` has unknown field "extra"`},
		{"empty structure name", `{"v":1,"structures":{"":{"kind":"binary_legs","carry":{}}}}`, "structure name must be non-empty"},
		{"NUL in structure name", `{"v":1,"structures":{"a\u0000b":{"kind":"binary_legs","carry":{}}}}`, `structure "a\x00b" contains a NUL byte`},
		{"invalid UTF-8 in structure name", "{\"v\":1,\"structures\":{\"bad\xff\":{\"kind\":\"binary_legs\",\"carry\":{}}}}", "envelope is not valid UTF-8"},
		{"non-canonical key", entry(`{"kind":"binary_legs","carry":{"AAAAAAAA-1111-1111-1111-111111111111":` + leg + `}}`), `key "AAAAAAAA-1111-1111-1111-111111111111" is not canonical`},
		{"non-UUID key", entry(`{"kind":"binary_legs","carry":{"nope":` + leg + `}}`), `key "nope" is not a UUID`},
		{"lone surrogate in structure name", `{"v":1,"structures":{"a\ud800":{"kind":"binary_legs","carry":{}}}}`, "structure \"a\uFFFD\" contains U+FFFD"},
		{"lone surrogate in key", entry(`{"kind":"binary_legs","carry":{"\ud800":` + leg + `}}`), "key \"\uFFFD\" is not a UUID"},
		{"nil UUID key", entry(`{"kind":"binary_legs","carry":{"00000000-0000-0000-0000-000000000000":` + leg + `}}`), `key "00000000-0000-0000-0000-000000000000" is the nil UUID`},
		{"duplicate envelope field", `{"v":1,"structures":{"primary":{"kind":"binary_legs","carry":{}}},"structures":{}}`, `envelope repeats member "structures"`},
		{"duplicate structure name", `{"v":1,"structures":{"a":{"kind":"binary_legs","carry":{}},"a":{"kind":"binary_legs","carry":{}}}}`, `structures repeats member "a"`},
		{"duplicate entry field", entry(`{"kind":"binary_legs","carry":{},"carry":{}}`), primary + ` repeats member "carry"`},
		{"duplicate carry key", entry(`{"kind":"binary_legs","carry":{"` + key + `":` + leg + `,"` + key + `":` + leg + `}}`), primary + ` carry repeats member "` + key + `"`},
		{"duplicate leg field", withLeg(`{"left":9,"left":1,"right":1}`), atKey + ` repeats member "left"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCarryForward(json.RawMessage(tc.raw))
			assertRefusalNames(t, err, tc.want)
		})
	}
}

func TestParseCarryForwardReadsSpacedAndReorderedInput(t *testing.T) {
	raw := json.RawMessage(`{"structures": {"primary": {"carry": ` +
		`{"11111111-1111-1111-1111-111111111111": {"right": 2.50, "left": 1.0}}, ` +
		`"kind": "binary_legs"}}, "v": 1}`)
	got, err := ParseCarryForward(raw)
	if err != nil {
		t.Fatalf("ParseCarryForward: %v", err)
	}
	assertSameCarryForward(t, got, map[string]map[string]LegVolumesDTO{
		"primary": {
			"11111111-1111-1111-1111-111111111111": {Left: dec.MustParse("1"), Right: dec.MustParse("2.5")},
		},
	})
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
