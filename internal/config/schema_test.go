package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Valid fixtures: should produce zero errors ---

func TestSchemaValidatesAllValidFixtures(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	fixtures := []string{
		"valid/minimal-unilevel.yaml",
		"valid/full-unilevel.yaml",
		"valid/binary-plan.yaml",
		"valid/binary-cycle-step-plan.yaml",
		"valid/hybrid-plan.yaml",
		"valid/generation-plan.yaml",
		"valid/matrix-plan.yaml",
		"valid/stairstep-plan.yaml",
		"valid/multi-tier-stairstep-plan.yaml",
		"valid/streamline-plan.yaml",
		"valid/board-plan.yaml",
		"valid/leg-quality-plan.yaml",
		"valid/windowed-rank-plan.yaml",
		"valid/tenure-rank-plan.yaml",
		"valid/leg-cap-fraction-plan.yaml",
	}
	for _, f := range fixtures {
		t.Run(f, func(t *testing.T) {
			errs := p.validateSchema(readFixture(t, f))
			assert.Empty(t, errs, "%s should validate without errors", f)
		})
	}
}

// --- Invalid fixtures: should produce errors ---

func TestSchemaRejectsMissingName(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/missing-name.yaml"))
	require.NotEmpty(t, errs, "missing name should produce errors")

	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
	}
}

func TestSchemaRejectsBadPeriodLength(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/bad-period-length.yaml"))
	require.NotEmpty(t, errs, "bad period length should produce errors")

	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
	}
}

func TestSchemaRejectsNegativePayoutLag(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/negative-payout-lag.yaml"))
	require.NotEmpty(t, errs, "negative payout lag should produce errors")

	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
	}
}

func TestSchemaRejectsUnknownStructureType(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/unknown-structure-type.yaml"))
	require.NotEmpty(t, errs, "unknown structure type should produce errors")

	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
	}
}

// TestSchemaRejectsDepthFirstSpillover pins HEU-535: depth_first is no longer an
// allowed spillover direction. The Rust engine already rejects it
// (validate.rs, via HEU-517), so the authoring schema must agree.
func TestSchemaRejectsDepthFirstSpillover(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)
	base := readFixture(t, "valid/matrix-plan.yaml")
	require.Empty(t, p.validateSchema(base), "base matrix fixture should validate cleanly")

	// Both spillover_direction sites must reject depth_first: the matrix structure
	// params AND the placement config. The two fixture lines are identical, so each
	// anchor pins one via its distinct parent (`height` vs `matrix:`) — a revert of
	// either enum then fails its own subtest, not just the first occurrence.
	sites := []struct {
		name   string
		anchor string
	}{
		{"structure params", "height: 7\n      spillover_direction: breadth_first"},
		{"placement config", "matrix:\n    spillover_direction: breadth_first"},
	}
	for _, site := range sites {
		t.Run(site.name, func(t *testing.T) {
			mutated := replaceInYAML(t, base, site.anchor,
				strings.Replace(site.anchor, "breadth_first", "depth_first", 1))
			errs := p.validateSchema(mutated)
			require.NotEmpty(t, errs, "depth_first spillover should fail the schema gate")
			foundViolation := false
			for _, e := range errs {
				if e.Code == "schema_violation" {
					foundViolation = true
					assert.Equal(t, SeverityError, e.Severity)
				}
			}
			assert.True(t, foundViolation, "expected a schema_violation for depth_first spillover")
		})
	}
}

// TestSchemaAllowsPerRankMaxGenerationsZero pins HEU-442: a per-rank
// max_generations_per_rank override of 0 (the calculator treats 0 as excluding
// that rank) must pass the schema gate. The scalar max_generations keeps
// minimum 1; only the per-rank map value allows 0.
func TestSchemaAllowsPerRankMaxGenerationsZero(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)
	base := readFixture(t, "valid/generation-plan.yaml")
	require.Empty(t, p.validateSchema(base), "base generation fixture should validate cleanly")

	perRankZero := replaceInYAML(t, base, "Executive: 3", "Executive: 0")
	assert.Empty(t, p.validateSchema(perRankZero),
		"a per-rank max_generations override of 0 should pass the schema gate")
}

func TestSchemaRejectsMissingCommission(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/missing-commission.yaml"))
	require.NotEmpty(t, errs, "missing commission should produce errors")

	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
	}
}

func TestSchemaRejectsBinaryModeWithoutConfig(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/binary-mode-without-config.yaml"))
	require.NotEmpty(t, errs, "binary mode without config should produce errors")

	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
	}
}

func TestSchemaRejectsCompressionMissingRankThreshold(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/compression-missing-rank-threshold.yaml"))
	require.NotEmpty(t, errs, "compression missing rank threshold should produce errors")

	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
	}
}

func TestSchemaRejectsInfinityMissingFlatRate(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/infinity-missing-flat-rate.yaml"))
	require.NotEmpty(t, errs, "infinity missing flat rate should produce errors")

	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
	}
}

func TestSchemaRejectsVelocityDaysMissingTimeframeDays(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/velocity-days-missing-timeframe-days.yaml"))
	require.NotEmpty(t, errs, "velocity days without timeframe_days should produce errors")

	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
	}
}

