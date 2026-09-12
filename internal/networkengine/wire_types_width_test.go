package networkengine

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWireTypesNarrowMirrors pins the width of every wire DTO field that
// mirrors a narrow Rust engine type. A silent widening is caught here rather
// than truncating at the Rust boundary.
//
// The list is built by pairing each narrow Rust wire field with its Go
// counterpart. A Go-side search for narrow types cannot find a mirror that has
// already widened.
//
// Rows removed when their DTOs were deleted: see HEU-583.
func TestWireTypesNarrowMirrors(t *testing.T) {
	cases := []struct {
		name  string
		typ   reflect.Type
		field string
		want  string
	}{
		{"EvaluatedRankDTO.Ordinal", reflect.TypeFor[EvaluatedRankDTO](), "Ordinal", "uint16"},
		{"CommissionEarningDTO.Level", reflect.TypeFor[CommissionEarningDTO](), "Level", "uint8"},
		{"CommissionEarningDTO.Rate", reflect.TypeFor[CommissionEarningDTO](), "Rate", "*float64"},
		{"CommissionEarningDTO.Walk", reflect.TypeFor[CommissionEarningDTO](), "Walk", "*uint32"},
		{"WalkDTO.Index", reflect.TypeFor[WalkDTO](), "Index", "uint32"},
		{"WalkDTO.StreamID", reflect.TypeFor[WalkDTO](), "StreamID", "*uint32"},
		{"PlanIdentityDTO.Version", reflect.TypeFor[PlanIdentityDTO](), "Version", "uint32"},
		{"FrozenStreamSkipDTO.StreamID", reflect.TypeFor[FrozenStreamSkipDTO](), "StreamID", "uint32"},
		{"EngineNode.Depth", reflect.TypeFor[EngineNode](), "Depth", "uint32"},
		{"EnginePosition.Depth", reflect.TypeFor[EnginePosition](), "Depth", "uint32"},
		{"StreamlineAddMemberRequest.StreamIDOverride", reflect.TypeFor[StreamlineAddMemberRequest](), "StreamIDOverride", "*uint32"},
		{"EvaluateRanksRequest.History", reflect.TypeFor[EvaluateRanksRequest](), "History", "map[string]map[string]*uint16"},
		{"StreamlineAddMemberResultDTO.StreamID", reflect.TypeFor[StreamlineAddMemberResultDTO](), "StreamID", "uint32"},
		{"StreamlineExpandRequest.TotalAllowed", reflect.TypeFor[StreamlineExpandRequest](), "TotalAllowed", "uint32"},
		{"StreamlineExpandResultDTO.NewStreamIDs", reflect.TypeFor[StreamlineExpandResultDTO](), "NewStreamIDs", "[]uint32"},
		{"StreamlineUpdateAllowanceRequest.TotalAllowed", reflect.TypeFor[StreamlineUpdateAllowanceRequest](), "TotalAllowed", "uint32"},
		{"StreamlineFreezeResultDTO.Frozen", reflect.TypeFor[StreamlineFreezeResultDTO](), "Frozen", "[]uint32"},
		{"StreamlineFreezeResultDTO.Unfrozen", reflect.TypeFor[StreamlineFreezeResultDTO](), "Unfrozen", "[]uint32"},
		{"StreamlineFreezeResultDTO.Created", reflect.TypeFor[StreamlineFreezeResultDTO](), "Created", "[]uint32"},
		{"StreamlineFreezeResultDTO.Destroyed", reflect.TypeFor[StreamlineFreezeResultDTO](), "Destroyed", "[]uint32"},
		{"StreamlineRemoveMemberResultDTO.RemovedFrom", reflect.TypeFor[StreamlineRemoveMemberResultDTO](), "RemovedFrom", "[]uint32"},
		{"StreamPositionDTO.StreamID", reflect.TypeFor[StreamPositionDTO](), "StreamID", "uint32"},
		{"StreamSummaryDTO.ID", reflect.TypeFor[StreamSummaryDTO](), "ID", "uint32"},
		{"BoardCycleEarningDTO.CycleNumber", reflect.TypeFor[BoardCycleEarningDTO](), "CycleNumber", "uint32"},
		{"BoardCommissionResultDTO.UpdatedCycleCounts", reflect.TypeFor[BoardCommissionResultDTO](), "UpdatedCycleCounts", "map[string]uint32"},
		{"CalculateBoardCommissionsRequest.PeriodCycleCounts", reflect.TypeFor[CalculateBoardCommissionsRequest](), "PeriodCycleCounts", "map[string]uint32"},
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
