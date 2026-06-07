package networkengine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// windowedRankPlanJSON is a compensation plan for the windowed-gate end-to-end
// test. It defines three ranks:
//
//   - Associate (ordinal 1): trivial PV >= 0 on "Test", so any tree member passes.
//   - Director  (ordinal 5): trivial PV >= 0 on "Test". This is the window's
//     threshold rank and the fallback the distributor lands on when the window
//     gate is not met.
//   - Executive (ordinal 6): trivial PV >= 0 on "Test" base criteria, AND a
//     window gate requiring achieved >= Director in 6 of the last 12 periods.
//
// Every rank references the "Test" structure in qualification.structures so the
// worker registers a navigator for it. A gated rank with empty structures would
// yield an empty navigator map and the distributor would never be evaluated.
const windowedRankPlanJSON = `{
    "name": "Windowed Rank Integration Test Plan",
    "version": 1,
    "structures": [
        {
            "type": "unilevel",
            "config": {
                "name": "Test",
                "level_commission": {
                    "broad_commission_percent": 0.40,
                    "volume_to_dollar_multiplier": null,
                    "commissionable_depth": 3,
                    "rate_table": {
                        "Associate": { "1": 0.05, "2": 0.05, "3": 0.05 }
                    }
                },
                "compression": null
            }
        }
    ],
    "period": {
        "length": "month",
        "start_date": "2026-03-01",
        "payout_lag_days": 14
    },
    "volume": {
        "inhibit_signup_volume": false,
        "base_currency": "USD",
        "volume_to_dollar_multiplier": 1.0,
        "deduct_qualifying_volume": false
    },
    "ranks": [
        {
            "name": "Associate",
            "ordinal": 1,
            "qualification": {
                "structures": [
                    {
                        "structure": "Test",
                        "personal_volume": 0.0,
                        "group_volume": 0.0,
                        "max_group_volume_per_leg": 1e12,
                        "min_retail_volume": 0.0,
                        "distributor_count": null
                    }
                ],
                "required_products": []
            },
            "qualified_structures": ["Test"],
            "demotion_policy": "promotion_only"
        },
        {
            "name": "Director",
            "ordinal": 5,
            "qualification": {
                "structures": [
                    {
                        "structure": "Test",
                        "personal_volume": 0.0,
                        "group_volume": 0.0,
                        "max_group_volume_per_leg": 1e12,
                        "min_retail_volume": 0.0,
                        "distributor_count": null
                    }
                ],
                "required_products": []
            },
            "qualified_structures": ["Test"],
            "demotion_policy": "promotion_only"
        },
        {
            "name": "Executive",
            "ordinal": 6,
            "qualification": {
                "structures": [
                    {
                        "structure": "Test",
                        "personal_volume": 0.0,
                        "group_volume": 0.0,
                        "max_group_volume_per_leg": 1e12,
                        "min_retail_volume": 0.0,
                        "distributor_count": null
                    }
                ],
                "required_products": [],
                "window": {
                    "threshold_rank": "Director",
                    "qualifying_periods": 6,
                    "window_periods": 12
                }
            },
            "qualified_structures": ["Test"],
            "demotion_policy": "promotion_only"
        }
    ],
    "rank_tracking": { "track_achieved_rank": false },
    "rank_features": { "constraints_enabled": false, "overrides_enabled": false },
    "commission_eligibility": {
        "min_personal_volume": 0.0,
        "require_order_in_period": false,
        "eligible_statuses": [],
        "active_leg_tiers": []
    },
    "bonuses": {
        "matching": null,
        "sponsor": null,
        "fast_start": null,
        "rank_advancement": null,
        "leadership_development": null,
        "infinity": null,
        "lifestyle": null,
        "pool": null,
        "matrix_completion": null,
        "position": null,
        "board_cycling": null
    },
    "payout": {
        "base_currency": "USD",
        "minimum_amount": 50.0,
        "split_payouts_enabled": true,
        "methods": [
            { "type": "bank_transfer", "fee": 2.50 }
        ]
    },
    "caps": {
        "per_distributor_per_period": null,
        "company_payout_cap_percent": 0.42,
        "cap_enforcement": "pro_rata",
        "clawback_on_refund": false
    },
    "placement": {
        "donated_placement": null,
        "holding_tank": null,
        "binary_placement": null
    }
}`

