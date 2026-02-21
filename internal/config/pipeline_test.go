package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
		{"valid/binary-cycle-step-plan.yaml", "Binary Cycle Step", []string{"binary"}},
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
		assert.Equal(t, SeverityError, e.Severity)
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
	yamlBytes = replaceInYAML(t, yamlBytes, "payout_lag_days: 14", "payout_lag_days: 45")

	jsonBytes, errs, err := p.LoadAndValidate(yamlBytes)

	require.NoError(t, err, "no infrastructure error expected")
	require.NotNil(t, jsonBytes, "warnings should not block JSON output")

	// Verify at least one warning exists.
	var hasWarning bool
	for _, e := range errs {
		if e.Severity == SeverityWarning {
			hasWarning = true
			break
		}
	}
	assert.True(t, hasWarning, "errs should contain at least one warning")
}

// TestPipelineCommissionContentRoundTrip runs each valid fixture through the
// full pipeline and verifies that commission content values survive the
// YAML → unmarshal → resolveCommissions → translate → JSON round trip.
// This catches bugs where deserialization silently produces zero-value structs.
func TestPipelineCommissionContentRoundTrip(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	tests := []struct {
		fixture string
		// verify is called with the parsed JSON output. It checks commission
		// content values specific to each fixture.
		verify func(t *testing.T, doc map[string]any)
	}{
		{
			fixture: "valid/minimal-unilevel.yaml",
			verify: func(t *testing.T, doc map[string]any) {
				cfg := structureConfig(t, doc, 0)
				lc := cfg["level_commission"].(map[string]any)
				assert.Equal(t, float64(5), lc["commissionable_depth"])
				rt := lc["rate_table"].(map[string]any)
				assoc := rt["Associate"].(map[string]any)
				assert.Equal(t, 0.05, assoc["1"])
				assert.Equal(t, 0.04, assoc["2"])
			},
		},
		{
			fixture: "valid/binary-plan.yaml",
			verify: func(t *testing.T, doc map[string]any) {
				cfg := structureConfig(t, doc, 0)
				bc := cfg["binary_commission"].(map[string]any)
				mode := bc["mode"].(map[string]any)
				pairing, ok := mode["pairing"].(map[string]any)
				require.True(t, ok, "binary mode should contain pairing key")
				assert.Equal(t, 0.10, pairing["percent"])
				assert.Equal(t, "weaker_leg", pairing["calculation"])
				assert.Equal(t, 5000.0, pairing["cap_per_period"])
				assert.Equal(t, "carry_forward", pairing["volume_after_payout"])
			},
		},
		{
			fixture: "valid/hybrid-plan.yaml",
			verify: func(t *testing.T, doc map[string]any) {
				// First structure: unilevel
				uniCfg := structureConfig(t, doc, 0)
				lc := uniCfg["level_commission"].(map[string]any)
				assert.Equal(t, float64(5), lc["commissionable_depth"])

				// Second structure: binary with pairing
				binCfg := structureConfig(t, doc, 1)
				bc := binCfg["binary_commission"].(map[string]any)
				mode := bc["mode"].(map[string]any)
				pairing := mode["pairing"].(map[string]any)
				assert.Equal(t, 0.10, pairing["percent"])
				assert.Equal(t, "weaker_leg", pairing["calculation"])
				assert.Equal(t, "net_off", pairing["volume_after_payout"])
			},
		},
		{
			fixture: "valid/matrix-plan.yaml",
			verify: func(t *testing.T, doc map[string]any) {
				cfg := structureConfig(t, doc, 0)
				lc := cfg["level_commission"].(map[string]any)
				assert.Equal(t, float64(7), lc["commissionable_depth"])
				mp := cfg["matrix_params"].(map[string]any)
				assert.Equal(t, float64(3), mp["width"])
				assert.Equal(t, float64(7), mp["height"])
				assert.Equal(t, "breadth_first", mp["spillover_direction"])
			},
		},
		{
			fixture: "valid/stairstep-plan.yaml",
			verify: func(t *testing.T, doc map[string]any) {
				cfg := structureConfig(t, doc, 0)
				lc := cfg["level_commission"].(map[string]any)
				assert.Equal(t, float64(6), lc["commissionable_depth"])
				bk := cfg["breakaway"].(map[string]any)
				assert.Equal(t, "Supervisor", bk["threshold_rank"])
				assert.Equal(t, true, bk["group_volume_excludes_breakaway"])
				assert.Equal(t, "differential", bk["override_calculation"])
			},
		},
		{
			fixture: "valid/generation-plan.yaml",
			verify: func(t *testing.T, doc map[string]any) {
				cfg := structureConfig(t, doc, 0)
				assert.Equal(t, true, cfg["level_commissions_enabled"])
				lc := cfg["level_commission"].(map[string]any)
				assert.Equal(t, float64(5), lc["commissionable_depth"])
				gc := cfg["generation_commission"].(map[string]any)
				assert.Equal(t, float64(4), gc["max_generations"])
				assert.Equal(t, "threshold_rank", gc["boundary_mode"])
				assert.Equal(t, "Executive", gc["boundary_rank"])
			},
		},
		{
			fixture: "valid/streamline-plan.yaml",
			verify: func(t *testing.T, doc map[string]any) {
				cfg := structureConfig(t, doc, 0)
				sc := cfg["streamline_commission"].(map[string]any)
				assert.Equal(t, float64(10), sc["commissionable_depth"])
				levels := sc["dynamic_compression"].([]any)
				require.Len(t, levels, 6)
				// Levels should be sorted by level number ascending.
				l1 := levels[0].(map[string]any)
				assert.Equal(t, float64(1), l1["level"])
				assert.Equal(t, "Affiliate", l1["min_rank"])
				assert.Equal(t, 0.10, l1["percent"])
				l6 := levels[5].(map[string]any)
				assert.Equal(t, float64(6), l6["level"])
				assert.Equal(t, "Director", l6["min_rank"])
				assert.Equal(t, 0.02, l6["percent"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			yamlBytes := readFixture(t, tt.fixture)
			jsonBytes, errs, err := p.LoadAndValidate(yamlBytes)

			require.NoError(t, err)
			assert.False(t, hasErrors(errs), "no hard errors expected, got: %v", errs)
			require.NotNil(t, jsonBytes)

			var doc map[string]any
			require.NoError(t, json.Unmarshal(jsonBytes, &doc))

			tt.verify(t, doc)
		})
	}
}

// structureConfig extracts the "config" object from the Nth structure in
// the translated JSON output.
func structureConfig(t *testing.T, doc map[string]any, index int) map[string]any {
	t.Helper()
	structures := doc["structures"].([]any)
	require.Greater(t, len(structures), index, "expected at least %d structures", index+1)
	s := structures[index].(map[string]any)
	cfg, ok := s["config"].(map[string]any)
	require.True(t, ok, "structure %d should have a config object", index)
	return cfg
}

func TestNewPipelineInvalidSchemaPath(t *testing.T) {
	_, err := NewPipeline("/nonexistent/schema.json")
	require.Error(t, err)
}

func TestNewPipelineMalformedSchema(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "bad-schema.json")
	err := os.WriteFile(tmpFile, []byte(`{"type": "not-a-real-type"}`), 0644)
	require.NoError(t, err)

	_, err = NewPipeline(tmpFile)
	require.Error(t, err)
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