func TestSchemaRejectsSearchDepthExceedsMax(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/search-depth-exceeds-max.yaml"))
	require.NotEmpty(t, errs, "search_depth exceeding 255 should produce errors")

	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
	}
}

func TestSchemaRejectsPercentageOutOfRange(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/percentage-out-of-range.yaml"))
	require.NotEmpty(t, errs, "percentage out of range should produce errors")

	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
	}
}

// TestSchemaRejectsStreamlinePercentOutOfRange pins both bounds on
// StreamlineLevel.percent. Percent is a fraction (0.05 = 5%), so 5.0 is the
// whole-number authoring trap, not a 500% payout. One case per bound: either
// alone would leave the other free to be deleted with the suite still green.
func TestSchemaRejectsStreamlinePercentOutOfRange(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)
	base := readFixture(t, "valid/streamline-plan.yaml")
	require.Empty(t, p.validateSchema(base), "base fixture should validate cleanly")

	// See rules_test.go for why this string is duplicated rather than shared —
	// the two are produced independently and may legitimately diverge.
	const levelPercentPath = "/structures/1/commission/dynamic_compression/1/percent"

	// Anchor on two lines. A bare "percent: 0.10" would also match
	// broad_commission_percent, which sits earlier in the fixture, if it were
	// ever set to that value. Level 2 shares min_rank: Affiliate but carries a
	// different percent, so this pair is unique to level 1.
	const level1 = "min_rank: Affiliate\n          percent: "
	const level1Valid = level1 + "0.10"

	over := replaceInYAML(t, base, level1Valid, level1+"5.0")
	errs := p.validateSchema(over)
	require.NotEmpty(t, errs, "streamline percent 5.0 should fail the schema gate")
	foundOver := false
	for _, e := range errs {
		if e.Code == "schema_violation" && e.Path == levelPercentPath {
			foundOver = true
			assert.Equal(t, SeverityError, e.Severity)
		}
	}
	assert.True(t, foundOver, "expected a schema_violation at %s, got %+v", levelPercentPath, errs)

	under := replaceInYAML(t, base, level1Valid, level1+"-0.10")
	errs = p.validateSchema(under)
	require.NotEmpty(t, errs, "streamline percent -0.10 should fail the schema gate")
	foundUnder := false
	for _, e := range errs {
		if e.Code == "schema_violation" && e.Path == levelPercentPath {
			foundUnder = true
			assert.Equal(t, SeverityError, e.Severity)
		}
	}
	assert.True(t, foundUnder, "expected a schema_violation at %s, got %+v", levelPercentPath, errs)

	// Both bounds are inclusive, so the endpoints must stay legal.
	assert.Empty(t, p.validateSchema(replaceInYAML(t, base, level1Valid, level1+"1.0")),
		"streamline percent 1.0 should pass the schema gate")
	assert.Empty(t, p.validateSchema(replaceInYAML(t, base, level1Valid, level1+"0")),
		"streamline percent 0 should pass the schema gate")
}

func TestSchemaRejectsPassUpCountZero(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/pass-up-count-zero.yaml"))
	require.NotEmpty(t, errs, "pass_up count=0 should produce schema errors")

	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
	}
}

func TestSchemaAcceptsPassUpOnBinary(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	// The JSON schema does not use additionalProperties: false on commission
	// types, so pass_up on a binary commission is silently accepted. The field
	// is dropped during Go deserialization because BinaryCommission has no
	// PassUp field. This test documents that behavior.
	errs := p.validateSchema(readFixture(t, "invalid/pass-up-on-binary.yaml"))
	assert.Empty(t, errs, "pass_up on binary should not produce schema errors (extra properties are allowed)")
}

func TestStringifyKey(t *testing.T) {
	tests := []struct {
		input    any
		expected string
	}{
		{"hello", "hello"},
		{42, "42"},
		{int64(99), "99"},
		{3.14, "3.14"},
		{true, "true"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.expected, stringifyKey(tt.input), "stringifyKey(%v)", tt.input)
	}
}

// TestValidateSchemaReturnsSchemaErrorForNonValidationError verifies the
// defensive branch where jsonschema.Validate returns an error that is not
// a *jsonschema.ValidationError. This is hard to trigger naturally because
// the library consistently returns *jsonschema.ValidationError. The branch
// exists as a safety net for unexpected error types from the library.
// See schema.go:33-41.

// TestSchemaRejectsMultiTierMinSplitOutZero verifies that a multi-tier
// breakaway with a tier whose min_split_out_groups is 0 fails schema
// validation. The Go uint8 type accepts 0, but the schema's minimum: 1
// constraint is the gate. This pins the "schema is the gate" contract
// referenced in TestBreakawayTier_MinSplitOutGroups_BoundaryValues.
func TestSchemaRejectsMultiTierMinSplitOutZero(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/multi-tier-min-split-out-zero.yaml"))
	require.NotEmpty(t, errs, "min_split_out_groups=0 should produce schema errors")

	// Pin the failure mode: at least one error must be a schema_violation
	// pointing at min_split_out_groups, not some unrelated structural error
	// in the fixture. Without this, a future tightening that breaks the
	// fixture in some other way would still pass this test.
	foundMinSplitOutFailure := false
	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
		if e.Code == "schema_violation" && strings.Contains(e.Path, "min_split_out_groups") {
			foundMinSplitOutFailure = true
		}
	}
	assert.True(
		t,
		foundMinSplitOutFailure,
		"expected at least one schema_violation on min_split_out_groups, got %+v",
		errs,
	)
}