// windowedAxis is the 12-period prior-period axis for the test, most-recent-first
// (period_id DESC). Zero-padded months keep lexicographic order correct.
var windowedAxis = []string{
	"2026-12", "2026-11", "2026-10", "2026-09", "2026-08", "2026-07",
	"2026-06", "2026-05", "2026-04", "2026-03", "2026-02", "2026-01",
}

// directorOrdinal and associateOrdinal are the achieved-rank ordinals seeded
// into prior-period history. The window threshold is Director (ordinal 5), so a
// Director period counts toward the gate and an Associate period does not.
const (
	directorOrdinal  uint16 = 5
	associateOrdinal uint16 = 1
)

// seedHistory writes one achieved-rank row per period for userID: Director for
// the first directorPeriods periods of the (DESC) axis, meaning the most-recent
// directorPeriods periods, and Associate for the rest.
// Each SaveResult call replaces a whole period, matching the store contract.
func seedHistory(t *testing.T, store QualificationHistoryStore, userID uuid.UUID, directorPeriods int) {
	t.Helper()
	ctx := context.Background()
	for i, period := range windowedAxis {
		rank, ord := "Associate", associateOrdinal
		if i < directorPeriods {
			rank, ord = "Director", directorOrdinal
		}
		require.NoError(t, store.SaveResult(ctx, period, []QualificationHistoryEntry{
			{UserID: userID, Rank: strPtr(rank), Ordinal: u16Ptr(ord)},
		}))
	}
}

