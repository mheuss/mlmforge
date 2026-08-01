package networkengine

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamlineStructureConfigDTO_Deserialization(t *testing.T) {
	jsonStr := `{
		"name": "main_stream",
		"streamline_commission": {
			"volume_to_dollar_multiplier": 0.5,
			"commissionable_depth": 10,
			"dynamic_compression": [
				{"level": 1, "min_rank": "active", "percent": 0.05},
				{"level": 2, "min_rank": "silver", "percent": 0.03},
				{"level": 3, "min_rank": "gold", "percent": 0.01}
			],
			"streams": {
				"additional_per_rank": {"silver": 1, "gold": 2},
				"assignment_mode": "round_robin",
				"per_enrollment_choice": false,
				"freeze_on_demotion": true
			}
		}
	}`

	var dto StreamlineStructureConfigDTO
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &dto))

	assert.Equal(t, "main_stream", dto.Name)
	assert.Equal(t, uint8(10), dto.StreamlineCommission.CommissionableDepth)
	require.Len(t, dto.StreamlineCommission.DynamicCompression, 3)
	assert.Equal(t, "silver", dto.StreamlineCommission.DynamicCompression[1].MinRank)
	assert.Equal(t, 0.05, dto.StreamlineCommission.DynamicCompression[0].Percent)
	assert.Equal(t, 0.03, dto.StreamlineCommission.DynamicCompression[1].Percent)
	assert.Equal(t, 0.01, dto.StreamlineCommission.DynamicCompression[2].Percent)
	require.NotNil(t, dto.StreamlineCommission.Streams)
	assert.Equal(t, uint8(2), dto.StreamlineCommission.Streams.AdditionalPerRank["gold"])
	assert.True(t, dto.StreamlineCommission.Streams.FreezeOnDemotion)
	require.NotNil(t, dto.StreamlineCommission.VolumeToDollarMultiplier)
	assert.Equal(t, 0.5, *dto.StreamlineCommission.VolumeToDollarMultiplier)
}

func TestStreamlineStructureConfigDTO_NilOptionalFields(t *testing.T) {
	jsonStr := `{
		"name": "simple_stream",
		"streamline_commission": {
			"commissionable_depth": 5,
			"dynamic_compression": [
				{"level": 1, "min_rank": "active", "percent": 0.10}
			]
		}
	}`

	var dto StreamlineStructureConfigDTO
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &dto))

	assert.Nil(t, dto.StreamlineCommission.VolumeToDollarMultiplier)
	assert.Nil(t, dto.StreamlineCommission.Streams)
	assert.Equal(t, uint8(5), dto.StreamlineCommission.CommissionableDepth)
	require.Len(t, dto.StreamlineCommission.DynamicCompression, 1)
	assert.Equal(t, 0.10, dto.StreamlineCommission.DynamicCompression[0].Percent)
}

func TestEvaluateRanksRequest_RoundTrip(t *testing.T) {
	src := EvaluateRanksRequest{
		Distributors: map[string]DistributorPrimitivesDTO{
			"00000000-0000-0000-0000-000000000001": {
				PersonalVolume:   100.0,
				RetailVolume:     25.0,
				Status:           "active",
				HasOrderInPeriod: true,
				ActiveProducts:   []string{"kit-a"},
			},
		},
		VolumeSources: []VolumeSourceDTO{
			{SourceID: "00000000-0000-0000-0000-000000000001", CVAmount: 50.0},
		},
	}

	b, err := json.Marshal(src)
	require.NoError(t, err)

	var got EvaluateRanksRequest
	require.NoError(t, json.Unmarshal(b, &got))
	require.Len(t, got.Distributors, 1)
	require.Equal(t, "active", got.Distributors["00000000-0000-0000-0000-000000000001"].Status)
	require.Len(t, got.VolumeSources, 1)
}

func TestEvaluatedRankDTO_QualifiedDeserialization(t *testing.T) {
	jsonStr := `{"kind":"qualified","rank":"silver","ordinal":2}`
	var dto EvaluatedRankDTO
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &dto))
	assert.Equal(t, "qualified", dto.Kind)
	assert.Equal(t, "silver", dto.Rank)
	assert.Equal(t, uint16(2), dto.Ordinal)
}

func TestEvaluatedRankDTO_UnrankedDeserialization(t *testing.T) {
	jsonStr := `{"kind":"unranked"}`
	var dto EvaluatedRankDTO
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &dto))
	assert.Equal(t, "unranked", dto.Kind)
	assert.Equal(t, "", dto.Rank)
	assert.Equal(t, uint16(0), dto.Ordinal)
}

func TestEvaluatedRankDTO_QualifiedMarshalShape(t *testing.T) {
	dto := EvaluatedRankDTO{Kind: "qualified", Rank: "silver", Ordinal: 2}
	b, err := json.Marshal(dto)
	require.NoError(t, err)
	assert.JSONEq(t, `{"kind":"qualified","rank":"silver","ordinal":2}`, string(b))
}

func TestEvaluatedRankDTO_UnrankedMarshalShape(t *testing.T) {
	dto := EvaluatedRankDTO{Kind: "unranked"}
	b, err := json.Marshal(dto)
	require.NoError(t, err)
	assert.JSONEq(t, `{"kind":"unranked"}`, string(b))
}

func TestEvaluateRanksRequest_EmptyOmitsHistory(t *testing.T) {
	req := EvaluateRanksRequest{
		Distributors:  map[string]DistributorPrimitivesDTO{},
		VolumeSources: []VolumeSourceDTO{},
	}
	b, err := json.Marshal(req)
	require.NoError(t, err)
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(b, &m))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	assert.ElementsMatch(t, []string{"distributors", "volume_sources"}, keys,
		"an empty request must omit history_window/history (omitempty)")
}

func TestEvaluateRanksRequest_PopulatedIncludesHistory(t *testing.T) {
	uid := uuid.New()
	two := uint16(2)
	var unranked *uint16 // nil
	req := EvaluateRanksRequest{
		Distributors:  map[string]DistributorPrimitivesDTO{},
		VolumeSources: []VolumeSourceDTO{},
		HistoryWindow: []string{"2026-05", "2026-04"},
		History: map[string]map[string]*uint16{
			uid.String(): {"2026-05": &two, "2026-04": unranked},
		},
	}
	b, err := json.Marshal(req)
	require.NoError(t, err)
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(b, &m))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	assert.ElementsMatch(t,
		[]string{"distributors", "volume_sources", "history_window", "history"}, keys)
	assert.Contains(t, string(b), `"2026-04":null`) // Unranked survives as null
	var back EvaluateRanksRequest
	require.NoError(t, json.Unmarshal(b, &back))
	assert.Nil(t, back.History[uid.String()]["2026-04"])
	assert.Equal(t, uint16(2), *back.History[uid.String()]["2026-05"])
}
