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

// sponsorlessLayout names where a sponsorless parent's recruits are placed.
type sponsorlessLayout int

const (
	recruitOutside sponsorlessLayout = iota
	recruitUnder
	recruitsMixed
)

// restoreSponsorlessMatrixParent restores a matrix with a sponsorless parent on
// both engines.
func restoreSponsorlessMatrixParent(t *testing.T, client, fake removalAgreementEngine, layout sponsorlessLayout) {
	t.Helper()
	ctx := context.Background()
	removedSponsor, otherParent := writerRoot, writerRoot
	switch layout {
	case recruitUnder:
		otherParent = writerChild
	case recruitsMixed:
		removedSponsor = writerChild
	}
	for _, e := range []removalAgreementEngine{client, fake} {
		agreementRooted(treeTypeMatrix)(t, e)
		placeInSlot(t, e, treeTypeMatrix, writerChild, writerRoot, writerRoot, 0, 200)
		placeInSlot(t, e, treeTypeMatrix, writerRemoved, writerChild, removedSponsor, 0, 300)
		placeInSlot(t, e, treeTypeMatrix, writerOther, otherParent, writerChild, 1, 400)
	}
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
		s.Sponsored[writerRoot] = slices.DeleteFunc(s.Sponsored[writerRoot], func(id string) bool { return id == writerChild })
	})
	require.NoError(t, client.RestoreSnapshot(ctx, restoredTree, workerTaken.TreeType, workerData), "the worker's restore")
	require.NoError(t, fake.RestoreSnapshot(ctx, restoredTree, fakeTaken.TreeType, fakeData))
}

func TestFakeWriterEngine_RefusesASponsorlessMatrixParentBeforeItsChildren(t *testing.T) {
	for _, mode := range []PruningMode{PruningPromoteEarliest, PruningHoldingTank} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := context.Background()
			client := startWorker(t)
			fake := newFakeWriterEngine()
			restoreSponsorlessMatrixParent(t, client, fake, recruitOutside)

			check := removeCheck(writerChild, mode)
			workerCheck := client.CheckMutation(ctx, restoredTree, check)
			fakeCheck := fake.CheckMutation(ctx, restoredTree, check)
			_, workerApply := removeUser(client, restoredTree, writerChild, mode)
			_, fakeApply := removeUser(fake, restoredTree, writerChild, mode)

			require.Equal(t, "SPONSORLESS_WITH_RECRUITS", engineCodeOf(workerCheck), "the worker's check answer")
			assert.Equal(t, engineErrorOf(workerCheck), engineErrorOf(workerApply), "the worker's apply against its check")
			assert.Equal(t, engineErrorOf(workerCheck), engineErrorOf(fakeCheck), "check")
			assert.Equal(t, engineErrorOf(workerApply), engineErrorOf(fakeApply), "apply")
		})
	}
}

func TestFakeWriterEngine_RefusesATankRemovalOfASponsorlessParentAsUnmodelled(t *testing.T) {
	ctx := context.Background()
	client := startWorker(t)
	fake := newFakeWriterEngine()
	restoreSponsorlessMatrixParent(t, client, fake, recruitUnder)

	check := removeCheck(writerChild, PruningHoldingTank)
	workerCheck := client.CheckMutation(ctx, restoredTree, check)
	fakeCheck := fake.CheckMutation(ctx, restoredTree, check)
	_, fakeApply := removeUser(fake, restoredTree, writerChild, PruningHoldingTank)

	require.NoError(t, workerCheck, "the worker's check answer")
	require.EqualError(t, fakeCheck, "fakeWriterEngine removes only a matrix leaf, and "+writerChild+" has children")
	assert.Equal(t, fakeCheck, fakeApply)
}

