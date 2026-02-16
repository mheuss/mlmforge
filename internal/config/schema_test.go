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

func TestSchemaRejectsPercentageOutOfRange(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	errs := p.validateSchema(readFixture(t, "invalid/percentage-out-of-range.yaml"))
	require.NotEmpty(t, errs, "percentage out of range should produce errors")

	for _, e := range errs {
		assert.Equal(t, SeverityError, e.Severity)
	}
}
