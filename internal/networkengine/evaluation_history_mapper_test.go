package networkengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvaluationResultToHistoryEntries_QualifiedAndUnranked(t *testing.T) {
	result := &EvaluationResultDTO{
		Ranks: map[string]EvaluatedRankDTO{
			"00000000-0000-0000-0000-000000000001": {Kind: "qualified", Rank: "silver", Ordinal: 2},
			"00000000-0000-0000-0000-000000000002": {Kind: "unranked"},
		},
	}

	entries, err := evaluationResultToHistoryEntries(result)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	// Index by UserID for stable assertions.
	byUser := map[string]QualificationHistoryEntry{}
	for _, e := range entries {
		byUser[e.UserID.String()] = e
	}

	q, ok := byUser["00000000-0000-0000-0000-000000000001"]
	require.True(t, ok, "qualified user must be present in mapped entries")
	require.NotNil(t, q.Rank)
	require.NotNil(t, q.Ordinal)
	assert.Equal(t, "silver", *q.Rank)
	assert.Equal(t, uint16(2), *q.Ordinal)

	u, ok := byUser["00000000-0000-0000-0000-000000000002"]
	require.True(t, ok, "unranked user must be present in mapped entries")
	assert.Nil(t, u.Rank)
	assert.Nil(t, u.Ordinal)
}

func TestEvaluationResultToHistoryEntries_RejectsInvalidUUID(t *testing.T) {
	result := &EvaluationResultDTO{
		Ranks: map[string]EvaluatedRankDTO{
			"not-a-uuid": {Kind: "unranked"},
		},
	}

	_, err := evaluationResultToHistoryEntries(result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse user_id")
}

func TestEvaluationResultToHistoryEntries_RejectsUnknownKind(t *testing.T) {
	result := &EvaluationResultDTO{
		Ranks: map[string]EvaluatedRankDTO{
			"00000000-0000-0000-0000-000000000001": {Kind: "bogus"},
		},
	}

	_, err := evaluationResultToHistoryEntries(result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown rank kind")
}

func TestEvaluationResultToHistoryEntries_RejectsQualifiedEmptyRank(t *testing.T) {
	result := &EvaluationResultDTO{
		Ranks: map[string]EvaluatedRankDTO{
			"00000000-0000-0000-0000-000000000001": {Kind: "qualified", Rank: "", Ordinal: 1},
		},
	}

	_, err := evaluationResultToHistoryEntries(result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty rank name")
}

func TestEvaluationResultToHistoryEntries_RejectsQualifiedZeroOrdinal(t *testing.T) {
	result := &EvaluationResultDTO{
		Ranks: map[string]EvaluatedRankDTO{
			"00000000-0000-0000-0000-000000000001": {Kind: "qualified", Rank: "silver", Ordinal: 0},
		},
	}

	_, err := evaluationResultToHistoryEntries(result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zero ordinal")
}
