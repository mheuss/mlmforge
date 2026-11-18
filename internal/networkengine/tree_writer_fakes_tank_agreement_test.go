package networkengine

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// editTank decodes the holding tank in a worker or fake matrix snapshot,
// applies edit, and encodes the result.
func editTank(t *testing.T, data json.RawMessage, edit func(*[]HoldingTankEntryDTO)) json.RawMessage {
	t.Helper()
	var tree map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &tree))
	raw, ok := tree["holding_tank"]
	require.True(t, ok, "the snapshot holds no holding_tank key")
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var tank []HoldingTankEntryDTO
	require.NoError(t, dec.Decode(&tank), "the snapshot's holding tank")
	edit(&tank)
	encoded, err := json.Marshal(tank)
	require.NoError(t, err)
	tree["holding_tank"] = encoded
	out, err := json.Marshal(tree)
	require.NoError(t, err)
	return out
}

// tankOf returns the holding tank in a snapshot of writerTree.
func tankOf(t *testing.T, e removalAgreementEngine) []HoldingTankEntryDTO {
	t.Helper()
	taken, err := e.TakeSnapshot(context.Background(), writerTree)
	require.NoError(t, err)
	var tank []HoldingTankEntryDTO
	editTank(t, taken.Data, func(held *[]HoldingTankEntryDTO) { tank = *held })
	return tank
}

func TestFakeWriterEngine_SnapshotsTheTankTheWorkerSnapshots(t *testing.T) {
	ctx := context.Background()
	client := startWorker(t)
	fake := newFakeWriterEngine()
	for _, e := range []removalAgreementEngine{client, fake} {
		agreementRooted(treeTypeMatrix)(t, e)
		placeInSlot(t, e, treeTypeMatrix, writerChild, writerRoot, writerRoot, 0, 200)
		placeInSlot(t, e, treeTypeMatrix, writerOther, writerRoot, writerChild, 1, 300)
		for _, user := range []string{writerOther, writerChild} {
			_, err := e.RemoveMatrixNode(ctx, writerTree, user, PruningHoldingTank)
			require.NoError(t, err)
		}
	}

	workerTank := tankOf(t, client)

	child, root := writerChild, writerRoot
	require.Equal(t, []HoldingTankEntryDTO{
		{UserID: writerOther, SponsorUserID: &child, EnrolledAt: 300},
		{UserID: writerChild, SponsorUserID: &root, EnrolledAt: 200},
	}, workerTank, "the worker's tank")
	assert.Equal(t, workerTank, tankOf(t, fake))
}

func TestFakeWriterEngine_RefusesATankedUserAfterARestore(t *testing.T) {
	ctx := context.Background()
	client := startWorker(t)
	fake := newFakeWriterEngine()
	for _, e := range []removalAgreementEngine{client, fake} {
		agreementTanked(t, e)
		roundTrip(t, e)
	}

	check := CheckAddNodeAt(writerChild, writerRoot, writerRoot, 0, 300)
	workerCheck := client.CheckMutation(ctx, writerTree, check)
	fakeCheck := fake.CheckMutation(ctx, writerTree, check)
	workerApply := client.AddNodeAt(ctx, writerTree, writerChild, writerRoot, writerRoot, 0, 300)
	fakeApply := fake.AddNodeAt(ctx, writerTree, writerChild, writerRoot, writerRoot, 0, 300)

	require.Equal(t, "USER_IN_HOLDING_TANK", engineCodeOf(workerCheck), "the worker's check answer")
	assert.Equal(t, engineErrorOf(workerCheck), engineErrorOf(workerApply), "the worker's apply against its check")
	assert.Equal(t, engineErrorOf(workerCheck), engineErrorOf(fakeCheck), "check")
	assert.Equal(t, engineErrorOf(workerApply), engineErrorOf(fakeApply), "apply")
}

func TestFakeWriterEngine_RefusesATankedUserAsARestoredRoot(t *testing.T) {
	ctx := context.Background()
	client := startWorker(t)
	fake := newFakeWriterEngine()
	agreementTree(treeTypeMatrix)(t, client)
	agreementTree(treeTypeMatrix)(t, fake)
	workerTaken, err := client.TakeSnapshot(ctx, writerTree)
	require.NoError(t, err)
	fakeTaken, err := fake.TakeSnapshot(ctx, writerTree)
	require.NoError(t, err)
	waiting := HoldingTankEntryDTO{UserID: writerOther, EnrolledAt: 100}
	workerData := editTank(t, workerTaken.Data, func(tank *[]HoldingTankEntryDTO) { *tank = []HoldingTankEntryDTO{waiting} })
	fakeData := editFakeSnapshot(t, fakeTaken.Data, func(s *fakeSnapshot) { s.HoldingTank = []HoldingTankEntryDTO{waiting} })
	require.NoError(t, client.RestoreSnapshot(ctx, restoredTree, workerTaken.TreeType, workerData), "the worker's restore")
	require.NoError(t, fake.RestoreSnapshot(ctx, restoredTree, fakeTaken.TreeType, fakeData))

	check := CheckAddRoot(writerOther, 100)
	workerCheck := client.CheckMutation(ctx, restoredTree, check)
	fakeCheck := fake.CheckMutation(ctx, restoredTree, check)
	workerApply := client.AddRoot(ctx, restoredTree, writerOther, 100)
	fakeApply := fake.AddRoot(ctx, restoredTree, writerOther, 100)

	require.Equal(t, "USER_IN_HOLDING_TANK", engineCodeOf(workerCheck), "the worker's check answer")
	assert.Equal(t, engineErrorOf(workerCheck), engineErrorOf(workerApply), "the worker's apply against its check")
	assert.Equal(t, engineErrorOf(workerCheck), engineErrorOf(fakeCheck), "check")
	assert.Equal(t, engineErrorOf(workerApply), engineErrorOf(fakeApply), "apply")
}

func TestFakeWriterEngine_IgnoresATankKeyOffAMatrixAsTheWorkerDoes(t *testing.T) {
	ctx := context.Background()
	tanks := map[string]string{
		"a list naming the user":  `[{"user_id":"` + writerOther + `","sponsor_user_id":null,"enrolled_at":1}]`,
		"a value that is no list": `"not a tank"`,
	}
	for _, treeType := range []string{treeTypeUnilevel, treeTypeBinary} {
		for name, tank := range tanks {
			t.Run(treeType+" "+name, func(t *testing.T) {
				client := startWorker(t)
				fake := newFakeWriterEngine()
				for _, e := range []removalAgreementEngine{client, fake} {
					agreementRooted(treeType)(t, e)
					taken, err := e.TakeSnapshot(ctx, writerTree)
					require.NoError(t, err)
					var tree map[string]json.RawMessage
					require.NoError(t, json.Unmarshal(taken.Data, &tree))
					tree["holding_tank"] = json.RawMessage(tank)
					data, err := json.Marshal(tree)
					require.NoError(t, err)

					require.NoError(t, e.RestoreSnapshot(ctx, restoredTree, taken.TreeType, data))
					opts := []AddNodeOption{}
					if treeType == treeTypeBinary {
						opts = append(opts, WithPosition(0))
					}
					assert.NoError(t, e.AddNode(ctx, restoredTree, writerOther, writerRoot, writerRoot, 300, opts...))
				}
			})
		}
	}
}
