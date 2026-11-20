package networkengine

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// subtreePlacement is one placement in a removal layout.
type subtreePlacement struct {
	user, parent, sponsor string
	slot                  int
	enrolledAt            int64
}

// placeLayout places each user in layout into writerTree on e, in order.
func placeLayout(t *testing.T, e agreementEngine, layout []subtreePlacement) {
	t.Helper()
	for _, p := range layout {
		placeInSlot(t, e, treeTypeMatrix, p.user, p.parent, p.sponsor, p.slot, p.enrolledAt)
	}
}

// restoreWithoutSponsor restores both engines' snapshots of writerTree as
// restoredTree, with user's link to sponsor cut on both of its sides.
func restoreWithoutSponsor(t *testing.T, client, fake removalAgreementEngine, user, sponsor string) {
	t.Helper()
	ctx := context.Background()
	workerTaken, err := client.TakeSnapshot(ctx, writerTree)
	require.NoError(t, err)
	fakeTaken, err := fake.TakeSnapshot(ctx, writerTree)
	require.NoError(t, err)
	workerData := editWorkerSnapshot(t, workerTaken.Data, func(a *workerArena) {
		u, s := a.slot(t, user), a.slot(t, sponsor)
		a.Nodes[u].Sponsor = nil
		a.Nodes[s].Sponsored = slices.DeleteFunc(a.Nodes[s].Sponsored, func(i int) bool { return i == u })
	})
	fakeData := editFakeSnapshot(t, fakeTaken.Data, func(s *fakeSnapshot) {
		n := s.Nodes[user]
		n.SponsorUserID = nil
		s.Nodes[user] = n
		s.Sponsored[sponsor] = slices.DeleteFunc(s.Sponsored[sponsor], func(id string) bool { return id == user })
	})
	require.NoError(t, client.RestoreSnapshot(ctx, restoredTree, workerTaken.TreeType, workerData), "the worker's restore")
	require.NoError(t, fake.RestoreSnapshot(ctx, restoredTree, fakeTaken.TreeType, fakeData), "the fake's restore")
}

func TestFakeWriterEngine_RefusesATankRemovalInTheWorkersOrder(t *testing.T) {
	outsideX := testUserUUID(50)
	base := []subtreePlacement{
		{writerChild, writerRoot, writerRoot, 0, 200},
		{writerOther, writerChild, writerChild, 0, 300},
		{writerRemoved, writerRoot, writerOther, 1, 400},
	}
	for _, row := range []struct {
		name   string
		layout []subtreePlacement
		named  string
	}{
		{"a descendant whose sponsor chain ends in the tank", base, writerOther},
		{"the removed user before a descendant", append(slices.Clone(base),
			subtreePlacement{outsideX, writerRoot, writerChild, 2, 500}), writerChild},
	} {
		t.Run(row.name, func(t *testing.T) {
			ctx := context.Background()
			client := startWorker(t)
			fake := newFakeWriterEngine()
			for _, e := range []removalAgreementEngine{client, fake} {
				agreementRooted(treeTypeMatrix)(t, e)
				placeLayout(t, e, row.layout)
			}
			restoreWithoutSponsor(t, client, fake, writerChild, writerRoot)

			check := removeCheck(writerChild, PruningHoldingTank)
			workerCheck := client.CheckMutation(ctx, restoredTree, check)
			fakeCheck := fake.CheckMutation(ctx, restoredTree, check)
			_, workerApply := removeUser(client, restoredTree, writerChild, PruningHoldingTank)
			_, fakeApply := removeUser(fake, restoredTree, writerChild, PruningHoldingTank)

			require.Equal(t, "SPONSORLESS_WITH_RECRUITS: cannot remove user "+row.named+
				": no node in its sponsor chain survives this removal. Recruits that outlive it: 1",
				engineErrorOf(workerCheck), "the worker's check answer")
			assert.Equal(t, engineErrorOf(workerCheck), engineErrorOf(workerApply), "the worker's apply against its check")
			assert.Equal(t, engineErrorOf(workerCheck), engineErrorOf(fakeCheck), "check")
			assert.Equal(t, engineErrorOf(workerApply), engineErrorOf(fakeApply), "apply")
			for _, placed := range row.layout {
				assert.Equal(t, sponsorOf(t, client, restoredTree, placed.user), sponsorOf(t, fake, restoredTree, placed.user),
					"the sponsor of %s after the refusals", placed.user)
			}
		})
	}
}

