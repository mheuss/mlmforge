package networkengine

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// editTank decodes the holding tank in a matrix snapshot that holds a
// holding_tank key, applies edit, and encodes the result.
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

// tankOf returns the holding tank in a snapshot of structure.
func tankOf(t *testing.T, e removalAgreementEngine, structure string) []HoldingTankEntryDTO {
	t.Helper()
	taken, err := e.TakeSnapshot(context.Background(), structure)
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

	workerTank := tankOf(t, client, writerTree)

	child, root := writerChild, writerRoot
	require.Equal(t, []HoldingTankEntryDTO{
		{UserID: writerOther, SponsorUserID: &child, EnrolledAt: 300},
		{UserID: writerChild, SponsorUserID: &root, EnrolledAt: 200},
	}, workerTank, "the worker's tank")
	assert.Equal(t, workerTank, tankOf(t, fake, writerTree))
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

var tankLoop = testUserUUID(10)

// tankEntry builds a tank entry for user, sponsored by sponsor when it is not empty.
func tankEntry(user, sponsor string) HoldingTankEntryDTO {
	entry := HoldingTankEntryDTO{UserID: user, EnrolledAt: 500}
	if sponsor != "" {
		entry.SponsorUserID = &sponsor
	}
	return entry
}

// tankShape is one tank, and an optional arena edit, that a restore must answer.
type tankShape struct {
	name        string
	tank        []HoldingTankEntryDTO
	editWorker  func(*testing.T, *workerArena)
	editFake    func(*fakeSnapshot)
	workerFault string
	fakeMessage string
}

func tankShapes() []tankShape {
	return []tankShape{
		{
			name:        "a tanked user who is also placed",
			tank:        []HoldingTankEntryDTO{tankEntry(writerChild, writerRoot)},
			workerFault: `^the holding tank names ` + writerChild + `; that user is also placed at node slot \d+$`,
			fakeMessage: "the holding tank names " + writerChild + "; the snapshot also places that user",
		},
		{
			name:        "a user listed twice",
			tank:        []HoldingTankEntryDTO{tankEntry(writerRemoved, ""), tankEntry(writerRemoved, "")},
			workerFault: `^the holding tank lists ` + writerRemoved + ` more than once$`,
			fakeMessage: "the holding tank lists " + writerRemoved + " more than once",
		},
		{
			name:        "a placed user listed twice",
			tank:        []HoldingTankEntryDTO{tankEntry(writerChild, ""), tankEntry(writerChild, "")},
			workerFault: `^the holding tank names ` + writerChild + `; that user is also placed at node slot \d+$`,
			fakeMessage: "the holding tank names " + writerChild + "; the snapshot also places that user",
		},
		{
			name:        "a tank entry that sponsors itself",
			tank:        []HoldingTankEntryDTO{tankEntry(writerRemoved, writerRemoved)},
			workerFault: `^the holding tank's sponsor chain from ` + writerRemoved + ` reaches ` + writerRemoved + ` twice$`,
			fakeMessage: "the holding tank's sponsor chain from " + writerRemoved + " reaches " + writerRemoved + " twice",
		},
		{
			name:        "two tank entries that sponsor each other",
			tank:        []HoldingTankEntryDTO{tankEntry(writerRemoved, tankLoop), tankEntry(tankLoop, writerRemoved)},
			workerFault: `^the holding tank's sponsor chain from ` + writerRemoved + ` reaches ` + writerRemoved + ` twice$`,
			fakeMessage: "the holding tank's sponsor chain from " + writerRemoved + " reaches " + writerRemoved + " twice",
		},
		{
			name:        "a tank chain that enters a cycle",
			tank:        []HoldingTankEntryDTO{tankEntry(writerRemoved, tankLoop), tankEntry(tankLoop, tankLoop)},
			workerFault: `^the holding tank's sponsor chain from ` + writerRemoved + ` reaches ` + tankLoop + ` twice$`,
			fakeMessage: "the holding tank's sponsor chain from " + writerRemoved + " reaches " + tankLoop + " twice",
		},
		{
			name: "a tank cycle found from a later entry",
			tank: []HoldingTankEntryDTO{tankEntry(writerRemoved, ""), tankEntry(tankLoop, testUserUUID(11)),
				tankEntry(testUserUUID(11), tankLoop)},
			workerFault: `^the holding tank's sponsor chain from ` + tankLoop + ` reaches ` + tankLoop + ` twice$`,
			fakeMessage: "the holding tank's sponsor chain from " + tankLoop + " reaches " + tankLoop + " twice",
		},
		{
			name:        "two placed users in the tank",
			tank:        []HoldingTankEntryDTO{tankEntry(writerChild, ""), tankEntry(writerOther, "")},
			workerFault: `^the holding tank names ` + writerChild + `; that user is also placed at node slot \d+$`,
			fakeMessage: "the holding tank names " + writerChild + "; the snapshot also places that user",
		},
		{
			name: "two users listed twice",
			tank: []HoldingTankEntryDTO{tankEntry(writerRemoved, ""), tankEntry(tankLoop, ""),
				tankEntry(writerRemoved, ""), tankEntry(tankLoop, "")},
			workerFault: `^the holding tank lists ` + writerRemoved + ` more than once$`,
			fakeMessage: "the holding tank lists " + writerRemoved + " more than once",
		},
		{
			name: "two users listed twice, the second repeated first",
			tank: []HoldingTankEntryDTO{tankEntry(writerRemoved, ""), tankEntry(tankLoop, ""),
				tankEntry(tankLoop, ""), tankEntry(writerRemoved, "")},
			workerFault: `^the holding tank lists ` + tankLoop + ` more than once$`,
			fakeMessage: "the holding tank lists " + tankLoop + " more than once",
		},
		{
			name: "a user listed twice beside a tank cycle",
			tank: []HoldingTankEntryDTO{tankEntry(writerRemoved, ""), tankEntry(writerRemoved, tankLoop),
				tankEntry(tankLoop, writerRemoved)},
			workerFault: `^the holding tank lists ` + writerRemoved + ` more than once$`,
			fakeMessage: "the holding tank lists " + writerRemoved + " more than once",
		},
		{
			name: "a tank fault beside a root with a sponsor",
			tank: []HoldingTankEntryDTO{tankEntry(writerRemoved, ""), tankEntry(writerRemoved, "")},
			editWorker: func(t *testing.T, a *workerArena) {
				x := a.slot(t, writerChild)
				a.Nodes[a.slot(t, writerRoot)].Sponsor = &x
			},
			editFake:    func(s *fakeSnapshot) { setFakeSponsor(s, writerRoot, writerChild) },
			workerFault: `^the root at node slot \d+ holds ` + writerRoot + `, whose sponsor is node slot \d+$`,
			fakeMessage: "the root " + writerRoot + " names sponsor " + writerChild,
		},
		{
			name: "a tank fault beside a placed node that sponsors itself",
			tank: []HoldingTankEntryDTO{tankEntry(writerRemoved, ""), tankEntry(writerRemoved, "")},
			editWorker: func(t *testing.T, a *workerArena) {
				y := a.slot(t, writerOther)
				a.Nodes[y].Sponsor = &y
				a.Nodes[y].Sponsored = []int{y}
				a.Nodes[a.slot(t, writerChild)].Sponsored = []int{}
			},
			editFake: func(s *fakeSnapshot) {
				setFakeSponsor(s, writerOther, writerOther)
				s.Sponsored[writerOther] = []string{writerOther}
				delete(s.Sponsored, writerChild)
			},
			workerFault: `^the sponsor chain from node slot \d+ reaches node slot \d+ twice$`,
			fakeMessage: "the sponsor chain from " + writerOther + " reaches " + writerOther + " twice",
		},
	}
}

// tankRestoreSetup builds a rooted matrix with two placed users.
func tankRestoreSetup(t *testing.T, e agreementEngine) {
	t.Helper()
	agreementRooted(treeTypeMatrix)(t, e)
	placeInSlot(t, e, treeTypeMatrix, writerChild, writerRoot, writerRoot, 0, 200)
	placeInSlot(t, e, treeTypeMatrix, writerOther, writerRoot, writerChild, 1, 300)
}

// restoreWithTank restores both engines' snapshots with the shape's tank and
// arena edits applied.
func restoreWithTank(t *testing.T, client, fake removalAgreementEngine, shape tankShape) (workerErr, fakeErr error) {
	t.Helper()
	ctx := context.Background()
	workerTaken, err := client.TakeSnapshot(ctx, writerTree)
	require.NoError(t, err)
	fakeTaken, err := fake.TakeSnapshot(ctx, writerTree)
	require.NoError(t, err)
	workerData := editTank(t, workerTaken.Data, func(held *[]HoldingTankEntryDTO) { *held = shape.tank })
	if shape.editWorker != nil {
		workerData = editWorkerSnapshot(t, workerData, func(a *workerArena) { shape.editWorker(t, a) })
	}
	fakeData := editFakeSnapshot(t, fakeTaken.Data, func(s *fakeSnapshot) {
		s.HoldingTank = shape.tank
		if shape.editFake != nil {
			shape.editFake(s)
		}
	})
	return client.RestoreSnapshot(ctx, restoredTree, workerTaken.TreeType, workerData),
		fake.RestoreSnapshot(ctx, restoredTree, fakeTaken.TreeType, fakeData)
}

func TestFakeWriterEngine_RefusesTheTanksTheWorkerRefuses(t *testing.T) {
	for _, shape := range tankShapes() {
		t.Run(shape.name, func(t *testing.T) {
			client := startWorker(t)
			fake := newFakeWriterEngine()
			tankRestoreSetup(t, client)
			tankRestoreSetup(t, fake)

			workerErr, fakeErr := restoreWithTank(t, client, fake, shape)

			var workerRefusal *EngineError
			require.ErrorAs(t, workerErr, &workerRefusal, "the worker's answer")
			require.Equal(t, "INCONSISTENT_SNAPSHOT", workerRefusal.Code, "the worker's code")
			require.Regexp(t, shape.workerFault, workerRefusal.Message, "the worker's fault")
			requireEngineRefusal(t, fakeErr, "INCONSISTENT_SNAPSHOT", shape.fakeMessage)
			assert.NotContains(t, fake.trees, restoredTree)
		})
	}
}

func TestFakeWriterEngine_RestoresTankChainsThatEndAsTheWorkerDoes(t *testing.T) {
	shape := tankShape{tank: []HoldingTankEntryDTO{
		tankEntry(writerRemoved, tankLoop),
		tankEntry(tankLoop, writerChild),
		tankEntry(writerAbsentSponsor, testUserUUID(11)),
		tankEntry(testUserUUID(12), ""),
		tankEntry(testUserUUID(13), tankLoop),
	}}
	client := startWorker(t)
	fake := newFakeWriterEngine()
	tankRestoreSetup(t, client)
	tankRestoreSetup(t, fake)

	workerErr, fakeErr := restoreWithTank(t, client, fake, shape)

	require.NoError(t, workerErr, "the worker's restore")
	require.NoError(t, fakeErr)
}

func TestFakeWriterEngine_WritesTheHoldingTankKeyAsTheWorkerDoes(t *testing.T) {
	ctx := context.Background()
	for _, row := range []struct {
		name      string
		treeType  string
		tankChild bool
		want      string
	}{
		{"unilevel", treeTypeUnilevel, false, ""},
		{"binary", treeTypeBinary, false, ""},
		{"matrix with an empty tank", treeTypeMatrix, false, "[]"},
		{"matrix with one tanked user", treeTypeMatrix, true,
			`[{"user_id":"00000000-0000-0000-0000-000000000002","sponsor_user_id":"00000000-0000-0000-0000-000000000001","enrolled_at":200}]`},
	} {
		t.Run(row.name, func(t *testing.T) {
			client := startWorker(t)
			fake := newFakeWriterEngine()
			for _, e := range []removalAgreementEngine{client, fake} {
				agreementWithChild(row.treeType)(t, e)
				if row.tankChild {
					_, err := e.RemoveMatrixNode(ctx, writerTree, writerChild, PruningHoldingTank)
					require.NoError(t, err)
				}
			}
			assert.Equal(t, row.want, tankKeyOf(t, client, writerTree), "the worker's holding_tank")
			assert.Equal(t, row.want, tankKeyOf(t, fake, writerTree), "the fake's holding_tank")
		})
	}
}

// tankKeyOf returns the raw holding_tank value in e's snapshot of structure, or "" when the key is absent.
func tankKeyOf(t *testing.T, e removalAgreementEngine, structure string) string {
	t.Helper()
	taken, err := e.TakeSnapshot(context.Background(), structure)
	require.NoError(t, err)
	var tree map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(taken.Data, &tree))
	return string(tree["holding_tank"])
}
