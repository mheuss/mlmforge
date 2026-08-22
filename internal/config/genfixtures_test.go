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
// configContractFixtures maps each engine_fixture name to its authoring plan.
// Each authoring plan carries the tightened fields for its structure type;
// full-unilevel is the rich fixture for the bonus/eligibility/period/placement
// fields. Shared by the regenerator and the golden guard below.
//
// One of these has a consumer outside the width contract. The Rust worker's
// integration suite pulls `generation.json` in through `include_str!` as its
// only generation test plan, and depends on two things the width contract does
// not promise: that the plan passes `load_plan` validation, and that its
// structure stays named `GenTree`. See `GENERATION_TEST_PLAN_JSON` in
// `engine/network-engine-worker/tests/worker_integration.rs` (HEU-626).
//
// Changing `valid/generation-plan.yaml` in a way that breaks either one fails
// the Rust tests, not this file. That is loud rather than silent, but the
// signal arrives in the other language, so look here first.
var configContractFixtures = map[string]string{
	"unilevel":   "valid/full-unilevel.yaml",
	"matrix":     "valid/matrix-plan.yaml",
	"stairstep":  "valid/stairstep-plan.yaml",
	"generation": "valid/generation-plan.yaml",
	"streamline": "valid/streamline-plan.yaml",
	"board":      "valid/board-plan.yaml",
}

// engineFixture runs the real pipeline for an authoring plan and returns the
// engine JSON formatted exactly as the committed fixture is written (2-space
// indent, trailing newline).
func engineFixture(t *testing.T, p *Pipeline, authoring string) []byte {
	t.Helper()
	engineJSON, errs, err := p.LoadAndValidate(readFixture(t, authoring))
	require.NoError(t, err, "%s pipeline error", authoring)
	require.Empty(t, errs, "%s should validate cleanly", authoring)

	var buf bytes.Buffer
	require.NoError(t, json.Indent(&buf, engineJSON, "", "  "))
	buf.WriteByte('\n')
	return buf.Bytes()
}

func TestGenerateConfigContractFixtures(t *testing.T) {
	if os.Getenv("REGEN_FIXTURES") != "1" {
		t.Skip("set REGEN_FIXTURES=1 to regenerate engine config-contract fixtures")
	}
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)

	outDir := filepath.Join("..", "..", "engine", "testdata", "config_contract", "fixtures")
	require.NoError(t, os.MkdirAll(outDir, 0o755))

	for name, fixture := range configContractFixtures {
		require.NoError(t, os.WriteFile(filepath.Join(outDir, name+".json"), engineFixture(t, p, fixture), 0o644))
	}
}

// TestConfigContractFixturesMatchPipeline is the golden guard for the engine
// config-contract fixtures: it asserts that live translateToEngine output
// byte-equals each committed fixture. This locks the wire SHAPE — including the
// type-before-config key order (byte-stability since HEU-648, not a parsing
// requirement) that HEU-513's contract fix and the Rust width
// test (Task 15) depend on. Without it, a translate.go regression would desync
// the fixtures from real output while every other test stayed green (the fixtures
// are only REGEN-written, never compared; the Rust test reads the stale files).
// Output is deterministic (fixed struct order, sorted maps), so this is stable.
func TestConfigContractFixturesMatchPipeline(t *testing.T) {
	p, err := NewPipeline(schemaPath(t))
	require.NoError(t, err)
	dir := filepath.Join("..", "..", "engine", "testdata", "config_contract", "fixtures")
	for name, fixture := range configContractFixtures {
		t.Run(name, func(t *testing.T) {
			committed, err := os.ReadFile(filepath.Join(dir, name+".json"))
			require.NoError(t, err)
			require.Equal(t, string(committed), string(engineFixture(t, p, fixture)),
				"engine fixture %s.json is stale — live translateToEngine output differs "+
					"(wire-shape/ordering drift?). Regenerate: REGEN_FIXTURES=1 go test "+
					"./internal/config/ -run TestGenerateConfigContractFixtures", name)
		})
	}
}