// TestSchemaRejectsRateKeyOverU8 verifies the tightened rate-map propertyNames
// (HEU-513 Part B) reject a level key above the Rust u8 range at the schema gate
// — the happy-path guard this task closes — and accept the inclusive boundary.
// Exercises the shared $defs/RateTable pattern via minimal-unilevel's rate_table
// by mutating one otherwise-valid key, so a future regex typo (admitting 256 or
// rejecting 255) fails CI instead of passing silently.
func TestSchemaRejectsRateKeyOverU8(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)
	base := readFixture(t, "valid/minimal-unilevel.yaml")
	require.Empty(t, p.validateSchema(base), "base fixture should validate cleanly")

	over := replaceInYAML(t, base, `"5": 0.01`, `"300": 0.01`)
	errs := p.validateSchema(over)
	require.NotEmpty(t, errs, "rate_table key 300 should fail the schema gate")
	foundViolation := false
	for _, e := range errs {
		if e.Code == "schema_violation" {
			foundViolation = true
			assert.Equal(t, SeverityError, e.Severity)
		}
	}
	assert.True(t, foundViolation, "expected a schema_violation for rate_table key 300, got %+v", errs)

	boundary := replaceInYAML(t, base, `"5": 0.01`, `"255": 0.01`)
	assert.Empty(t, p.validateSchema(boundary), "rate_table key 255 should pass the schema gate")
}

// TestSchemaRejectsStreamlineLevelOverU8 verifies the tightened
// dynamic_compression propertyNames (HEU-513 Task 9) reject a level key above
// the Rust u8 range at the schema gate and accept the inclusive boundary.
// Streamline levels are 1-based, so the pattern is [1, 255].
func TestSchemaRejectsStreamlineLevelOverU8(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)
	base := readFixture(t, "valid/streamline-plan.yaml")
	require.Empty(t, p.validateSchema(base), "base fixture should validate cleanly")

	over := replaceInYAML(t, base, `"3":`, `"256":`)
	errs := p.validateSchema(over)
	require.NotEmpty(t, errs, "dynamic_compression level 256 should fail the schema gate")
	foundViolation := false
	for _, e := range errs {
		if e.Code == "schema_violation" {
			foundViolation = true
			assert.Equal(t, SeverityError, e.Severity)
		}
	}
	assert.True(t, foundViolation, "expected a schema_violation for level 256, got %+v", errs)

	boundary := replaceInYAML(t, base, `"3":`, `"255":`)
	assert.Empty(t, p.validateSchema(boundary), "dynamic_compression level 255 should pass the schema gate")

	// Level 0 must also fail — streamline levels are 1-based; this locks in the
	// 1-based pattern so a future loosening to the 0-based sibling is caught.
	require.NotEmpty(t, p.validateSchema(replaceInYAML(t, base, `"3":`, `"0":`)),
		"dynamic_compression level 0 should fail the schema gate (1-based)")
}

// TestSchemaRejectsOverMaxBounds verifies a schema maximum refuses one past a
// field's ceiling and accepts the ceiling itself.
func TestSchemaRejectsOverMaxBounds(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)
	cases := []struct {
		name     string
		fixture  string
		old      string
		over     string
		boundary string
	}{
		{"max_cycles_per_period u32", "valid/board-plan.yaml", "max_cycles_per_period: 5", "max_cycles_per_period: 4294967296", "max_cycles_per_period: 4294967295"},
		{"max_cascade_depth u32", "valid/board-plan.yaml", "max_cascade_depth: 10", "max_cascade_depth: 4294967296", "max_cascade_depth: 4294967295"},
		{"stall_threshold_periods u32", "valid/board-plan.yaml", "stall_threshold_periods: 3", "stall_threshold_periods: 4294967296", "stall_threshold_periods: 4294967295"},
		{"grace count u16", "valid/full-unilevel.yaml", "count: 2\n        unit: months", "count: 65536\n        unit: months", "count: 65535\n        unit: months"},
		{"rank ordinal u16", "valid/full-unilevel.yaml", "ordinal: 4", "ordinal: 65536", "ordinal: 65535"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := readFixture(t, tc.fixture)
			require.Empty(t, p.validateSchema(base), "base fixture should validate cleanly")

			over := replaceInYAML(t, base, tc.old, tc.over)
			errs := p.validateSchema(over)
			require.NotEmpty(t, errs, "%s over-max should fail the schema gate", tc.name)
			found := false
			for _, e := range errs {
				if e.Code == "schema_violation" {
					found = true
					assert.Equal(t, SeverityError, e.Severity)
				}
			}
			assert.True(t, found, "expected schema_violation for %s, got %+v", tc.name, errs)

			// The inclusive ceiling itself must pass — guards against a maximum
			// typed one too low.
			boundary := replaceInYAML(t, base, tc.old, tc.boundary)
			assert.Empty(t, p.validateSchema(boundary), "%s at the ceiling should pass the schema gate", tc.name)
		})
	}
}

