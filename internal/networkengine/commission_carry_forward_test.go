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
	if parsed.V != carryForwardVersion {
		t.Fatalf("v = %d, want %d", parsed.V, carryForwardVersion)
	}
	if len(parsed.Structures) == 0 {
		t.Fatal("fixture holds no structures")
	}
	for name, entry := range parsed.Structures {
		if entry.Kind != kindBinaryLegs {
			t.Errorf("structure %q kind = %q, want %q", name, entry.Kind, kindBinaryLegs)
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
