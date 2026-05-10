package networkengine

import (
	"encoding/json"
	"testing"

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
				{"level": 1, "min_rank": "active", "percent": 5.0},
				{"level": 2, "min_rank": "silver", "percent": 3.0},
				{"level": 3, "min_rank": "gold", "percent": 1.0}
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
				{"level": 1, "min_rank": "active", "percent": 10.0}
			]
		}
	}`

	var dto StreamlineStructureConfigDTO
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &dto))

	assert.Nil(t, dto.StreamlineCommission.VolumeToDollarMultiplier)
	assert.Nil(t, dto.StreamlineCommission.Streams)
	assert.Equal(t, uint8(5), dto.StreamlineCommission.CommissionableDepth)
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
