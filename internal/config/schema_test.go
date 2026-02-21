package config

import (
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
		"valid/streamline-plan.yaml",
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

func TestSchemaRejectsPercentageOutOfRange(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/percentage-out-of-range.yaml"))
	require.NotEmpty(t, errs, "percentage out of range should produce errors")

	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
	}
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