func TestFakeWriterEngine_RefusesATankRemovalNamingASponsorlessDescendant(t *testing.T) {
	ctx := context.Background()
	client := startWorker(t)
	fake := newFakeWriterEngine()
	layout := []subtreePlacement{
		{writerChild, writerRoot, writerRoot, 0, 200},
		{writerOther, writerChild, writerChild, 0, 300},
		{writerRemoved, writerRoot, writerOther, 1, 400},
	}
	for _, e := range []removalAgreementEngine{client, fake} {
		agreementRooted(treeTypeMatrix)(t, e)
		placeLayout(t, e, layout)
	}
	restoreWithoutSponsor(t, client, fake, writerOther, writerChild)

	check := removeCheck(writerChild, PruningHoldingTank)
	workerCheck := client.CheckMutation(ctx, restoredTree, check)
	fakeCheck := fake.CheckMutation(ctx, restoredTree, check)
	_, workerApply := removeUser(client, restoredTree, writerChild, PruningHoldingTank)
	_, fakeApply := removeUser(fake, restoredTree, writerChild, PruningHoldingTank)

	require.Equal(t, "SPONSORLESS_WITH_RECRUITS: cannot remove user "+writerOther+
		": no node in its sponsor chain survives this removal. Recruits that outlive it: 1",
		engineErrorOf(workerCheck), "the worker's check answer")
	assert.Equal(t, engineErrorOf(workerCheck), engineErrorOf(workerApply), "the worker's apply against its check")
	assert.Equal(t, engineErrorOf(workerCheck), engineErrorOf(fakeCheck), "check")
	assert.Equal(t, engineErrorOf(workerApply), engineErrorOf(fakeApply), "apply")
	for _, e := range []removalAgreementEngine{client, fake} {
		assert.Equal(t, "", sponsorOf(t, e, restoredTree, writerOther), "writerOther's sponsor after the refusals")
		assert.Equal(t, writerOther, sponsorOf(t, e, restoredTree, writerRemoved), "writerRemoved's sponsor after the refusals")
	}
}

var (
	tankS  = testUserUUID(51)
	tankT  = testUserUUID(52)
	tankX  = testUserUUID(53)
	tankG  = testUserUUID(54)
	tankC1 = testUserUUID(55)
	tankC0 = testUserUUID(56)
	tankO1 = testUserUUID(57)
	tankO2 = testUserUUID(58)
	tankO3 = testUserUUID(59)
)

// tankSubtreeLayout places X under S with C0 and C1 below it and G below C0.
// O1, O2 and O3 sit outside X's subtree, recruited by G, C1 and X.
var tankSubtreeLayout = []subtreePlacement{
	{tankS, writerRoot, writerRoot, 0, 200},
	{tankT, writerRoot, writerRoot, 1, 210},
	{tankX, tankS, tankS, 0, 300},
	{tankC1, tankX, tankT, 1, 400},
	{tankC0, tankX, tankX, 0, 600},
	{tankG, tankC0, tankC0, 0, 500},
	{tankO3, tankS, tankX, 1, 700},
	{tankO1, tankT, tankG, 0, 710},
	{tankO2, tankT, tankC1, 1, 720},
}

// workerLists returns the recruit map from the worker's snapshot of writerTree,
// leaving out users with no recruits.
func workerLists(t *testing.T, client *EngineClient) map[string][]string {
	t.Helper()
	taken, err := client.TakeSnapshot(context.Background(), writerTree)
	require.NoError(t, err)
	lists := map[string][]string{}
	editWorkerSnapshot(t, taken.Data, func(a *workerArena) {
		for slot, node := range a.Nodes {
			if slices.Contains(a.FreeList, slot) {
				continue
			}
			for _, recruit := range node.Sponsored {
				lists[node.UserID] = append(lists[node.UserID], a.Nodes[recruit].UserID)
			}
		}
	})
	return lists
}

// placement is where a node sits: its parent, slot and depth.
type placement struct {
	parent string
	slot   int
	depth  uint32
}