// TestSchemaRequiresAdditionalPerRank verifies additional_per_rank is required
// on StreamConfig (HEU-513 Task 10 fold-in). Rust StreamConfig.additional_streams
// has no serde(default), so a streams block omitting the field passes the old
// schema then dies opaquely at Rust serde; the schema now catches it at the gate.
func TestSchemaRequiresAdditionalPerRank(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)
	base := readFixture(t, "valid/streamline-plan.yaml")
	require.Empty(t, p.validateSchema(base), "base fixture should validate cleanly")

	// Rename the field so the streams block omits additional_per_rank
	// (StreamConfig has no additionalProperties:false, so the renamed key is
	// otherwise allowed — the only violation is the missing required field).
	without := replaceInYAML(t, base, "additional_per_rank:", "removed_field:")
	errs := p.validateSchema(without)
	require.NotEmpty(t, errs, "streams without additional_per_rank should fail the schema gate")
	found := false
	for _, e := range errs {
		if e.Code == "schema_violation" {
			found = true
			assert.Equal(t, SeverityError, e.Severity)
		}
	}
	assert.True(t, found, "expected a schema_violation for missing additional_per_rank, got %+v", errs)
}

func TestSchemaRejectsLegQualityBadPredicate(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/leg-quality-bad-predicate.yaml"))
	require.NotEmpty(t, errs, "leg_quality predicate missing min_rank should produce errors")

	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
	}
}

// TestSchemaRejectsWindowMissingThresholdRank verifies that a rank window
// object without threshold_rank fails schema validation. The window property
// has "required": ["threshold_rank", "qualifying_periods", "window_periods"]
// in the RankQualificationWindow $def.
func TestSchemaRejectsWindowMissingThresholdRank(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/window-missing-threshold-rank.yaml"))
	require.NotEmpty(t, errs, "window missing threshold_rank should produce errors")

	// Pin the failure mode: a schema_violation on the window gate whose message
	// names the missing threshold_rank property. The missing property surfaces
	// in the message rather than the path (the path points at the window object).
	foundMissingThresholdRank := false
	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
		if e.Code == "schema_violation" &&
			strings.Contains(e.Path, "window") &&
			strings.Contains(e.Message, "threshold_rank") {
			foundMissingThresholdRank = true
		}
	}
	assert.True(
		t,
		foundMissingThresholdRank,
		"expected a schema_violation on the window gate naming threshold_rank, got %+v",
		errs,
	)
}

// TestSchemaRejectsWindowPeriodsZero verifies that window_periods: 0 fails the
// minimum: 1 bound on the RankQualificationWindow $def. This pins the schema as
// the gate for the lower bound (Go uint8 accepts 0 but the plan is invalid).
func TestSchemaRejectsWindowPeriodsZero(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/window-periods-zero.yaml"))
	require.NotEmpty(t, errs, "window_periods=0 should produce schema errors")

	foundWindowPeriodsBound := false
	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
		if e.Code == "schema_violation" && strings.Contains(e.Path, "window_periods") {
			foundWindowPeriodsBound = true
		}
	}
	assert.True(
		t,
		foundWindowPeriodsBound,
		"expected at least one schema_violation on window_periods, got %+v",
		errs,
	)
}

// TestSchemaRejectsTenureMissingThresholdRank verifies that a tenure object
// without threshold_rank fails schema validation. The tenure property has
// "required": ["threshold_rank", "periods"] in the TenureRequirement $def.
func TestSchemaRejectsTenureMissingThresholdRank(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/tenure-missing-threshold-rank.yaml"))
	require.NotEmpty(t, errs, "tenure missing threshold_rank should produce errors")

	// Pin the failure mode: a schema_violation on the tenure gate whose message
	// names the missing threshold_rank property. The missing property surfaces
	// in the message rather than the path (the path points at the tenure object).
	foundMissingThresholdRank := false
	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
		if e.Code == "schema_violation" &&
			strings.Contains(e.Path, "tenure") &&
			strings.Contains(e.Message, "threshold_rank") {
			foundMissingThresholdRank = true
		}
	}
	assert.True(
		t,
		foundMissingThresholdRank,
		"expected a schema_violation on the tenure gate naming threshold_rank, got %+v",
		errs,
	)
}

// TestSchemaRejectsTenurePeriodsZero verifies that periods: 0 fails the
// minimum: 1 bound on the TenureRequirement $def. This pins the schema as the
// gate for the lower bound (Go uint8 accepts 0 but the plan is invalid).
func TestSchemaRejectsTenurePeriodsZero(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/tenure-periods-zero.yaml"))
	require.NotEmpty(t, errs, "tenure periods=0 should produce schema errors")

	foundPeriodsBound := false
	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
		if e.Code == "schema_violation" && strings.Contains(e.Path, "periods") {
			foundPeriodsBound = true
		}
	}
	assert.True(
		t,
		foundPeriodsBound,
		"expected at least one schema_violation on periods, got %+v",
		errs,
	)
}

