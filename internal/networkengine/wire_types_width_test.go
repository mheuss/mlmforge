package networkengine

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWireTypesNarrowMirrors pins the width of the wire DTO fields that mirror
// narrow Rust engine types, so a silent widening (e.g. uint8 -> int) is caught
// here rather than truncating at the Rust FFI boundary. This is the wire-DTO
// counterpart to the compensation-plan config width contract (HEU-513 MVF-2).
//
// Scope: it guards the KNOWN narrow mirrors in wire_types.go. wire_types.go also
// holds many genuine int/int64 fields (positions, timestamps, counts, IDs) that
// are NOT width mirrors, so a comprehensive AST drift scan for this surface (like
// the config-side TestConfigContract_NoUntypedIntFields, which would need an
// allow-list of those genuine ints) is a tracked follow-up, not part of this PR.
func TestWireTypesNarrowMirrors(t *testing.T) {
	cases := []struct {
		name  string
		typ   reflect.Type
		field string
		want  string
	}{
		{"StreamlineCommissionDTO.CommissionableDepth", reflect.TypeFor[StreamlineCommissionDTO](), "CommissionableDepth", "uint8"},
		{"StreamlineLevelDTO.Level", reflect.TypeFor[StreamlineLevelDTO](), "Level", "uint8"},
		{"StreamConfigDTO.AdditionalPerRank", reflect.TypeFor[StreamConfigDTO](), "AdditionalPerRank", "map[string]uint8"},
		{"EvaluatedRankDTO.Ordinal", reflect.TypeFor[EvaluatedRankDTO](), "Ordinal", "uint16"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, ok := c.typ.FieldByName(c.field)
			require.True(t, ok, "%s: field %s not found", c.name, c.field)
			assert.Equal(t, c.want, f.Type.String(),
				"%s width drifted — must stay %s to mirror its narrow Rust engine type",
				c.name, c.want)
		})
	}
}
