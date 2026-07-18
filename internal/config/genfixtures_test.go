package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGenerateConfigContractFixtures regenerates the engine-shape config-contract
// fixtures consumed by the width-manifest boundary tests (Tasks 13-15). It runs
// the real Go pipeline (schema validate -> resolve -> translateToEngine) on each
// authoring fixture and writes the engine JSON, so the fixture matches exactly
// what the Rust worker receives. Guarded by REGEN_FIXTURES so a normal `go test`
// run never writes into the source tree.
//
// Regenerate with: REGEN_FIXTURES=1 go test ./internal/config/ -run TestGenerateConfigContractFixtures
func TestGenerateConfigContractFixtures(t *testing.T) {
	if os.Getenv("REGEN_FIXTURES") == "" {
		t.Skip("set REGEN_FIXTURES=1 to regenerate engine config-contract fixtures")
	}
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	outDir := filepath.Join("..", "..", "engine", "testdata", "config_contract", "fixtures")
	require.NoError(t, os.MkdirAll(outDir, 0o755))

	// engine_fixture name -> authoring fixture. Each authoring plan carries the
	// tightened fields for its structure type; full-unilevel is the rich fixture
	// for the bonus/eligibility/period/placement fields.
	fixtures := map[string]string{
		"unilevel":   "valid/full-unilevel.yaml",
		"matrix":     "valid/matrix-plan.yaml",
		"stairstep":  "valid/stairstep-plan.yaml",
		"generation": "valid/generation-plan.yaml",
		"streamline": "valid/streamline-plan.yaml",
		"board":      "valid/board-plan.yaml",
	}
	for name, fixture := range fixtures {
		engineJSON, errs, err := p.LoadAndValidate(readFixture(t, fixture))
		require.NoError(t, err, "%s pipeline error", fixture)
		require.Empty(t, errs, "%s should validate cleanly", fixture)

		var buf bytes.Buffer
		require.NoError(t, json.Indent(&buf, engineJSON, "", "  "))
		buf.WriteByte('\n')
		require.NoError(t, os.WriteFile(filepath.Join(outDir, name+".json"), buf.Bytes(), 0o644))
	}
}
