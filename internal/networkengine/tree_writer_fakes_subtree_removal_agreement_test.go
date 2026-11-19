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
