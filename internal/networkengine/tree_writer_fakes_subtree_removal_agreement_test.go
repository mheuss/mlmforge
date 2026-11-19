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
	workerTaken, err := client.TakeSnapshot(ctx, writerTree)
	require.NoError(t, err)
	fakeTaken, err := fake.TakeSnapshot(ctx, writerTree)
	require.NoError(t, err)
	workerData := editWorkerSnapshot(t, workerTaken.Data, func(a *workerArena) {
		g, x := a.slot(t, writerOther), a.slot(t, writerChild)
		a.Nodes[g].Sponsor = nil
		a.Nodes[x].Sponsored = slices.DeleteFunc(a.Nodes[x].Sponsored, func(s int) bool { return s == g })
	})
	fakeData := editFakeSnapshot(t, fakeTaken.Data, func(s *fakeSnapshot) {
		n := s.Nodes[writerOther]
		n.SponsorUserID = nil
		s.Nodes[writerOther] = n
		delete(s.Sponsored, writerChild)
	})
	require.NoError(t, client.RestoreSnapshot(ctx, restoredTree, workerTaken.TreeType, workerData), "the worker's restore")
	require.NoError(t, fake.RestoreSnapshot(ctx, restoredTree, fakeTaken.TreeType, fakeData))

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
