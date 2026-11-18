package networkengine

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// restoredTree is the structure an edited snapshot is restored as.
var restoredTree = testTreeUUID(2)

// workerArena is the JSON shape of the arena in a worker tree snapshot.
type workerArena struct {
	Nodes    []workerNode   `json:"nodes"`
	Index    map[string]int `json:"index"`
	FreeList []int          `json:"free_list"`
	Root     *int           `json:"root"`
}

// workerNode is one slot of a worker arena.
type workerNode struct {
	UserID     string `json:"user_id"`
	Parent     *int   `json:"parent"`
	Children   []int  `json:"children"`
	Sponsor    *int   `json:"sponsor"`
	Sponsored  []int  `json:"sponsored"`
	Depth      uint32 `json:"depth"`
	EnrolledAt int64  `json:"enrolled_at"`
}

// slot returns the arena slot the worker's index gives user.
func (a *workerArena) slot(t *testing.T, user string) int {
	t.Helper()
	slot, ok := a.Index[user]
	require.True(t, ok, "the worker's index holds no entry for %s", user)
	return slot
}

// editWorkerSnapshot decodes the arena in a worker snapshot, applies edit, and encodes the result.
func editWorkerSnapshot(t *testing.T, data json.RawMessage, edit func(*workerArena)) json.RawMessage {
	t.Helper()
	var tree map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &tree))
	dec := json.NewDecoder(bytes.NewReader(tree["arena"]))
	dec.DisallowUnknownFields()
	var arena workerArena
	require.NoError(t, dec.Decode(&arena), "the worker's arena")
	edit(&arena)
	encoded, err := json.Marshal(arena)
	require.NoError(t, err)
	tree["arena"] = encoded
	out, err := json.Marshal(tree)
	require.NoError(t, err)
	return out
}

// editFakeSnapshot decodes a fake snapshot, applies edit, and encodes the result.
func editFakeSnapshot(t *testing.T, data json.RawMessage, edit func(*fakeSnapshot)) json.RawMessage {
	t.Helper()
	var snap fakeSnapshot
	require.NoError(t, json.Unmarshal(data, &snap))
	edit(&snap)
	out, err := json.Marshal(snap)
	require.NoError(t, err)
	return out
}

// restoreSetup places writerChild under the root, sponsored by the root, and
// writerOther under the root, sponsored by writerChild. It then places
// writerRemoved under writerChild and removes it.
func restoreSetup(t *testing.T, e agreementEngine, treeType string) {
	t.Helper()
	agreementRooted(treeType)(t, e)
	placeInSlot(t, e, treeType, writerChild, writerRoot, writerRoot, 0, 200)
	placeInSlot(t, e, treeType, writerOther, writerRoot, writerChild, 1, 300)
	placeInSlot(t, e, treeType, writerRemoved, writerChild, writerRoot, 0, 400)
	mode := PruningMode("")
	if treeType == treeTypeMatrix {
		mode = PruningPromoteEarliest
	}
	_, err := removeUser(e, writerTree, writerRemoved, mode)
	require.NoError(t, err)
}

// removeCheck builds the check for removing user under mode.
func removeCheck(user string, mode PruningMode) Mutation {
	if mode == "" {
		return CheckRemoveNode(user)
	}
	return CheckRemoveMatrixNode(user, mode)
}

func TestWorkerSnapshotEditor_RoundTripsTheWorkersArena(t *testing.T) {
	ctx := context.Background()
	for _, treeType := range []string{treeTypeUnilevel, treeTypeBinary, treeTypeMatrix} {
		t.Run(treeType, func(t *testing.T) {
			client := startWorker(t)
			restoreSetup(t, client, treeType)
			taken, err := client.TakeSnapshot(ctx, writerTree)
			require.NoError(t, err)
			var tree map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(taken.Data, &tree))
			require.Contains(t, tree, "arena", "the worker's snapshot keys")

			var edited workerArena
			data := editWorkerSnapshot(t, taken.Data, func(a *workerArena) { edited = *a })

			require.NotNil(t, edited.Root, "the worker's root slot")
			assert.Equal(t, edited.slot(t, writerRoot), *edited.Root, "the root slot")
			assert.Len(t, edited.FreeList, 1, "the tombstone from removing writerRemoved")
			require.NoError(t, client.RestoreSnapshot(ctx, restoredTree, taken.TreeType, data), "the worker's restore")
		})
	}
}

