package networkengine

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	removalA = testUserUUID(31)
	removalB = testUserUUID(32)
	removalC = testUserUUID(33)
)

// removalAgreementEngine adds position, snapshot and drop calls to agreementEngine.
type removalAgreementEngine interface {
	agreementEngine
	GetPosition(ctx context.Context, structure, userID string) (*EnginePosition, error)
	TakeSnapshot(ctx context.Context, structure string) (*SnapshotResultDTO, error)
	RestoreSnapshot(ctx context.Context, structure, treeType string, data json.RawMessage) error
	DropStructure(ctx context.Context, structure string) (bool, error)
}

// removalTreeTypes pairs each tree type with the pruning mode used to remove from it.
var removalTreeTypes = []struct {
	name     string
	treeType string
	mode     PruningMode
}{
	{"unilevel", treeTypeUnilevel, ""},
	{"binary", treeTypeBinary, ""},
	{"matrix promote_earliest", treeTypeMatrix, PruningPromoteEarliest},
	{"matrix holding_tank", treeTypeMatrix, PruningHoldingTank},
}

// startWorker starts a worker process for one test.
func startWorker(t *testing.T) *EngineClient {
	t.Helper()
	binary := findWorkerBinary(t)
	client, err := NewEngineClient(context.Background(), binary)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Stop() })
	return client
}

// placeInSlot places user under parent in writerTree, at slot where the tree type has slots.
func placeInSlot(t *testing.T, e agreementEngine, treeType, user, parent, sponsor string, slot int, enrolledAt int64) {
	t.Helper()
	ctx := context.Background()
	switch treeType {
	case treeTypeMatrix:
		require.NoError(t, e.AddNodeAt(ctx, writerTree, user, parent, sponsor, slot, enrolledAt))
	case treeTypeBinary:
		require.NoError(t, e.AddNode(ctx, writerTree, user, parent, sponsor, enrolledAt, WithPosition(slot)))
	default:
		require.NoError(t, e.AddNode(ctx, writerTree, user, parent, sponsor, enrolledAt))
	}
}

// removeUser removes user from structure under mode.
func removeUser(e agreementEngine, structure, user string, mode PruningMode) ([]Responsored, error) {
	ctx := context.Background()
	if mode == "" {
		return e.RemoveNode(ctx, structure, user)
	}
	res, err := e.RemoveMatrixNode(ctx, structure, user, mode)
	if err != nil {
		return nil, err
	}
	return res.Responsored, nil
}

// sponsorOf returns user's sponsor in structure, or "" when it has none.
func sponsorOf(t *testing.T, e removalAgreementEngine, structure, user string) string {
	t.Helper()
	pos, err := e.GetPosition(context.Background(), structure, user)
	require.NoError(t, err)
	if pos.SponsorUserID == nil {
		return ""
	}
	return *pos.SponsorUserID
}

// roundTrip takes a snapshot of writerTree, drops it, and restores it.
func roundTrip(t *testing.T, e removalAgreementEngine) {
	t.Helper()
	ctx := context.Background()
	taken, err := e.TakeSnapshot(ctx, writerTree)
	require.NoError(t, err)
	_, err = e.DropStructure(ctx, writerTree)
	require.NoError(t, err)
	require.NoError(t, e.RestoreSnapshot(ctx, writerTree, taken.TreeType, taken.Data))
}

// requireHeldSponsors requires every sponsor in the fake's writerTree to name a node it holds.
func requireHeldSponsors(t *testing.T, fake *fakeWriterEngine) {
	t.Helper()
	nodes := fake.trees[writerTree].nodes
	for _, id := range slices.Sorted(maps.Keys(nodes)) {
		if sponsor := nodes[id].SponsorUserID; sponsor != nil {
			require.Contains(t, nodes, *sponsor, "the sponsor of %s", id)
		}
	}
}

// singleRemovalSetup places A under the root, sponsored by the root, and B and
// C sponsored by A, leaving A with no children.
func singleRemovalSetup(t *testing.T, e agreementEngine, treeType string) {
	t.Helper()
	agreementRooted(treeType)(t, e)
	placeInSlot(t, e, treeType, removalA, writerRoot, writerRoot, 0, 200)
	placeInSlot(t, e, treeType, removalB, writerRoot, removalA, 1, 300)
	if treeType == treeTypeBinary {
		placeInSlot(t, e, treeType, removalC, removalB, removalA, 0, 400)
		return
	}
	placeInSlot(t, e, treeType, removalC, writerRoot, removalA, 2, 400)
}

func TestFakeWriterEngine_ResponsorsAsTheWorkerDoes(t *testing.T) {
	for _, tt := range removalTreeTypes {
		t.Run(tt.name, func(t *testing.T) {
			client := startWorker(t)
			fake := newFakeWriterEngine()
			singleRemovalSetup(t, client, tt.treeType)
			singleRemovalSetup(t, fake, tt.treeType)

			workerMoved, workerErr := removeUser(client, writerTree, removalA, tt.mode)
			fakeMoved, fakeErr := removeUser(fake, writerTree, removalA, tt.mode)

			require.NoError(t, workerErr, "the worker's answer")
			require.Equal(t, []Responsored{
				{UserID: removalB, NewSponsorID: writerRoot},
				{UserID: removalC, NewSponsorID: writerRoot},
			}, workerMoved, "the worker's responsored list")
			require.NoError(t, fakeErr)
			assert.Equal(t, workerMoved, fakeMoved, "responsored")
			for _, user := range []string{writerRoot, removalB, removalC} {
				assert.Equal(t, sponsorOf(t, client, writerTree, user), sponsorOf(t, fake, writerTree, user),
					"the sponsor of %s", user)
			}
			requireHeldSponsors(t, fake)
			roundTrip(t, fake)
		})
	}
}

