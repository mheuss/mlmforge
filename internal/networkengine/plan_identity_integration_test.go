package networkengine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mlmforge/mlmforge/internal/config"
)

// The plan identity the worker reports has to be the identity PlanHash
// computes over the same plan. Both sides hash sha256 over "the plan bytes",
// but they observe those bytes at different points: PlanHash takes stage-5
// pipeline output directly, while the worker takes what arrives in
// Request.params. Between the two, transport_stdio wraps the plan in a
// protocolRequest whose Params is a json.RawMessage, and json.Marshal on the
// outer struct compacts it.
//
// A same-slice test cannot see that. It hashes one buffer twice and passes
// whatever the marshal boundary does to the bytes in between. This drives a
// real worker over stdio so the comparison spans the boundary that could
// break it.
func TestPlanHashMatchesWorkerAcrossTheWire(t *testing.T) {
	root := filepath.Join("..", "..")

	pipeline, err := config.NewPipeline(filepath.Join(root, "schemas", "compensation-plan.schema.json"))
	require.NoError(t, err)

	yamlBytes, err := os.ReadFile(filepath.Join(root, "internal", "config", "testdata", "valid", "minimal-unilevel.yaml"))
	require.NoError(t, err)

	engineJSON, verrs, err := pipeline.LoadAndValidate(yamlBytes)
	require.NoError(t, err)
	for _, ve := range verrs {
		require.NotEqual(t, config.SeverityError, ve.Severity, "fixture stopped validating: %+v", ve)
	}

	goHash, err := PlanHash(engineJSON)
	require.NoError(t, err)

	ctx := context.Background()
	client, err := NewEngineClient(ctx, findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	require.NoError(t, client.LoadPlan(ctx, engineJSON))

	// load_plan on its own leaves no tree, so calculate_unilevel returns
	// STRUCTURE_NOT_FOUND and the identity assertions below never run.
	const (
		structure = "Primary"
		rootID    = "00000000-0000-0000-0000-000000000001"
		midID     = "00000000-0000-0000-0000-000000000002"
		leafID    = "00000000-0000-0000-0000-000000000003"
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

	require.Equal(t, goHash, result.Plan.Hash,
		"the worker hashed different bytes than PlanHash did; json.Marshal compacts a "+
			"RawMessage, so stage-5 output must already be compact")

	// Identity is three fields, not one. A correct hash beside the wrong name
	// means identity was assembled from the wrong source rather than from the
	// plan the worker actually loaded.
	require.Equal(t, "Starter Unilevel", result.Plan.Name)
	require.Equal(t, uint32(1), result.Plan.Version)
}
