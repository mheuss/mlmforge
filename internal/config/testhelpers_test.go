package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlmforge/mlmforge/internal/dec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// schemaPath returns the absolute path to the JSON Schema file.
// It verifies the file exists and fails the test if not found.
func schemaPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "schemas", "compensation-plan.schema.json")
	_, err := os.Stat(path)
	require.NoError(t, err, "schema file not found at %s", path)
	return path
}

// readFixture reads a test fixture file from the testdata directory.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path)
	require.NoError(t, err, "fixture not found at %s", path)
	return data
}

// replaceInYAML substitutes the first occurrence of old with new in YAML bytes.
// Fails the test if old is not found, preventing silent no-op replacements.
func replaceInYAML(t *testing.T, yamlBytes []byte, old, new string) []byte {
	t.Helper()
	result := strings.Replace(string(yamlBytes), old, new, 1)
	if result == string(yamlBytes) {
		t.Fatalf("replaceInYAML: %q not found in input", old)
	}
	return []byte(result)
}

// minimalPlan returns a valid CompensationPlan for use as a baseline in
// business-rule tests. Each test introduces one violation on top of this
// known-good plan.
func minimalPlan() *CompensationPlan {
	startDate := "2026-01-01"
	return &CompensationPlan{
		Name:    "Test Plan",
		Version: 2,
		Period: PeriodConfig{
			Length:        "month",
			StartDate:     &startDate,
			PayoutLagDays: 14,
		},
		Volume: VolumeConfig{
			BaseCurrency:             "USD",
			VolumeToDollarMultiplier: dec.MustParse("1.0"),
		},
		Ranks: []RankDefinition{
			{
				Name:    "Associate",
				Ordinal: 1,
				Qualification: RankQualification{
					Structures: []StructureQualification{
						{Structure: "Primary"},
					},
					RequiredProducts: []string{},
				},
				DemotionPolicy: DemotionPolicy{StringValue: "promotion_only"},
			},
			{
				Name:    "Silver",
				Ordinal: 2,
				Qualification: RankQualification{
					Structures: []StructureQualification{
						{
							Structure:      "Primary",
							PersonalVolume: dec.MustParse("100"),
							GroupVolume:    dec.MustParse("3000"),
						},
					},
					RequiredProducts: []string{},
				},
				QualifiedStructures: []string{"Primary"},
				DemotionPolicy:      DemotionPolicy{StringValue: "promotion_only"},
			},
		},
		RankTracking: RankTrackingConfig{TrackAchievedRank: false},
		CommissionEligibility: CommissionEligibility{
			EligibleStatuses: []string{"active"},
			ActiveLegTiers:   []ActiveLegTier{},
		},
		Structures: []StructureConfig{
			{Name: "Primary", Type: "unilevel"},
		},
		Payout: PayoutConfig{
			BaseCurrency:  "USD",
			MinimumAmount: dec.MustParse("50"),
			Methods:       []PayoutMethod{{Type: "bank_transfer", Fee: dec.MustParse("2.50")}},
		},
		Caps: CapsConfig{
			CompanyPayoutCapPercent: dec.MustParse("0.42"),
			CapEnforcement:          "pro_rata",
		},
		Placement: PlacementConfig{
			DonatedPlacementEnabled: false,
		},
	}
}

// minimalPlanWithCommission returns a minimalPlan() with the commission
// resolved on the first structure. Translation requires resolved commissions.
func minimalPlanWithCommission() *CompensationPlan {
	plan := minimalPlan()
	plan.Structures[0].resolvedCommission = &UnilevelCommission{
		BroadCommissionPercent: dec.MustParse("0.40"),
		CommissionableDepth:    5,
		RateTable: map[string]map[string]dec.Amount{
			"Associate": {"1": dec.MustParse("0.05"), "2": dec.MustParse("0.04")},
		},
	}
	return plan
}

// minimalPlanWithGenerationStructure returns minimalPlan() with a Generation
// structure resolved on the first structure. Used by tests that exercise
// GenerationCommission validation paths.
func minimalPlanWithGenerationStructure() *CompensationPlan {
	plan := minimalPlan()
	plan.Structures[0].Type = "generation"
	plan.Structures[0].resolvedCommission = &GenerationCommission{
		Generation: GenerationCommissionConfig{
			MaxGenerations:                3,
			GenerationRates:               map[string]dec.Amount{"1": dec.MustParse("0.10")},
			BoundaryMode:                  "threshold_rank",
			BoundaryRank:                  "Silver",
			EmptyGenerationConsumesNumber: false,
		},
	}
	return plan
}

// commissionNode parses YAML text into the node a structure's commission block holds.
func commissionNode(t *testing.T, text string) yaml.Node {
	t.Helper()
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(text), &doc))
	return *doc.Content[0]
}

// assertAmount checks that got equals the decimal value want names.
func assertAmount(t *testing.T, want string, got dec.Amount, msgAndArgs ...any) {
	t.Helper()
	if !dec.MustParse(want).Equal(got.Decimal) {
		assert.Fail(t, fmt.Sprintf("want %s, got %s", want, got.String()), msgAndArgs...)
	}
}
