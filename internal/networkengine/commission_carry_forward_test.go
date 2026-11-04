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
	for _, name := range []string{"", "bad\xff", "nul\x00byte"} {
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