func TestFakeWriterEngine_CountsOnlyTheRecruitsATankRemovalLeavesBehind(t *testing.T) {
	ctx := context.Background()
	client := startWorker(t)
	fake := newFakeWriterEngine()
	restoreSponsorlessMatrixParent(t, client, fake, recruitsMixed)

	check := removeCheck(writerChild, PruningHoldingTank)
	workerCheck := client.CheckMutation(ctx, restoredTree, check)
	fakeCheck := fake.CheckMutation(ctx, restoredTree, check)
	_, workerApply := removeUser(client, restoredTree, writerChild, PruningHoldingTank)
	_, fakeApply := removeUser(fake, restoredTree, writerChild, PruningHoldingTank)

	require.Equal(t, "SPONSORLESS_WITH_RECRUITS", engineCodeOf(workerCheck), "the worker's check answer")
	require.Contains(t, engineErrorOf(workerCheck), "Recruits that outlive it: 1", "the worker's check answer")
	assert.Equal(t, engineErrorOf(workerCheck), engineErrorOf(workerApply), "the worker's apply against its check")
	assert.Equal(t, engineErrorOf(workerCheck), engineErrorOf(fakeCheck), "check")
	assert.Equal(t, engineErrorOf(workerApply), engineErrorOf(fakeApply), "apply")
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

// setFakeParent sets user's parent in a fake snapshot.
func setFakeParent(s *fakeSnapshot, user, parent string) {
	n := s.Nodes[user]
	n.ParentUserID = &parent
	s.Nodes[user] = n
}

// setFakeDepth sets user's depth in a fake snapshot.
func setFakeDepth(s *fakeSnapshot, user string, depth uint32) {
	n := s.Nodes[user]
	n.Depth = depth
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
		{
			name: "a root sponsored by its own recruit",
			editWorker: func(t *testing.T, a *workerArena) {
				r, x := a.slot(t, writerRoot), a.slot(t, writerChild)
				a.Nodes[r].Sponsor = &x
				a.Nodes[x].Sponsored = append(a.Nodes[x].Sponsored, r)
			},
			editFake: func(s *fakeSnapshot) {
				setFakeSponsor(s, writerRoot, writerChild)
				s.Sponsored[writerChild] = append(s.Sponsored[writerChild], writerRoot)
			},
			workerFault: `^the root at node slot \d+ holds ` + writerRoot + `, whose sponsor is node slot \d+$`,
			fakeMessage: "the root " + writerRoot + " names sponsor " + writerChild,
		},
		{
			name: "a root that sponsors itself",
			editWorker: func(t *testing.T, a *workerArena) {
				r := a.slot(t, writerRoot)
				a.Nodes[r].Sponsor = &r
				a.Nodes[r].Sponsored = append(a.Nodes[r].Sponsored, r)
			},
			editFake: func(s *fakeSnapshot) {
				setFakeSponsor(s, writerRoot, writerRoot)
				s.Sponsored[writerRoot] = append(s.Sponsored[writerRoot], writerRoot)
			},
			workerFault: `^the root at node slot \d+ holds ` + writerRoot + `, whose sponsor is node slot \d+$`,
			fakeMessage: "the root " + writerRoot + " names sponsor " + writerRoot,
		},
		{
			name: "a root sponsor whose recruit list omits the root",
			editWorker: func(t *testing.T, a *workerArena) {
				x := a.slot(t, writerChild)
				a.Nodes[a.slot(t, writerRoot)].Sponsor = &x
			},
			editFake:    func(s *fakeSnapshot) { setFakeSponsor(s, writerRoot, writerChild) },
			workerFault: `^the root at node slot \d+ holds ` + writerRoot + `, whose sponsor is node slot \d+$`,
			fakeMessage: "the root " + writerRoot + " names sponsor " + writerChild,
		},
		{
			name: "a root sponsor naming an absent node",
			editWorker: func(t *testing.T, a *workerArena) {
				tomb := a.tombstone(t)
				a.Nodes[a.slot(t, writerRoot)].Sponsor = &tomb
			},
			editFake:    func(s *fakeSnapshot) { setFakeSponsor(s, writerRoot, writerRemoved) },
			workerFault: `^sponsor on node slot \d+ names node slot \d+; that slot is a tombstone$`,
			fakeMessage: "sponsor on node " + writerRoot + " names " + writerRemoved + "; the snapshot holds no such node",
		},
		{
			name: "two non-root nodes that sponsor each other",
			editWorker: func(t *testing.T, a *workerArena) {
				r, x, y := a.slot(t, writerRoot), a.slot(t, writerChild), a.slot(t, writerOther)
				a.Nodes[x].Sponsor = &y
				a.Nodes[y].Sponsored = []int{x}
				a.Nodes[r].Sponsored = slices.DeleteFunc(a.Nodes[r].Sponsored, func(s int) bool { return s == x })
			},
			editFake: func(s *fakeSnapshot) {
				setFakeSponsor(s, writerChild, writerOther)
				s.Sponsored[writerOther] = []string{writerChild}
				delete(s.Sponsored, writerRoot)
			},
			workerFault: `^the sponsor chain from node slot \d+ reaches node slot \d+ twice$`,
			fakeMessage: "the sponsor chain from " + writerChild + " reaches " + writerChild + " twice",
		},
		{
			name: "a non-root node that sponsors itself",
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
		{
			name: "a sponsor chain that enters a cycle",
			editWorker: func(t *testing.T, a *workerArena) {
				r, x, y := a.slot(t, writerRoot), a.slot(t, writerChild), a.slot(t, writerOther)
				a.Nodes[x].Sponsor = &y
				a.Nodes[x].Sponsored = []int{}
				a.Nodes[y].Sponsor = &y
				a.Nodes[y].Sponsored = []int{y, x}
				a.Nodes[r].Sponsored = slices.DeleteFunc(a.Nodes[r].Sponsored, func(s int) bool { return s == x })
			},
			editFake: func(s *fakeSnapshot) {
				setFakeSponsor(s, writerChild, writerOther)
				setFakeSponsor(s, writerOther, writerOther)
				s.Sponsored[writerOther] = []string{writerOther, writerChild}
				delete(s.Sponsored, writerChild)
				delete(s.Sponsored, writerRoot)
			},
			workerFault: `^the sponsor chain from node slot \d+ reaches node slot \d+ twice$`,
			fakeMessage: "the sponsor chain from " + writerChild + " reaches " + writerOther + " twice",
		},
		{
			name: "a sponsor cycle whose recruit lists were not updated",
			editWorker: func(t *testing.T, a *workerArena) {
				y := a.slot(t, writerOther)
				a.Nodes[a.slot(t, writerChild)].Sponsor = &y
			},
			editFake:    func(s *fakeSnapshot) { setFakeSponsor(s, writerChild, writerOther) },
			workerFault: `^sponsored on node slot \d+ names node slot \d+; sponsor on node slot \d+ does not name node slot \d+$`,
			fakeMessage: "the recruit list of " + writerRoot + " names " + writerChild + ", whose sponsor is " + writerOther,
		},
		{
			name: "a root parented on its own child",
			editWorker: func(t *testing.T, a *workerArena) {
				r, x := a.slot(t, writerRoot), a.slot(t, writerChild)
				a.Nodes[r].Parent = &x
				a.Nodes[x].Children = append(a.Nodes[x].Children, r)
			},
			editFake:    func(s *fakeSnapshot) { setFakeParent(s, writerRoot, writerChild) },
			workerFault: `^node slot \d+ holds ` + writerRoot + ` at depth 0; its parent at node slot \d+ is at depth 1$`,
			fakeMessage: "node " + writerRoot + " is at depth 0; its parent " + writerChild + " is at depth 1",
		},
		{
			name: "two non-root nodes parented on each other",
			editWorker: func(t *testing.T, a *workerArena) {
				r, x, y := a.slot(t, writerRoot), a.slot(t, writerChild), a.slot(t, writerOther)
				a.Nodes[x].Parent = &y
				a.Nodes[y].Parent = &x
				a.Nodes[x].Children = []int{y}
				a.Nodes[y].Children = []int{x}
				a.Nodes[r].Children = slices.DeleteFunc(a.Nodes[r].Children, func(c int) bool { return c == x || c == y })
			},
			editFake: func(s *fakeSnapshot) {
				setFakeParent(s, writerChild, writerOther)
				setFakeParent(s, writerOther, writerChild)
			},
			workerFault: `^node slot \d+ holds ` + writerChild + ` at depth 1; its parent at node slot \d+ is at depth 1$`,
			fakeMessage: "node " + writerChild + " is at depth 1; its parent " + writerOther + " is at depth 1",
		},
		{
			name: "a root parented on and sponsored by its own child",
			editWorker: func(t *testing.T, a *workerArena) {
				r, x := a.slot(t, writerRoot), a.slot(t, writerChild)
				a.Nodes[r].Parent = &x
				a.Nodes[x].Children = append(a.Nodes[x].Children, r)
				a.Nodes[r].Sponsor = &x
				a.Nodes[x].Sponsored = append(a.Nodes[x].Sponsored, r)
			},
			editFake: func(s *fakeSnapshot) {
				setFakeParent(s, writerRoot, writerChild)
				setFakeSponsor(s, writerRoot, writerChild)
				s.Sponsored[writerChild] = append(s.Sponsored[writerChild], writerRoot)
			},
			workerFault: `^node slot \d+ holds ` + writerRoot + ` at depth 0; its parent at node slot \d+ is at depth 1$`,
			fakeMessage: "node " + writerRoot + " is at depth 0; its parent " + writerChild + " is at depth 1",
		},
		{
			name: "a root at a depth other than 0",
			editWorker: func(t *testing.T, a *workerArena) {
				a.Nodes[a.slot(t, writerRoot)].Depth = 1
				a.Nodes[a.slot(t, writerChild)].Depth = 2
				a.Nodes[a.slot(t, writerOther)].Depth = 2
			},
			editFake: func(s *fakeSnapshot) {
				setFakeDepth(s, writerRoot, 1)
				setFakeDepth(s, writerChild, 2)
				setFakeDepth(s, writerOther, 2)
			},
			workerFault: `^node slot \d+ holds ` + writerRoot + ` at depth 1 and names no parent$`,
			fakeMessage: "node " + writerRoot + " is at depth 1 and names no parent",
		},
		{
			name:        "a node whose depth skips a level",
			editWorker:  func(t *testing.T, a *workerArena) { a.Nodes[a.slot(t, writerOther)].Depth = 5 },
			editFake:    func(s *fakeSnapshot) { setFakeDepth(s, writerOther, 5) },
			workerFault: `^node slot \d+ holds ` + writerOther + ` at depth 5; its parent at node slot \d+ is at depth 0$`,
			fakeMessage: "node " + writerOther + " is at depth 5; its parent " + writerRoot + " is at depth 0",
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
