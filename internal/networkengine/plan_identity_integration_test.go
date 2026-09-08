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
// outer struct rewrites it — compacting insignificant whitespace and escaping
// <, > and & as <, > and &.
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

	// Both constants come from minimal-unilevel.yaml, whose own header calls it
	// a starting template for new plans. Renaming the plan there fails the name
	// assertion below; renaming the structure surfaces as STRUCTURE_NOT_FOUND
	// from the Rust worker, with nothing pointing back here.
	const (
		fixturePlanName = "Starter Unilevel"
		structure       = "Primary"
	)

	t.Run("pipeline fixture", func(t *testing.T) {
		assertIdentityMatchesAcrossTheWire(t, pipeline, base, fixturePlanName, structure)
	})

	// The escaping half of that boundary is invisible to every fixture in
	// internal/config/testdata/valid, none of which contains <, > or &. The two
	// sides agree today only because translateToEngine also uses json.Marshal,
	// so the bytes are already escaped before the transport sees them. Moving
	// that to a json.Encoder with SetEscapeHTML(false) is an ordinary-looking
	// refactor that would split the two hashes apart, and without this case
	// nothing in the repo would notice.
	t.Run("plan name carrying a character json.Marshal escapes", func(t *testing.T) {
		const escapedName = "Acme & Sons <Unilevel>"

		oldLine := []byte("name: " + fixturePlanName + "\n")
		newLine := []byte("name: \"" + escapedName + "\"\n")
		withEscapes := bytes.Replace(base, oldLine, newLine, 1)
		require.NotEqual(t, base, withEscapes,
			"the fixture's plan name line moved, so this sub-case substituted nothing and would pass vacuously")

		assertIdentityMatchesAcrossTheWire(t, pipeline, withEscapes, escapedName, structure)
	})
}

// assertIdentityMatchesAcrossTheWire runs one plan through the real pipeline,
// hashes it on the Go side, then loads it into a live worker and compares the
// identity the worker reports back.
func assertIdentityMatchesAcrossTheWire(t *testing.T, pipeline *config.Pipeline, yamlBytes []byte, wantPlanName, structure string) {
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
	// still pass. This tree shape pays two levels.
	require.NotEmpty(t, result.Earnings, "no earnings, so the setup degenerated and proves less than it looks")

	require.Equal(t, goHash, result.Plan.Hash,
		"the worker hashed different bytes than PlanHash did; json.Marshal both compacts and "+
			"HTML-escapes a RawMessage, so stage-5 output must already be compact and escaped")

	// Identity is three fields, not one. A correct hash beside the wrong name
	// means identity was assembled from the wrong source rather than from the
	// plan the worker actually loaded.
	require.Equal(t, wantPlanName, result.Plan.Name)
	require.Equal(t, uint32(1), result.Plan.Version)
}