// placementOf returns user's placement in writerTree on e.
func placementOf(t *testing.T, e removalAgreementEngine, user string) placement {
	t.Helper()
	pos, err := e.GetPosition(context.Background(), writerTree, user)
	require.NoError(t, err, "the position of %s", user)
	p := placement{slot: pos.Position, depth: pos.Depth}
	if pos.ParentUserID != nil {
		p.parent = *pos.ParentUserID
	}
	return p
}

func TestFakeWriterEngine_TanksASubtreeAsTheWorkerDoes(t *testing.T) {
	ctx := context.Background()
	client := startWorker(t)
	fake := newFakeWriterEngine()
	for _, e := range []removalAgreementEngine{client, fake} {
		agreementRooted(treeTypeMatrix)(t, e)
		placeLayout(t, e, tankSubtreeLayout)
	}

	check := removeCheck(tankX, PruningHoldingTank)
	workerCheck := client.CheckMutation(ctx, writerTree, check)
	fakeCheck := fake.CheckMutation(ctx, writerTree, check)
	workerRes, workerErr := client.RemoveMatrixNode(ctx, writerTree, tankX, PruningHoldingTank)
	fakeRes, fakeErr := fake.RemoveMatrixNode(ctx, writerTree, tankX, PruningHoldingTank)

	require.NoError(t, workerCheck, "the worker's check answer")
	require.NoError(t, workerErr, "the worker's answer")
	require.Equal(t, MatrixRemovalResult{
		Removed:      tankX,
		Repositioned: []string{},
		MovedToTank:  []string{tankC0, tankC1, tankG, tankX},
		Relocated:    []Relocation{},
		Responsored: []Responsored{
			{UserID: tankO3, NewSponsorID: tankS},
			{UserID: tankO2, NewSponsorID: tankT},
			{UserID: tankO1, NewSponsorID: tankS},
		},
	}, *workerRes, "the worker's result")
	require.NoError(t, fakeCheck, "the fake's check answer")
	require.NoError(t, fakeErr)
	assert.Equal(t, *workerRes, *fakeRes, "result")

	x, s, t1, c0 := tankX, tankS, tankT, tankC0
	workerTank := tankOf(t, client)
	require.Equal(t, []HoldingTankEntryDTO{
		{UserID: tankC0, SponsorUserID: &x, EnrolledAt: 600},
		{UserID: tankC1, SponsorUserID: &t1, EnrolledAt: 400},
		{UserID: tankG, SponsorUserID: &c0, EnrolledAt: 500},
		{UserID: tankX, SponsorUserID: &s, EnrolledAt: 300},
	}, workerTank, "the worker's tank")
	assert.Equal(t, workerTank, tankOf(t, fake), "tank")

	workerRecruits := workerLists(t, client)
	require.Equal(t, map[string][]string{
		writerRoot: {tankS, tankT},
		tankS:      {tankO3, tankO1},
		tankT:      {tankO2},
	}, workerRecruits, "the worker's recruit lists")
	assert.Equal(t, workerRecruits, fakeLists(t, fake), "recruit lists")

	for _, user := range []string{writerRoot, tankS, tankT, tankO1, tankO2, tankO3} {
		assert.Equal(t, placementOf(t, client, user), placementOf(t, fake, user), "the placement of %s", user)
		assert.Equal(t, sponsorOf(t, client, writerTree, user), sponsorOf(t, fake, writerTree, user), "the sponsor of %s", user)
	}
	for _, user := range []string{tankX, tankC0, tankC1, tankG} {
		_, workerPos := client.GetPosition(ctx, writerTree, user)
		_, fakePos := fake.GetPosition(ctx, writerTree, user)
		require.Error(t, workerPos, "the worker's position of tanked user %s", user)
		assert.Equal(t, engineErrorOf(workerPos), engineErrorOf(fakePos), "the position of tanked user %s", user)
		replace := CheckAddNodeAt(user, writerRoot, writerRoot, 2, 900)
		workerPlace := client.CheckMutation(ctx, writerTree, replace)
		require.Error(t, workerPlace, "the worker's check of placing tanked user %s", user)
		assert.Equal(t, engineErrorOf(workerPlace), engineErrorOf(fake.CheckMutation(ctx, writerTree, replace)),
			"placing tanked user %s", user)
	}
	requireHeldSponsors(t, fake)
	roundTrip(t, fake)
}