// A bare number carries no unit, and the same digits mean a percentage under
// one reading and currency under the other. HEU-699.
func TestSchemaRejectsABareMinOverride(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)
	base := readFixture(t, "valid/stairstep-plan.yaml")
	require.Empty(t, p.validateSchema(base), "base fixture should validate cleanly")
	tagged := "min_override:\n              type: currency\n              value: 10.00"
	bare := replaceInYAML(t, base, tagged, "min_override: 10.00")
	require.NotEmpty(t, p.validateSchema(bare), "a bare min_override must be rejected")
}

func convertYAML(t *testing.T, src string) (string, []ValidationError) {
	t.Helper()
	var root yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(src), &root))
	doc, errs := yamlNodeToJSON(&root)
	out, err := json.Marshal(doc)
	require.NoError(t, err)
	return string(out), errs
}

func TestYAMLNumbersKeepTheirTextAsJSONNumbers(t *testing.T) {
	out, errs := convertYAML(t, "a: 30.30\nb: 1e-7\nc: [1, 2.50]\nrates:\n  1: 0.10\n  2: 0.05\n")
	require.Empty(t, errs)
	assert.Equal(t, `{"a":30.30,"b":1e-7,"c":[1,2.50],"rates":{"1":0.10,"2":0.05}}`, out)
}

func TestYAMLNumericFormsOutsideJSONAreRefused(t *testing.T) {
	for _, form := range []string{".inf", "-.inf", ".nan", "0x10", "0o17", "1_000", "+5", ".5", "1.", "01"} {
		_, errs := convertYAML(t, "a: "+form+"\n")
		require.Len(t, errs, 1, form)
		assert.Equal(t, "/a", errs[0].Path, form)
		assert.Equal(t, "invalid_number", errs[0].Code, form)
		assert.Equal(t, SeverityError, errs[0].Severity, form)
	}
}

func TestYAMLMergeKeepsExplicitKeysAndTheFirstMergedValue(t *testing.T) {
	out, errs := convertYAML(t, "one: &o {x: 1}\ntwo: &t {x: 2, z: 9}\nboth:\n  <<: [*o, *t]\n  y: 3\nover:\n  <<: *o\n  x: 5\n")
	require.Empty(t, errs)
	assert.Equal(t, `{"both":{"x":1,"y":3,"z":9},"one":{"x":1},"over":{"x":5},"two":{"x":2,"z":9}}`, out)
}

func TestYAMLAliasCarriesTheAnchoredNumber(t *testing.T) {
	out, errs := convertYAML(t, "base: &rate 0.07\nuse: *rate\n")
	require.Empty(t, errs)
	assert.Equal(t, `{"base":0.07,"use":0.07}`, out)
}

func TestYAMLNumericKeysAreStringified(t *testing.T) {
	out, errs := convertYAML(t, "rates:\n  1: 0.10\n  12: 0.02\n")
	require.Empty(t, errs)
	assert.Equal(t, `{"rates":{"1":0.10,"12":0.02}}`, out)
}

func TestYAMLKeysThatCollideAfterStringifyingAreRefused(t *testing.T) {
	_, errs := convertYAML(t, "m:\n  1: a\n  \"1\": b\n")
	require.Len(t, errs, 1)
	assert.Equal(t, "/m/1", errs[0].Path)
	assert.Equal(t, "duplicate_key", errs[0].Code)
}

func TestYAMLSelfReferentialAliasIsRefused(t *testing.T) {
	_, errs := convertYAML(t, "a: &a\n  b: *a\n")
	require.NotEmpty(t, errs)
	assert.Equal(t, "alias_cycle", errs[0].Code)
}

func TestYAMLMergeCycleIsRefused(t *testing.T) {
	_, errs := convertYAML(t, "a: &a\n  <<: *a\n  x: 1\n")
	require.NotEmpty(t, errs)
	assert.Equal(t, "alias_cycle", errs[0].Code)
}

func TestYAMLAliasAmplificationStopsAtTheBudget(t *testing.T) {
	src := "l0: &l0 [x, x, x, x, x, x, x, x, x, x]\n"
	for i := 1; i <= 7; i++ {
		prev := fmt.Sprintf("*l%d", i-1)
		src += fmt.Sprintf("l%d: &l%d [%s%s]\n", i, i, strings.Repeat(prev+", ", 9), prev)
	}
	_, errs := convertYAML(t, src)
	require.NotEmpty(t, errs)
	assert.Equal(t, "document_too_large", errs[len(errs)-1].Code)
}

func TestYAMLHugeExponentIsRefused(t *testing.T) {
	_, errs := convertYAML(t, "a: 0e-2147483648\n")
	require.Len(t, errs, 1)
	assert.Equal(t, "invalid_number", errs[0].Code)
}

func TestSchemaBoundsCompareExactly(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)
	base := readFixture(t, "valid/leg-cap-fraction-plan.yaml")
	require.Empty(t, p.validateSchema(base))
	over := replaceInYAML(t, base, "max_group_volume_per_leg_fraction: 0.6", "max_group_volume_per_leg_fraction: 1.00000000000000000001")
	require.NotEmpty(t, p.validateSchema(over), "a fraction above 1 by 1e-20 must fail the schema maximum")
}