var (
	removalM = testUserUUID(34)
	removalP = testUserUUID(35)
	removalF = testUserUUID(36)
)

// orderScenario sponsors M by the root, A by M, B by A and P by M, placed in
// that order, with A and M left without children.
func orderScenario(t *testing.T, e agreementEngine, treeType string) {
	t.Helper()
	agreementRooted(treeType)(t, e)
	switch treeType {
	case treeTypeBinary:
		placeInSlot(t, e, treeType, removalF, writerRoot, writerRoot, 0, 150)
		placeInSlot(t, e, treeType, removalM, writerRoot, writerRoot, 1, 200)
		placeInSlot(t, e, treeType, removalA, removalF, removalM, 0, 300)
		placeInSlot(t, e, treeType, removalB, removalF, removalA, 1, 400)
		placeInSlot(t, e, treeType, removalP, removalB, removalM, 0, 500)
	case treeTypeMatrix:
		placeInSlot(t, e, treeType, removalM, writerRoot, writerRoot, 0, 200)
		placeInSlot(t, e, treeType, removalA, writerRoot, removalM, 1, 300)
		placeInSlot(t, e, treeType, removalB, writerRoot, removalA, 2, 400)
		placeInSlot(t, e, treeType, removalP, removalB, removalM, 0, 500)
	default:
		placeInSlot(t, e, treeType, removalM, writerRoot, writerRoot, 0, 200)
		placeInSlot(t, e, treeType, removalA, writerRoot, removalM, 0, 300)
		placeInSlot(t, e, treeType, removalB, writerRoot, removalA, 0, 400)
		placeInSlot(t, e, treeType, removalP, writerRoot, removalM, 0, 500)
	}
}

// orderScenarioLists is the recruit map the test expects from the fake after A and then M are removed.
func orderScenarioLists(treeType string) map[string][]string {
	if treeType == treeTypeBinary {
		return map[string][]string{writerRoot: {removalF, removalP, removalB}}
	}
	return map[string][]string{writerRoot: {removalP, removalB}}
}

// fakeLists returns the recruit map from the fake's snapshot of structure.
func fakeLists(t *testing.T, fake *fakeWriterEngine, structure string) map[string][]string {
	t.Helper()
	taken, err := fake.TakeSnapshot(context.Background(), structure)
	require.NoError(t, err)
	var snap fakeSnapshot
	require.NoError(t, json.Unmarshal(taken.Data, &snap))
	return snap.Sponsored
}

func TestFakeWriterEngine_KeepsTheWorkersRecruitOrderAcrossTwoRemovals(t *testing.T) {
	for _, restore := range []bool{false, true} {
		for _, tt := range removalTreeTypes {
			name := tt.name
			if restore {
				name += " with a restore between"
			}
			t.Run(name, func(t *testing.T) {
				client := startWorker(t)
				fake := newFakeWriterEngine()
				const worker, fakeIdx = 0, 1
				engines := []removalAgreementEngine{worker: client, fakeIdx: fake}
				names := []string{worker: "worker", fakeIdx: "fake"}
				var first, second [2][]Responsored
				for i, e := range engines {
					orderScenario(t, e, tt.treeType)
					var err error
					first[i], err = removeUser(e, writerTree, removalA, tt.mode)
					require.NoError(t, err, "removing A on the %s", names[i])
					if restore {
						roundTrip(t, e)
					}
					second[i], err = removeUser(e, writerTree, removalM, tt.mode)
					require.NoError(t, err, "removing M on the %s", names[i])
				}

				require.Equal(t, []Responsored{{UserID: removalB, NewSponsorID: removalM}}, first[worker],
					"the worker's responsored list on removing A")
				require.Equal(t, []Responsored{
					{UserID: removalP, NewSponsorID: writerRoot},
					{UserID: removalB, NewSponsorID: writerRoot},
				}, second[worker], "the worker's responsored list on removing M")
				assert.Equal(t, first[worker], first[fakeIdx], "responsored on removing A")
				assert.Equal(t, second[worker], second[fakeIdx], "responsored on removing M")
				for _, user := range []string{removalA, removalM} {
					_, workerErr := client.GetPosition(context.Background(), writerTree, user)
					_, fakeErr := fake.GetPosition(context.Background(), writerTree, user)
					require.Error(t, workerErr, "the worker's position of removed user %s", user)
					assert.Equal(t, engineErrorOf(workerErr), engineErrorOf(fakeErr), "the position of removed user %s", user)
				}
				if !restore {
					assert.Equal(t, orderScenarioLists(tt.treeType), fakeLists(t, fake, writerTree))
				}
				survivors := []string{writerRoot, removalB, removalP}
				if tt.treeType == treeTypeBinary {
					survivors = append(survivors, removalF)
				}
				for _, user := range survivors {
					assert.Equal(t, sponsorOf(t, client, writerTree, user), sponsorOf(t, fake, writerTree, user),
						"the sponsor of %s", user)
				}
				requireHeldSponsors(t, fake)
				roundTrip(t, fake)
			})
		}
	}
}
