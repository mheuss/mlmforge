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

// workerArena is the arena a worker tree snapshot carries.
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

// restoreSetup places X under the root, sponsored by the root, and Y under the
// root, sponsored by X. It then places Z under X and removes it.
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

// removeCheck builds the check for removing user under mode, or remove_node when mode is empty.
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
			assert.Equal(t, edited.Index[writerRoot], *edited.Root, "the root slot")
			assert.Len(t, edited.FreeList, 1, "the tombstone from removing Z")
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
				assert.Equal(t, "", sponsorOf(t, e, restoredTree, writerChild), "X's sponsor after the refusals")
				assert.Equal(t, writerChild, sponsorOf(t, e, restoredTree, writerOther), "Y's sponsor after the refusals")
			}
		})
	}
}
