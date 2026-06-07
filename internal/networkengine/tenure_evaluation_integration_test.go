package networkengine

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tenureRankPlanJSON is a compensation plan for the tenure-gate end-to-end test.
// It mirrors windowedRankPlanJSON but swaps the window gate for a tenure gate.
// It defines three ranks:
//
//   - Associate (ordinal 1): trivial PV >= 0 on "Test", so any tree member passes.
//   - Director  (ordinal 5): trivial PV >= 0 on "Test". This is the tenure gate's
//     threshold rank and the fallback the distributor lands on when the gate is
//     not met.
//   - Executive (ordinal 6): trivial PV >= 0 on "Test" base criteria, AND a
//     tenure gate requiring achieved >= Director for the 4 most-recent
//     consecutive periods.
//
// Every rank references the "Test" structure in qualification.structures so the
// worker registers a navigator for it. A gated rank with empty structures would
// yield an empty navigator map and the distributor would never be evaluated.
const tenureRankPlanJSON = `{
    "name": "Tenure Rank Integration Test Plan",
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
                "tenure": {
                    "threshold_rank": "Director",
                    "periods": 4
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

// tenureAxis is the 12-period prior-period axis for the tenure test,
// most-recent-first (period_id DESC). Zero-padded months keep lexicographic
// order correct. The tenure gate requires the 4 most-recent consecutive periods
// (the leading 4 here: 2026-12, 2026-11, 2026-10, 2026-09) to be Director+.
var tenureAxis = []string{
	"2026-12", "2026-11", "2026-10", "2026-09", "2026-08", "2026-07",
	"2026-06", "2026-05", "2026-04", "2026-03", "2026-02", "2026-01",
}

// noGap is the sentinel for seedTenureHistory's gapIndex: when passed, every
// trailing Director period is written as Director and the streak is unbroken.
const noGap = -1

// seedTenureHistory writes one achieved-rank row per period for userID across
// tenureAxis. The trailing directorPeriods periods (the most-recent ones, the
// front of the DESC axis) are Director; the rest are Associate. If gapIndex is
// not noGap, the period at that axis index is instead written as an Unranked row
// (rank NULL), which gates as below-threshold and breaks the consecutive streak.
// Each SaveResult call replaces a whole period, matching the store contract.
func seedTenureHistory(t *testing.T, store QualificationHistoryStore, userID uuid.UUID, directorPeriods, gapIndex int) {
	t.Helper()
	ctx := context.Background()
	for i, period := range tenureAxis {
		var entry QualificationHistoryEntry
		switch {
		case i == gapIndex:
			// Evaluated, Unranked: a present row with nil rank/ordinal.
			entry = QualificationHistoryEntry{UserID: userID, Rank: nil, Ordinal: nil}
		case i < directorPeriods:
			entry = QualificationHistoryEntry{UserID: userID, Rank: strPtr("Director"), Ordinal: u16Ptr(directorOrdinal)}
		default:
			entry = QualificationHistoryEntry{UserID: userID, Rank: strPtr("Associate"), Ordinal: u16Ptr(associateOrdinal)}
		}
		require.NoError(t, store.SaveResult(ctx, period, []QualificationHistoryEntry{entry}))
	}
}

// TestTenureEvaluation_QualifiesWithFourConsecutiveDirector proves the tenure
// gate end-to-end through the real Go client -> Rust worker -> Go path. A
// distributor who achieved Director in each of the 4 most-recent consecutive
// periods meets tenure{Director, 4} and is awarded the gated Executive rank.
// Twelve periods are persisted; only the trailing 4 are Director (the earlier 8
// are Associate), so the award is driven solely by the consecutive trailing run.
func TestTenureEvaluation_QualifiesWithFourConsecutiveDirector(t *testing.T) {
	store := NewMemoryQualificationHistoryStore()
	userID := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	seedTenureHistory(t, store, userID, 4, noGap)

	got := evaluateWithHistory(t, store, userID, tenureRankPlanJSON, tenureAxis)

	assert.Equal(t, "qualified", got.Kind)
	assert.Equal(t, "Executive", got.Rank, "Director in the trailing 4 consecutive periods meets tenure{Director, 4}")
	assert.Equal(t, uint16(6), got.Ordinal)
}

// TestTenureEvaluation_WithheldWhenGapInTrailingPeriods proves the gate is
// withheld when the consecutive trailing run is broken. The trailing 4 periods
// would be Director, but 2026-10 (axis index 2, inside the trailing 4) is
// Unranked instead. That gap breaks the streak, so the Executive tenure gate
// fails and the distributor falls back to Director (ordinal 5), the highest rank
// whose criteria pass without the gate.
func TestTenureEvaluation_WithheldWhenGapInTrailingPeriods(t *testing.T) {
	store := NewMemoryQualificationHistoryStore()
	userID := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	// Put the gap at axis index 2 (2026-10), inside the trailing 4, to break the streak.
	const gapAtIndex = 2
	seedTenureHistory(t, store, userID, 4, gapAtIndex)

	got := evaluateWithHistory(t, store, userID, tenureRankPlanJSON, tenureAxis)

	assert.Equal(t, "qualified", got.Kind)
	assert.Equal(t, "Director", got.Rank, "a gap in the trailing 4 breaks the streak, so gated Executive is withheld and the rank falls back to Director")
	assert.Equal(t, uint16(5), got.Ordinal)
}