func TestYAMLMergeAmplificationStopsAtTheBudget(t *testing.T) {
	src := "m0: &m0 {}\n"
	for i := 1; i <= 8; i++ {
		prev := fmt.Sprintf("*m%d", i-1)
		src += fmt.Sprintf("m%d: &m%d {<<: [%s%s]}\n", i, i, strings.Repeat(prev+", ", 9), prev)
	}
	_, errs := convertYAML(t, src)
	require.NotEmpty(t, errs)
	assert.Equal(t, "document_too_large", errs[len(errs)-1].Code)
}

func TestYAMLMergeFormsOutsideTheMergeRuleAreRefused(t *testing.T) {
	cases := map[string]string{
		"s: &s [{x: 1}]\nm:\n  <<: *s\n":         "invalid_merge",
		"a: &a {x: 1}\nm:\n  <<: [[*a]]\n":       "invalid_merge",
		"s: &s [{x: 1}]\nm:\n  <<: [*s]\n":       "invalid_merge",
		"m:\n  <<: 1\n":                          "invalid_merge",
		"a: &a {x: 1}\nm:\n  <<: *a\n  <<: *a\n": "duplicate_key",
	}
	for src, code := range cases {
		_, errs := convertYAML(t, src)
		require.Len(t, errs, 1, src)
		assert.Equal(t, code, errs[0].Code, src)
	}
}

func TestYAMLErrorIsReportedOncePerSourceNode(t *testing.T) {
	cases := map[string]string{
		"x: &x .inf\ny: [*x, *x, *x]\n":                      "invalid_number",
		"a: &a {1: x, \"1\": y}\nb: {<<: *a}\nc: {<<: *a}\n": "duplicate_key",
	}
	for src, code := range cases {
		_, errs := convertYAML(t, src)
		require.Len(t, errs, 1, src)
		assert.Equal(t, code, errs[0].Code, src)
	}
}

func TestYAMLDuplicateKeysThroughAliasesStopAtTheBudget(t *testing.T) {
	src := "d: &d {" + strings.Repeat("a: 1, ", 98) + "a: 1}\nr: [" + strings.Repeat("*d, ", 10999) + "*d]\n"
	_, errs := convertYAML(t, src)
	require.NotEmpty(t, errs)
	assert.Equal(t, "document_too_large", errs[len(errs)-1].Code)
}

func TestYAMLBudgetErrorSurvivesANodeThatAlreadyFailed(t *testing.T) {
	body := "x: &x .inf\nl1: &l1 [" + strings.Repeat("*x, ", 99) + "*x]\nl2: &l2 [" + strings.Repeat("*l1, ", 99) + "*l1]\nl3: [" + strings.Repeat("*l2, ", 99) + "*l2]\n"
	for _, prefix := range []string{"", "w: [0]\n"} {
		_, errs := convertYAML(t, prefix+body)
		codes := make([]string, len(errs))
		for i, e := range errs {
			codes[i] = e.Code
		}
		assert.Contains(t, codes, "document_too_large", "prefix %q", prefix)
	}
}

func bytesAllocatedBy(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestSchemaRefusesExcessiveAliasingBeforeConverting(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)
	src := "l0: &l0 [x, x, x, x, x, x, x, x, x, x]\n"
	for i := 1; i <= 7; i++ {
		prev := fmt.Sprintf("*l%d", i-1)
		src += fmt.Sprintf("l%d: &l%d [%s%s]\n", i, i, strings.Repeat(prev+", ", 9), prev)
	}

	errs := p.validateSchema([]byte(src))

	require.Len(t, errs, 1)
	assert.Equal(t, "yaml_parse_error", errs[0].Code)
	assert.Contains(t, errs[0].Message, "excessive aliasing")
}

func TestSchemaNestedMergeChainStaysSmall(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)
	keys := make([]string, 3000)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%d: 0", i)
	}
	src := "m: " + strings.Repeat("{<<: ", 3000) + "{" + strings.Join(keys, ", ") + "}" + strings.Repeat("}", 3000) + "\n"

	allocated := bytesAllocatedBy(func() { p.validateSchema([]byte(src)) })

	assert.Less(t, allocated, uint64(64<<20))
}

func TestSchemaAliasedLongNumberStaysSmall(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)
	src := "s: &s 1." + strings.Repeat("3", 20000) + "\nr: [" + strings.Repeat("*s, ", 299) + "*s]\n"

	allocated := bytesAllocatedBy(func() { p.validateSchema([]byte(src)) })

	assert.Less(t, allocated, uint64(64<<20))
}

func TestSchemaLongAliasKeyDoesNotRepeatInEveryError(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)
	src := "k: &k " + strings.Repeat("a", 20000) + "\nm:\n  *k : [" + strings.Repeat(".inf, ", 999) + ".inf]\n"

	errs := p.validateSchema([]byte(src))

	require.NotEmpty(t, errs)
	var size int
	for _, e := range errs {
		size += len(e.Path) + len(e.Message)
	}
	assert.Less(t, size, 1<<20)
}