func TestFakeWriterEngine_RefusesToRemoveASponsorlessUserWithRecruits(t *testing.T) {
	ctx := context.Background()
	for _, tt := range removalTreeTypes {
		t.Run(tt.name, func(t *testing.T) {
			client := startWorker(t)
			fake := newFakeWriterEngine()
			restoreSetup(t, client, tt.treeType)
			restoreSetup(t, fake, tt.treeType)
			workerTaken, err := client.TakeSnapshot(ctx, writerTree)
			require.NoError(t, err)
			fakeTaken, err := fake.TakeSnapshot(ctx, writerTree)
			require.NoError(t, err)
			workerData := editWorkerSnapshot(t, workerTaken.Data, func(a *workerArena) {
				x, r := a.slot(t, writerChild), a.slot(t, writerRoot)
				a.Nodes[x].Sponsor = nil
				a.Nodes[r].Sponsored = slices.DeleteFunc(a.Nodes[r].Sponsored, func(s int) bool { return s == x })
			})
			fakeData := editFakeSnapshot(t, fakeTaken.Data, func(s *fakeSnapshot) {
				n := s.Nodes[writerChild]
				n.SponsorUserID = nil
				s.Nodes[writerChild] = n
				delete(s.Sponsored, writerRoot)
			})
			require.NoError(t, client.RestoreSnapshot(ctx, restoredTree, workerTaken.TreeType, workerData),
				"the worker's restore")
			require.NoError(t, fake.RestoreSnapshot(ctx, restoredTree, fakeTaken.TreeType, fakeData))

			check := removeCheck(writerChild, tt.mode)
			workerCheck := client.CheckMutation(ctx, restoredTree, check)
			fakeCheck := fake.CheckMutation(ctx, restoredTree, check)
			_, workerApply := removeUser(client, restoredTree, writerChild, tt.mode)
			_, fakeApply := removeUser(fake, restoredTree, writerChild, tt.mode)

			require.Equal(t, "SPONSORLESS_WITH_RECRUITS", engineCodeOf(workerCheck), "the worker's check answer")
			assert.Equal(t, engineErrorOf(workerCheck), engineErrorOf(workerApply), "the worker's apply against its check")
			assert.Equal(t, engineErrorOf(workerCheck), engineErrorOf(fakeCheck), "check")
			assert.Equal(t, engineErrorOf(workerApply), engineErrorOf(fakeApply), "apply")
			for _, e := range []removalAgreementEngine{client, fake} {
				assert.Equal(t, "", sponsorOf(t, e, restoredTree, writerChild), "writerChild's sponsor after the refusals")
				assert.Equal(t, writerChild, sponsorOf(t, e, restoredTree, writerOther),
					"writerOther's sponsor after the refusals")
			}
		})
	}
}

// tombstone returns the one slot on the worker's free list.
func (a *workerArena) tombstone(t *testing.T) int {
	t.Helper()
	require.Len(t, a.FreeList, 1, "the worker's free list")
	return a.FreeList[0]
}

// setFakeSponsor sets user's sponsor in a fake snapshot.
func setFakeSponsor(s *fakeSnapshot, user, sponsor string) {
	n := s.Nodes[user]
	n.SponsorUserID = &sponsor
	s.Nodes[user] = n
}

// restoreShape pairs one worker edit with one fake edit.
type restoreShape struct {
	name        string
	editWorker  func(*testing.T, *workerArena)
	editFake    func(*fakeSnapshot)
	workerFault string
	fakeMessage string
}

