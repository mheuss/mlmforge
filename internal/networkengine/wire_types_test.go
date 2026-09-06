package networkengine

import (
	"encoding/json"
	"errors"
	"strings"
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

// TestCommissionCalculationResultDTO decodes the shape the five calculators
// return, with the two opposite null rules in one payload: `walk` is present
// and null on the second earning, while the walk's own optionals are absent
// rather than null.
func TestCommissionCalculationResultDTO(t *testing.T) {
	const payload = `{
		"earnings": [
			{"earner_id":"a","source_id":"b","level":2,"rate":0.05,
			 "cv_amount":100.0,"dollar_amount":2.0,"walk":0},
			{"earner_id":"c","source_id":"b","level":1,"rate":0.05,
			 "cv_amount":100.0,"dollar_amount":2.0,"walk":null}
		],
		"walks": [
			{"index":0,"source_id":"b","kind":"level","steps":[
				{"node_id":"a","outcome":"paid","consumed":true,"earner_rank":"member"}
			],"stop":"root_reached"}
		],
		"plan": {"name":"Test","version":1,
		         "hash":"sha256:0000000000000000000000000000000000000000000000000000000000000000"}
	}`

	var got CommissionCalculationResultDTO
	require.NoError(t, json.Unmarshal([]byte(payload), &got))

	require.Len(t, got.Earnings, 2)
	require.NotNil(t, got.Earnings[0].Walk, "a recorded walk decodes to a value")
	assert.Equal(t, uint32(0), *got.Earnings[0].Walk)
	assert.Nil(t, got.Earnings[1].Walk, "an unrecorded walk decodes to nil, not zero")
	assert.Equal(t, uint8(2), got.Earnings[0].Level)

	require.Len(t, got.Walks, 1)
	w := got.Walks[0]
	assert.Equal(t, uint32(0), w.Index)
	assert.Equal(t, "level", w.Kind)
	assert.Equal(t, "root_reached", w.Stop)
	assert.Nil(t, w.StreamID, "an absent stream_id decodes to nil")
	assert.Nil(t, w.Rank, "an absent rank decodes to nil")
	assert.Nil(t, w.StoppedAt, "root_reached names no node")

	require.Len(t, w.Steps, 1)
	require.NotNil(t, w.Steps[0].EarnerRank)
	assert.Equal(t, "member", *w.Steps[0].EarnerRank)
	assert.True(t, w.Steps[0].Consumed)

	assert.Equal(t, "Test", got.Plan.Name)
	assert.Equal(t, uint32(1), got.Plan.Version)
	assert.True(t, strings.HasPrefix(got.Plan.Hash, "sha256:"))
}

// TestCommissionEarningDTOEmitsNullWalk pins the asymmetry from the writing
// side: `walk` must reach the wire as null, never be omitted, because an
// unrecorded walk has to stay distinguishable from a missing field.
func TestCommissionEarningDTOEmitsNullWalk(t *testing.T) {
	b, err := json.Marshal(CommissionEarningDTO{EarnerID: "a", SourceID: "b", Level: 1})
	require.NoError(t, err)
	assert.Contains(t, string(b), `"walk":null`)
}

// TestVerifyPlanIdentity covers the check that stops a caller persisting
// payouts computed under a plan its run does not name.
func TestVerifyPlanIdentity(t *testing.T) {
	const good = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	const other = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

	result := CommissionCalculationResultDTO{Plan: PlanIdentityDTO{Hash: good}}
	assert.NoError(t, VerifyPlanIdentity(result, good))

	err := VerifyPlanIdentity(result, other)
	require.Error(t, err)

	// Typed, so a caller can tell "do not persist" from "retry" with errors.As.
	var mismatch *PlanIdentityMismatchError
	require.True(t, errors.As(err, &mismatch), "must be a PlanIdentityMismatchError")
	assert.Equal(t, other, mismatch.Expected)
	assert.Equal(t, good, mismatch.Reported)
	assert.Contains(t, err.Error(), other)
	assert.Contains(t, err.Error(), good)
}