func walkerCost(t *testing.T, src string) (errs []ValidationError, allocated uint64, elapsed time.Duration) {
	t.Helper()
	var root yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(src), &root))
	start := time.Now()
	allocated = bytesAllocatedBy(func() { _, errs = yamlNodeToJSON(&root) })
	return errs, allocated, time.Since(start)
}

func errorTextSize(errs []ValidationError) int {
	size := 0
	for _, e := range errs {
		size += len(e.Path) + len(e.Message)
	}
	return size
}

func TestYAMLNestingBeyondTheLimitIsRefused(t *testing.T) {
	_, errs := convertYAML(t, "a: "+strings.Repeat("[", 100)+strings.Repeat("]", 100)+"\n")

	require.Len(t, errs, 1)
	assert.Equal(t, "document_too_deep", errs[0].Code)
	assert.Equal(t, "the walk reached depth 65, counting each alias and merge followed; the limit is 64", errs[0].Message)
}

func TestYAMLConversionStopsAfterTheErrorCap(t *testing.T) {
	_, errs := convertYAML(t, "a: ["+strings.Repeat(".inf, ", 499)+".inf]\n")

	require.Len(t, errs, 101)
	assert.Equal(t, "invalid_number", errs[99].Code)
	assert.Equal(t, "too_many_errors", errs[100].Code)
}

func TestYAMLDuplicateLongKeyStatesItsLengthOutsideTheQuotes(t *testing.T) {
	key := strings.Repeat("a", 100)
	_, errs := convertYAML(t, "k: &k "+key+"\nm: {*k : 1, *k : 2}\n")

	require.Len(t, errs, 1)
	assert.Equal(t, "/m/"+strings.Repeat("a", 64)+"...", errs[0].Path)
	assert.Equal(t, `mapping key "`+strings.Repeat("a", 64)+`"... (100 bytes) appears more than once after conversion to JSON`, errs[0].Message)
}

func TestYAMLDeepMappingsUnderAliasedListsStaySmall(t *testing.T) {
	key := strings.Repeat("k", 64)
	src := "w: &w [" + strings.Repeat("1, ", 599) + "1]\nm: " + strings.Repeat("{"+key+": ", 9500) + "[" + strings.Repeat("*w, ", 599) + "*w]" + strings.Repeat("}", 9500) + "\n"

	_, allocated, _ := walkerCost(t, src)

	assert.Less(t, allocated, uint64(64<<20))
}

func TestYAMLDeepErrorsKeepLittleText(t *testing.T) {
	key := strings.Repeat("k", 64)
	src := "m: " + strings.Repeat("{"+key+": ", 9000) + "[" + strings.Repeat(".inf, ", 9999) + ".inf]" + strings.Repeat("}", 9000) + "\n"

	errs, _, _ := walkerCost(t, src)

	assert.Less(t, errorTextSize(errs), 1<<20)
}

func TestYAMLAliasedKeyIsConvertedOnce(t *testing.T) {
	keys := make([]string, 900)
	for i := range keys {
		keys[i] = "  ? !!binary " + base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%0768d", i))) + "\n  : 0\n"
	}
	src := "m: &m\n" + strings.Join(keys, "") + "r: [" + strings.Repeat("*m, ", 499) + "*m]\n"

	errs, allocated, _ := walkerCost(t, src)

	require.Empty(t, errs)
	assert.Less(t, allocated, uint64(384<<20))
}

// hostileShape is one generated document and the error code its shape must
// produce, or "" when it must convert cleanly.
type hostileShape struct {
	name string
	src  string
	want string
}

func between(r *rand.Rand, lo, hi int) int {
	return lo + r.IntN(hi-lo+1)
}

// wrap nests body inside depth containers of mixed kinds.
func wrap(r *rand.Rand, depth int, body string) string {
	var open, closers strings.Builder
	var stack []byte
	for range depth {
		if r.IntN(2) == 0 {
			open.WriteString("[")
			stack = append(stack, ']')
		} else {
			open.WriteString("{" + strings.Repeat("k", between(r, 1, 64)) + ": ")
			stack = append(stack, '}')
		}
	}
	for i := len(stack) - 1; i >= 0; i-- {
		closers.WriteByte(stack[i])
	}
	return open.String() + body + closers.String()
}

func aliasList(name string, count int) string {
	return "[" + strings.Repeat("*"+name+", ", count-1) + "*" + name + "]"
}

const hostileShapeCount = 7

