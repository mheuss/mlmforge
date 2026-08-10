package networkengine

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
