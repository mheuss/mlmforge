package networkengine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mlmforge/mlmforge/internal/config"
)

func TestDistributorIDs(t *testing.T) {
	t.Run("two valid UUIDs parse to sorted slice", func(t *testing.T) {
		m := map[string]DistributorPrimitivesDTO{
			"00000000-0000-0000-0000-000000000002": {},
			"00000000-0000-0000-0000-000000000001": {},
		}
		ids, err := distributorIDs(m)
		require.NoError(t, err)
		require.Len(t, ids, 2)
		assert.True(t, ids[0].String() < ids[1].String(), "expected ascending order by string")
		assert.Equal(t, "00000000-0000-0000-0000-000000000001", ids[0].String())
		assert.Equal(t, "00000000-0000-0000-0000-000000000002", ids[1].String())
	})

	t.Run("bad UUID key returns error naming the key", func(t *testing.T) {
		m := map[string]DistributorPrimitivesDTO{
			"not-a-uuid": {},
		}
		_, err := distributorIDs(m)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not-a-uuid")
	})

	t.Run("empty map returns empty slice and no error", func(t *testing.T) {
		m := map[string]DistributorPrimitivesDTO{}
		ids, err := distributorIDs(m)
		require.NoError(t, err)
		assert.Empty(t, ids)
	})
}

// monthlyWindowPlan builds a minimal monthly plan for rank driver tests.
func monthlyWindowPlan(startDate string, windowPeriods uint8) *config.CompensationPlan {
	return &config.CompensationPlan{
		Period: config.PeriodConfig{Length: "month", StartDate: &startDate},
		Ranks: []config.RankDefinition{{
			Name:    "Director",
			Ordinal: 3,
			Qualification: config.RankQualification{
				Window: &config.RankQualificationWindow{
					ThresholdRank: "Director", QualifyingPeriods: 1, WindowPeriods: windowPeriods,
				},
			},
		}},
	}
}

func TestNewRankDriver_RequiresStartDate(t *testing.T) {
	plan := monthlyWindowPlan("2026-01-01", 6)
	plan.Period.StartDate = nil

	client := NewEngineClientWithTransport(&mockTransport{response: json.RawMessage(`null`)})
	store := NewMemoryQualificationHistoryStore()
	provider := NewMemoryPeriodInputProvider()

	_, err := NewRankDriver(client, store, plan, provider)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start_date")
}

func TestNewRankDriver_Succeeds(t *testing.T) {
	client := NewEngineClientWithTransport(&mockTransport{response: json.RawMessage(`null`)})
	store := NewMemoryQualificationHistoryStore()
	provider := NewMemoryPeriodInputProvider()

	driver, err := NewRankDriver(client, store, monthlyWindowPlan("2026-01-01", 6), provider)
	require.NoError(t, err)
	assert.NotNil(t, driver)
}

func TestRankDriver_GuardAfterStart(t *testing.T) {
	// Plan starts 2026-03-01; evaluating 2026-02-15 is before the plan start.
	client := NewEngineClientWithTransport(&mockTransport{response: json.RawMessage(`null`)})
	store := NewMemoryQualificationHistoryStore()
	provider := NewMemoryPeriodInputProvider()

	driver, err := NewRankDriver(client, store, monthlyWindowPlan("2026-03-01", 6), provider)
	require.NoError(t, err)

	asOf := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
	_, err = driver.EvaluatePeriod(context.Background(), asOf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "before the plan start")
}

func TestNewRankDriver_NilCollaboratorsError(t *testing.T) {
	client := NewEngineClientWithTransport(&mockTransport{response: json.RawMessage(`null`)})
	store := NewMemoryQualificationHistoryStore()
	plan := monthlyWindowPlan("2026-01-01", 6)
	provider := NewMemoryPeriodInputProvider()

	_, err := NewRankDriver(nil, store, plan, provider)
	require.Error(t, err)

	_, err = NewRankDriver(client, nil, plan, provider)
	require.Error(t, err)

	_, err = NewRankDriver(client, store, nil, provider)
	require.Error(t, err)

	_, err = NewRankDriver(client, store, plan, nil)
	require.Error(t, err)
}