// hostileYAML builds a document of at most about 1 MB in the given shape.
// Each shape's parameters are drawn so the shape reaches the code it names.
func hostileYAML(r *rand.Rand, shape int) hostileShape {
	switch shape {
	case 0:
		w, m := between(r, 500, 1000), between(r, 2500, 20000)
		src := "w: &w [" + strings.Repeat("0, ", w-1) + "0]\nb: " + wrap(r, between(r, 0, 50), aliasList("w", m)) + "\n"
		return hostileShape{"alias fan-out", src, "document_too_large"}
	case 1:
		return hostileShape{"deep nesting", "b: " + wrap(r, between(r, 70, 9000), "0") + "\n", "document_too_deep"}
	case 2:
		n := between(r, 150, 20000)
		return hostileShape{"error spray", "b: " + wrap(r, between(r, 0, 50), "["+strings.Repeat(".inf, ", n-1)+".inf]") + "\n", "too_many_errors"}
	case 3:
		fan := between(r, 5, 10)
		levels := 2
		for total := fan * fan; total < 1_200_000; total *= fan {
			levels++
		}
		var b strings.Builder
		fmt.Fprintf(&b, "m0: &m0 {x0: 1, x1: 2}\n")
		for i := 1; i <= levels; i++ {
			fmt.Fprintf(&b, "m%d: &m%d {<<: %s}\n", i, i, aliasList(fmt.Sprintf("m%d", i-1), fan))
		}
		return hostileShape{"merge chain", b.String(), "document_too_large"}
	case 4:
		text := strings.Repeat("QUFB", between(r, 256, 98304))
		src := "s: &s !!binary " + text + "\nb: " + wrap(r, between(r, 0, 50), aliasList("s", between(r, 100, 3000))) + "\n"
		return hostileShape{"aliased long scalar", src, ""}
	case 5:
		key := strings.Repeat("k", between(r, 1025, 200000))
		m := between(r, 100, 3000)
		src := "k: &k " + key + "\nb: [" + strings.Repeat("{*k : 0}, ", m-1) + "{*k : 0}]\n"
		return hostileShape{"aliased long key", src, "invalid_key"}
	default:
		number := "1" + strings.Repeat("0", between(r, 400, 200000))
		if r.IntN(2) == 0 {
			number = fmt.Sprintf("1e%d", between(r, 400, 99999))
		}
		src := "n: &n " + number + "\nb: " + wrap(r, between(r, 0, 50), aliasList("n", between(r, 100, 3000))) + "\n"
		return hostileShape{"out-of-range number", src, "invalid_number"}
	}
}

func TestYAMLWalkerStaysBoundedOnHostileDocuments(t *testing.T) {
	r := rand.New(rand.NewPCG(969, 1))
	for i := range 4 * hostileShapeCount {
		doc := hostileYAML(r, i%hostileShapeCount)
		require.LessOrEqual(t, len(doc.src), 1<<20, "document %d (%s)", i, doc.name)

		errs, allocated, elapsed := walkerCost(t, doc.src)

		codes := make([]string, len(errs))
		for j, e := range errs {
			codes[j] = e.Code
		}
		if doc.want == "" {
			assert.Empty(t, codes, "document %d (%s)", i, doc.name)
		} else {
			assert.Contains(t, codes, doc.want, "document %d (%s)", i, doc.name)
		}
		assert.Less(t, allocated, uint64(256<<20), "document %d (%s) allocated %d bytes", i, doc.name, allocated)
		assert.Less(t, elapsed, 2*time.Second, "document %d (%s)", i, doc.name)
		assert.Less(t, errorTextSize(errs), 1<<20, "document %d (%s)", i, doc.name)
	}
}

func TestYAMLKeyLongerThanTheLimitIsRefused(t *testing.T) {
	_, errs := convertYAML(t, "k: &k "+strings.Repeat("k", 1025)+"\nm: {*k : 1}\n")

	require.Len(t, errs, 1)
	assert.Equal(t, "invalid_key", errs[0].Code)
	assert.Equal(t, "a mapping key is 1025 bytes long; the limit is 1024", errs[0].Message)
}

func TestYAMLAliasedLongScalarIsConvertedOnce(t *testing.T) {
	src := "b: &b !!binary " + strings.Repeat("QUFB", 16384) + "\nr: [" + strings.Repeat("*b, ", 1999) + "*b]\n"

	_, allocated, _ := walkerCost(t, src)

	assert.Less(t, allocated, uint64(32<<20))
}

func TestSchemaRefusesAnOutOfRangeNumberInANumericField(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)
	base := readFixture(t, "valid/leg-cap-fraction-plan.yaml")
	require.Empty(t, p.validateSchema(base))
	src := replaceInYAML(t, base, "volume_to_dollar_multiplier: 1.0", "volume_to_dollar_multiplier: 1e400")

	errs := p.validateSchema(src)

	require.Len(t, errs, 1)
	assert.Equal(t, "/volume/volume_to_dollar_multiplier", errs[0].Path)
	assert.Equal(t, "invalid_number", errs[0].Code)
}

func TestSchemaRefusesAnOutOfRangeNumberInAStringField(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)
	base := readFixture(t, "valid/leg-cap-fraction-plan.yaml")
	require.Empty(t, p.validateSchema(base))
	src := replaceInYAML(t, base, "name: Leg Cap Fraction Plan", "name: 1e400")

	errs := p.validateSchema(src)

	require.Len(t, errs, 1)
	assert.Equal(t, "/name", errs[0].Path)
	assert.Equal(t, "invalid_number", errs[0].Code)
}

func TestSchemaAcceptsAQuotedOutOfRangeNumberInAStringField(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)
	base := readFixture(t, "valid/leg-cap-fraction-plan.yaml")
	require.Empty(t, p.validateSchema(base))
	src := replaceInYAML(t, base, "name: Leg Cap Fraction Plan", "name: '1e400'")

	assert.Empty(t, p.validateSchema(src))
}
