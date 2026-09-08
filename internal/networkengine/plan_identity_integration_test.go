package networkengine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mlmforge/mlmforge/internal/config"
)

// The plan identity the worker reports has to be the identity PlanHash
// computes over the same plan. Both sides run sha256 over "the plan bytes",
// but they observe those bytes at different points: PlanHash takes stage-5
// pipeline output directly, while the worker takes what arrives in
// Request.params. Between the two, transport_stdio wraps the plan in a
// protocolRequest whose Params is a json.RawMessage, and json.Marshal on the
// outer struct rewrites it two ways. It compacts insignificant whitespace. It
// also escapes <, > and & into the six-byte forms \u003c, \u003e and \u0026.
//
// A same-slice test cannot see either transformation. It hashes one buffer
// twice and passes whatever the marshal boundary does to the bytes in
// between. This drives a real worker over stdio so the comparison spans the
// boundary that could break it.
//
// What this does not prove. Both sides are handed the same slice in adjacent
// statements, so this pins sha256(x) == worker(x) for one x. It does not
// prove a production caller feeds PlanHash the same bytes it feeds LoadPlan.
// Neither function has a non-test caller yet. A Postgres jsonb round-trip is
// the concrete risk once one exists: jsonb renormalizes whitespace and key
// order, so a plan stored and reloaded before hashing would break identity
// with this test still green. The task that wires the first real caller needs
// its own assertion.
func TestPlanHashMatchesWorkerAcrossTheWire(t *testing.T) {
	root := filepath.Join("..", "..")

	pipeline, err := config.NewPipeline(filepath.Join(root, "schemas", "compensation-plan.schema.json"))
	require.NoError(t, err)

	base, err := os.ReadFile(filepath.Join(root, "internal", "config", "testdata", "valid", "minimal-unilevel.yaml"))
	require.NoError(t, err)

	// All three come from minimal-unilevel.yaml, whose own header calls it a
	// starting template for new plans. Every assertion against them lives in
	// assertIdentityMatchesAcrossTheWire, not here. Renaming the plan or bumping
	// its version there fails an assertion naming the value; renaming the
	// structure surfaces as STRUCTURE_NOT_FOUND from the Rust worker, with
	// nothing pointing back here.
	const (
		fixturePlanName    = "Starter Unilevel"
		fixturePlanVersion = uint32(1)
		structure          = "Primary"
	)

	t.Run("pipeline fixture", func(t *testing.T) {
		assertIdentityMatchesAcrossTheWire(t, pipeline, base, fixturePlanName, fixturePlanVersion, structure)
	})

	// The escaping half of that boundary is invisible to every fixture in
	// internal/config/testdata/valid, none of which contains <, > or &.
	//
	// Be precise about which refactor this catches, because the two marshal
	// sites are not interchangeable. Switching translateToEngine
	// (internal/config/translate.go) to a json.Encoder with
	// SetEscapeHTML(false) splits the two hashes apart, and this sub-case
	// fails. Verified by doing it.
	//
	// Switching the transport's marshal does not, and this sub-case stays
	// green. translateToEngine has already escaped by then, so the six-byte
	// \u0026 sequence has nothing left to escape and the transport is a no-op
	// either way. A reviewer read an earlier version of this comment as
	// claiming otherwise, which is why it now names the file.
	//
	// What still has no guard is PlanHash being fed bytes that did not come
	// from json.Marshal at all, carrying a raw &. That is the shape a jsonb
	// round trip would produce, and it is the risk the paragraph at the top
	// of this file describes.
	t.Run("plan name carrying a character json.Marshal escapes", func(t *testing.T) {
		const escapedName = "Acme & Sons <Unilevel>"

		oldLine := []byte("name: " + fixturePlanName + "\n")
		newLine := []byte("name: \"" + escapedName + "\"\n")
		withEscapes := bytes.Replace(base, oldLine, newLine, 1)
		require.NotEqual(t, base, withEscapes,
			"the fixture's plan name line moved, so this sub-case substituted nothing and would pass vacuously")

		assertIdentityMatchesAcrossTheWire(t, pipeline, withEscapes, escapedName, fixturePlanVersion, structure)
	})
}

// assertIdentityMatchesAcrossTheWire runs one plan through the real pipeline,
// hashes it on the Go side, then loads it into a live worker and compares the
// identity the worker reports back.
func assertIdentityMatchesAcrossTheWire(t *testing.T, pipeline *config.Pipeline, yamlBytes []byte, wantPlanName string, wantPlanVersion uint32, structure string) {
	t.Helper()

	engineJSON, goHash := hashPipelineOutput(t, pipeline, yamlBytes)

	ctx := context.Background()
	client, err := NewEngineClient(ctx, findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	require.NoError(t, client.LoadPlan(ctx, engineJSON))

	// load_plan on its own leaves no tree, so calculate_unilevel returns
	// STRUCTURE_NOT_FOUND and the identity assertions below never run.
	const (
		rootID = "00000000-0000-0000-0000-000000000001"
		midID  = "00000000-0000-0000-0000-000000000002"
		leafID = "00000000-0000-0000-0000-000000000003"
	)
	require.NoError(t, client.CreateTree(ctx, structure, "unilevel"))
	require.NoError(t, client.AddRoot(ctx, structure, rootID, 100))
	require.NoError(t, client.AddNode(ctx, structure, midID, rootID, rootID, 200))
	require.NoError(t, client.AddNode(ctx, structure, leafID, midID, midID, 300))

	snapshot := DistributorSnapshotDTO{
		Rank:             "Associate",
		PersonalVolume:   100,
		Status:           "active",
		HasOrderInPeriod: true,
	}
	result, err := client.CalculateUnilevel(ctx, CalculateUnilevelRequest{
		StructureName: structure,
		Snapshots: map[string]DistributorSnapshotDTO{
			rootID: snapshot,
			midID:  snapshot,
			leafID: snapshot,
		},
		Volume: []VolumeSourceDTO{{SourceID: leafID, CVAmount: 100}},
	})
	require.NoError(t, err)

	// Identity is attached whether or not anything was earned, so without this
	// the setup could degenerate into a no-op and every assertion below would
	// still pass. The leaf's volume pays its two uplines, at levels 1 and 2.
	require.Len(t, result.Earnings, 2, "this tree pays the leaf's two uplines; a different count means the setup drifted")

	require.Equal(t, goHash, result.Plan.Hash,
		"the worker hashed different bytes than PlanHash did; json.Marshal both compacts and "+
			"HTML-escapes a RawMessage, so stage-5 output must already be compact and escaped")

	// Identity is three fields, not one. A correct hash beside the wrong name
	// means identity was assembled from the wrong source rather than from the
	// plan the worker actually loaded.
	require.Equal(t, wantPlanName, result.Plan.Name)
	require.Equal(t, wantPlanVersion, result.Plan.Version)
}