// evaluateWithHistory runs the full Go -> worker -> Go flow for one distributor
// against the windowed plan, building the history window from store and passing
// it into EvaluateRanks. It returns the distributor's evaluated rank.
func evaluateWithHistory(t *testing.T, store QualificationHistoryStore, userID uuid.UUID) EvaluatedRankDTO {
	t.Helper()
	ctx := context.Background()

	client, err := NewEngineClient(ctx, findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	userStr := userID.String()
	require.NoError(t, client.LoadPlan(ctx, json.RawMessage(windowedRankPlanJSON)))
	require.NoError(t, client.CreateTree(ctx, structureName, "unilevel"))
	require.NoError(t, client.AddRoot(ctx, structureName, userStr, 100))

	_, hist, err := BuildHistoryWindow(ctx, store, []uuid.UUID{userID}, windowedAxis)
	require.NoError(t, err)

	req := EvaluateRanksRequest{
		Distributors: map[string]DistributorPrimitivesDTO{
			userStr: {
				PersonalVolume:   100.0,
				Status:           "active",
				HasOrderInPeriod: true,
				ActiveProducts:   []string{},
			},
		},
		VolumeSources: []VolumeSourceDTO{},
		HistoryWindow: windowedAxis,
		History:       hist,
	}

	result, err := client.EvaluateRanks(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, result)

	got, ok := result.Ranks[userStr]
	require.True(t, ok, "expected an evaluated rank for %s", userStr)
	return got
}

// TestWindowedEvaluation_QualifiesAtSixOfTwelve proves the windowed N-of-M gate
// end-to-end through the real Go client -> Rust worker -> Go path. A distributor
// who achieved Director in 6 of the last 12 periods meets window{Director, 6/12}
// and is awarded the gated Executive rank.
func TestWindowedEvaluation_QualifiesAtSixOfTwelve(t *testing.T) {
	store := NewMemoryQualificationHistoryStore()
	userID := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	seedHistory(t, store, userID, 6)

	got := evaluateWithHistory(t, store, userID)

	assert.Equal(t, "qualified", got.Kind)
	assert.Equal(t, "Executive", got.Rank, "6 Director periods meets the 6-of-12 window")
	assert.Equal(t, uint16(6), got.Ordinal)
}

// TestWindowedEvaluation_WithheldAtFiveOfTwelve proves the gate is withheld when
// the threshold is missed by one period. With Director in only 5 of 12 periods,
// the Executive window fails; the distributor falls back to Director (ordinal 5),
// the highest rank whose criteria pass without the window.
func TestWindowedEvaluation_WithheldAtFiveOfTwelve(t *testing.T) {
	store := NewMemoryQualificationHistoryStore()
	userID := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	seedHistory(t, store, userID, 5)

	got := evaluateWithHistory(t, store, userID)

	assert.Equal(t, "qualified", got.Kind)
	assert.Equal(t, "Director", got.Rank, "5 Director periods misses the 6-of-12 window, so gated Executive is withheld and the rank falls back to Director")
	assert.Equal(t, uint16(5), got.Ordinal)
}

// TestWindowedEvaluation_BackwardCompat_EmptyHistoryEqualsOmitted proves the
// history fields are backward-compatible: for a plan with no window gate,
// evaluating with explicitly-empty HistoryWindow/History returns exactly the
// same result as evaluating with those fields omitted entirely. Empty and
// omitted are both wire-invisible (omitempty), so a non-gated plan cannot
// observe them. Running both through the real worker confirms it.
func TestWindowedEvaluation_BackwardCompat_EmptyHistoryEqualsOmitted(t *testing.T) {
	ctx := context.Background()

	client, err := NewEngineClient(ctx, findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	rootID := "00000000-0000-0000-0000-000000000001"
	childID := "00000000-0000-0000-0000-000000000002"

	// rankIntegrationPlanJSON has a single "member" rank and no window gate.
	require.NoError(t, client.LoadPlan(ctx, json.RawMessage(rankIntegrationPlanJSON)))
	require.NoError(t, client.CreateTree(ctx, structureName, "unilevel"))
	require.NoError(t, client.AddRoot(ctx, structureName, rootID, 100))
	require.NoError(t, client.AddNode(ctx, structureName, childID, rootID, rootID, 200))

	distributors := map[string]DistributorPrimitivesDTO{
		rootID:  {PersonalVolume: 100.0, Status: "active", HasOrderInPeriod: true, ActiveProducts: []string{}},
		childID: {PersonalVolume: 100.0, Status: "active", HasOrderInPeriod: true, ActiveProducts: []string{}},
	}

	// Request with the history fields left at their zero value (omitted on wire).
	omitted := EvaluateRanksRequest{
		Distributors:  distributors,
		VolumeSources: []VolumeSourceDTO{},
	}
	omittedResult, err := client.EvaluateRanks(ctx, omitted)
	require.NoError(t, err)
	require.NotNil(t, omittedResult)

	// Request with the history fields explicitly set to empty (also omitted on wire).
	empty := EvaluateRanksRequest{
		Distributors:  distributors,
		VolumeSources: []VolumeSourceDTO{},
		HistoryWindow: []string{},
		History:       map[string]map[string]*uint16{},
	}
	emptyResult, err := client.EvaluateRanks(ctx, empty)
	require.NoError(t, err)
	require.NotNil(t, emptyResult)

	assert.Equal(t, omittedResult.Ranks, emptyResult.Ranks,
		"empty history fields must yield the same result as omitted ones")

	// Sanity: the non-gated plan still awards "member" to both distributors,
	// so the equivalence above is over a real, populated result.
	for _, id := range []string{rootID, childID} {
		got, ok := emptyResult.Ranks[id]
		require.True(t, ok, "expected an evaluated rank for %s", id)
		assert.Equal(t, "qualified", got.Kind)
		assert.Equal(t, "member", got.Rank)
	}
}
