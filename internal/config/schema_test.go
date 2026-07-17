package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestConvertYAMLToJSONMapAnyAny(t *testing.T) {
	// Simulate a map[any]any input (yaml.v3 safety net path).
	input := map[any]any{
		"name":    "test",
		42:        "numeric key",
		int64(99): "int64 key",
	}
	result := convertYAMLToJSON(input)

	m, ok := result.(map[string]any)
	require.True(t, ok, "result should be map[string]any")
	assert.Equal(t, "test", m["name"])
	assert.Equal(t, "numeric key", m["42"])
	assert.Equal(t, "int64 key", m["99"])
}