// restoreShapes lists edits that make a worker or fake snapshot inconsistent.
func restoreShapes() []restoreShape {
	return []restoreShape{
		{
			name: "a sponsor naming an absent node",
			editWorker: func(t *testing.T, a *workerArena) {
				tomb := a.tombstone(t)
				a.Nodes[a.slot(t, writerChild)].Sponsor = &tomb
			},
			editFake:    func(s *fakeSnapshot) { setFakeSponsor(s, writerChild, writerRemoved) },
			workerFault: `^sponsor on node slot \d+ names node slot \d+; that slot is a tombstone$`,
			fakeMessage: "sponsor on node " + writerChild + " names " + writerRemoved + "; the snapshot holds no such node",
		},
		{
			name: "a recruit list entry naming an absent user",
			editWorker: func(t *testing.T, a *workerArena) {
				r := a.slot(t, writerRoot)
				a.Nodes[r].Sponsored = append(a.Nodes[r].Sponsored, a.tombstone(t))
			},
			editFake:    func(s *fakeSnapshot) { s.Sponsored[writerRoot] = append(s.Sponsored[writerRoot], writerRemoved) },
			workerFault: `^sponsored on node slot \d+ names node slot \d+; that slot is a tombstone$`,
			fakeMessage: "the recruit list of " + writerRoot + " names " + writerRemoved + "; the snapshot holds no such node",
		},
		{
			name: "a recruit list owned by an absent user",
			editWorker: func(t *testing.T, a *workerArena) {
				a.Nodes[a.tombstone(t)].Sponsored = []int{a.slot(t, writerOther)}
			},
			editFake:    func(s *fakeSnapshot) { s.Sponsored[writerRemoved] = []string{writerOther} },
			workerFault: `^node slot \d+ is a tombstone and still holds a sponsored node$`,
			fakeMessage: "a recruit list belongs to " + writerRemoved + "; the snapshot holds no such node",
		},
		{
			name: "a non-root node with no parent",
			editWorker: func(t *testing.T, a *workerArena) {
				y, r := a.slot(t, writerOther), a.slot(t, writerRoot)
				a.Nodes[y].Parent = nil
				a.Nodes[y].Depth = 0
				a.Nodes[r].Children = slices.DeleteFunc(a.Nodes[r].Children, func(c int) bool { return c == y })
			},
			editFake: func(s *fakeSnapshot) {
				n := s.Nodes[writerOther]
				n.ParentUserID = nil
				n.Depth = 0
				s.Nodes[writerOther] = n
			},
			workerFault: `^node slot \d+ holds ` + writerOther + ` and names no parent; root names node slot \d+$`,
			fakeMessage: "node " + writerOther + " names no parent and is not the root " + writerRoot,
		},
		{
			name:        "nodes with no root",
			editWorker:  func(_ *testing.T, a *workerArena) { a.Root = nil },
			editFake:    func(s *fakeSnapshot) { s.Root = "" },
			workerFault: `^the arena names no root; its live node count is 3$`,
			fakeMessage: "the snapshot holds 3 nodes and names no root",
		},
		{
			name: "a recruit list naming another sponsor's recruit",
			editWorker: func(t *testing.T, a *workerArena) {
				r := a.slot(t, writerRoot)
				a.Nodes[r].Sponsored = append(a.Nodes[r].Sponsored, a.slot(t, writerOther))
			},
			editFake:    func(s *fakeSnapshot) { s.Sponsored[writerRoot] = append(s.Sponsored[writerRoot], writerOther) },
			workerFault: `^sponsored on node slot \d+ names node slot \d+; sponsor on node slot \d+ does not name node slot \d+$`,
			fakeMessage: "the recruit list of " + writerRoot + " names " + writerOther + ", whose sponsor is " + writerChild,
		},
		{
			name: "a recruit list naming one user twice",
			editWorker: func(t *testing.T, a *workerArena) {
				y := a.slot(t, writerOther)
				a.Nodes[a.slot(t, writerChild)].Sponsored = []int{y, y}
			},
			editFake:    func(s *fakeSnapshot) { s.Sponsored[writerChild] = []string{writerOther, writerOther} },
			workerFault: `^sponsored on node slot \d+ names node slot \d+ more than once$`,
			fakeMessage: "the recruit list of " + writerChild + " names " + writerOther + " more than once",
		},
		{
			name:        "a node its sponsor's list omits",
			editWorker:  func(t *testing.T, a *workerArena) { a.Nodes[a.slot(t, writerChild)].Sponsored = []int{} },
			editFake:    func(s *fakeSnapshot) { delete(s.Sponsored, writerChild) },
			workerFault: `^sponsor on node slot \d+ names node slot \d+; sponsored on node slot \d+ does not name node slot \d+$`,
			fakeMessage: writerOther + " names sponsor " + writerChild + "; the recruit list of " + writerChild +
				" does not name it",
		},
		{
			name:       "a recruit list naming a user with no sponsor",
			editWorker: func(t *testing.T, a *workerArena) { a.Nodes[a.slot(t, writerChild)].Sponsor = nil },
			editFake: func(s *fakeSnapshot) {
				n := s.Nodes[writerChild]
				n.SponsorUserID = nil
				s.Nodes[writerChild] = n
			},
			workerFault: `^sponsored on node slot \d+ names node slot \d+; sponsor on node slot \d+ does not name node slot \d+$`,
			fakeMessage: "the recruit list of " + writerRoot + " names " + writerChild + ", who has no sponsor",
		},
	}
}

func TestFakeWriterEngine_RefusesTheSnapshotsTheWorkerRefuses(t *testing.T) {
	ctx := context.Background()
	for _, shape := range restoreShapes() {
		for _, treeType := range []string{treeTypeUnilevel, treeTypeBinary, treeTypeMatrix} {
			t.Run(shape.name+" "+treeType, func(t *testing.T) {
				client := startWorker(t)
				fake := newFakeWriterEngine()
				restoreSetup(t, client, treeType)
				restoreSetup(t, fake, treeType)
				workerTaken, err := client.TakeSnapshot(ctx, writerTree)
				require.NoError(t, err)
				fakeTaken, err := fake.TakeSnapshot(ctx, writerTree)
				require.NoError(t, err)

				workerErr := client.RestoreSnapshot(ctx, restoredTree, workerTaken.TreeType,
					editWorkerSnapshot(t, workerTaken.Data, func(a *workerArena) { shape.editWorker(t, a) }))
				fakeErr := fake.RestoreSnapshot(ctx, restoredTree, fakeTaken.TreeType,
					editFakeSnapshot(t, fakeTaken.Data, shape.editFake))

				var workerRefusal *EngineError
				require.ErrorAs(t, workerErr, &workerRefusal, "the worker's answer")
				require.Equal(t, "INCONSISTENT_SNAPSHOT", workerRefusal.Code, "the worker's code")
				require.Regexp(t, shape.workerFault, workerRefusal.Message, "the worker's fault")
				requireEngineRefusal(t, fakeErr, "INCONSISTENT_SNAPSHOT", shape.fakeMessage)
				assert.NotContains(t, fake.trees, restoredTree)
			})
		}
	}
}
