package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// replaceInYAML substitutes the first occurrence of old with new in YAML bytes.
func replaceInYAML(yamlBytes []byte, old, new string) []byte {
	return []byte(strings.Replace(string(yamlBytes), old, new, 1))
}

func TestPipelineValidFixtureProducesJSON(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	yamlBytes := readFixture(t, "valid/minimal-unilevel.yaml")
	jsonBytes, errs, err := p.LoadAndValidate(yamlBytes)

	require.NoError(t, err, "no infrastructure error expected")
	assert.False(t, hasErrors(errs), "no hard errors expected")
	require.NotNil(t, jsonBytes, "valid fixture should produce JSON output")

	var doc map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &doc))

	assert.Equal(t, "Starter Unilevel", doc["name"])
	assert.Equal(t, float64(1), doc["version"])

	structures, ok := doc["structures"].([]any)
	require.True(t, ok, "structures should be an array")
	require.Len(t, structures, 1)

	s := structures[0].(map[string]any)
	assert.Equal(t, "unilevel", s["type"])
	_, hasConfig := s["config"]
	assert.True(t, hasConfig, "structure should have a config object")
}

func TestPipelineAllValidFixtures(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	tests := []struct {
		fixture        string
		name           string
		structureTypes []string
	}{
		{"valid/minimal-unilevel.yaml", "Starter Unilevel", []string{"unilevel"}},
		{"valid/full-unilevel.yaml", "Premium Unilevel", []string{"unilevel"}},
		{"valid/binary-plan.yaml", "Classic Binary", []string{"binary"}},
		{"valid/hybrid-plan.yaml", "Hybrid Unilevel-Binary", []string{"unilevel", "binary"}},
		{"valid/matrix-plan.yaml", "Forced Matrix 3x7", []string{"matrix"}},
		{"valid/stairstep-plan.yaml", "Classic Stairstep Breakaway", []string{"stairstep"}},
		{"valid/generation-plan.yaml", "Generation Override Plan", []string{"generation"}},
		{"valid/streamline-plan.yaml", "Streamline Direct", []string{"streamline"}},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			yamlBytes := readFixture(t, tt.fixture)
			jsonBytes, errs, err := p.LoadAndValidate(yamlBytes)

			require.NoError(t, err, "no infrastructure error expected")
			assert.False(t, hasErrors(errs), "no hard errors expected, got: %v", errs)
			require.NotNil(t, jsonBytes, "valid fixture should produce JSON output")

			var doc map[string]any
			require.NoError(t, json.Unmarshal(jsonBytes, &doc))

			assert.Equal(t, tt.name, doc["name"])

			structures, ok := doc["structures"].([]any)
			require.True(t, ok, "structures should be an array")
			require.Len(t, structures, len(tt.structureTypes))

			for i, expectedType := range tt.structureTypes {
				s := structures[i].(map[string]any)
				assert.Equal(t, expectedType, s["type"], "structure %d type", i)
				_, hasConfig := s["config"]
				assert.True(t, hasConfig, "structure %d should have a config object", i)
			}
		})
	}
}

func TestPipelineInvalidFixtureReturnsErrors(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	yamlBytes := readFixture(t, "invalid/missing-name.yaml")
	jsonBytes, errs, err := p.LoadAndValidate(yamlBytes)

	require.NoError(t, err, "no infrastructure error expected")
	require.NotEmpty(t, errs, "missing-name fixture should produce errors")
	assert.Nil(t, jsonBytes, "invalid fixture should not produce JSON output")

	for _, e := range errs {
		assert.Equal(t, "error", e.Severity)
	}
}

func TestPipelineSchemaErrorsBlockBusinessRules(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	yamlBytes := readFixture(t, "invalid/missing-name.yaml")
	_, errs, err := p.LoadAndValidate(yamlBytes)

	require.NoError(t, err)
	require.NotEmpty(t, errs)

	// All errors should be schema violations. Business rules never ran.
	for _, e := range errs {
		assert.Equal(t, "schema_violation", e.Code,
			"expected only schema_violation errors, got %q: %s", e.Code, e.Message)
	}
}

func TestPipelineWarningsAllowJSON(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	yamlBytes := readFixture(t, "valid/minimal-unilevel.yaml")
	// Set payout_lag_days to 45, which triggers a warning (>30).
	yamlBytes = replaceInYAML(yamlBytes, "payout_lag_days: 14", "payout_lag_days: 45")

	jsonBytes, errs, err := p.LoadAndValidate(yamlBytes)

	require.NoError(t, err, "no infrastructure error expected")
	require.NotNil(t, jsonBytes, "warnings should not block JSON output")

	// Verify at least one warning exists.
	var hasWarning bool
	for _, e := range errs {
		if e.Severity == "warning" {
			hasWarning = true
			break
		}
	}
	assert.True(t, hasWarning, "errs should contain at least one warning")
}

func TestPipelineBadYAMLReturnsParseError(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	badYAML := []byte("not: [valid yaml\n  broken")
	jsonBytes, errs, err := p.LoadAndValidate(badYAML)

	require.NoError(t, err, "parse errors are validation errors, not infrastructure errors")
	require.NotEmpty(t, errs)
	assert.Nil(t, jsonBytes)
	assert.Equal(t, "yaml_parse_error", errs[0].Code)
}
