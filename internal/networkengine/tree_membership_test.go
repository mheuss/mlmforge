package networkengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// membershipEngine is a fake holding root and one child.
func membershipEngine(t *testing.T) *fakeWriterEngine {
	t.Helper()
	engine := newFakeWriterEngine()
	ctx := context.Background()
	require.NoError(t, engine.CreateTree(ctx, writerTree, treeTypeUnilevel))
	require.NoError(t, engine.AddRoot(ctx, writerTree, writerRoot, writeTime.Unix()))
	require.NoError(t, engine.AddNode(ctx, writerTree, writerChild, writerRoot, writerRoot, writeTime.Unix()))
	return engine
}

func memberRow(user string, depth int, enrolled time.Time) TreeNodeRow {
	return TreeNodeRow{TreeID: writerTree, UserID: user, Depth: depth, EnrolledAt: enrolled}
}

func TestCheckMembership_MatchesAnEngineThatHoldsTheRows(t *testing.T) {
	rows := []TreeNodeRow{memberRow(writerRoot, 0, writeTime), memberRow(writerChild, 1, writeTime)}

	got, err := checkMembership(context.Background(), membershipEngine(t), writerTree, rows)

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestCheckMembership_ComparesEnrolmentInWholeSeconds(t *testing.T) {
	fractional := writeTime.Add(400 * time.Millisecond)
	rows := []TreeNodeRow{memberRow(writerRoot, 0, fractional), memberRow(writerChild, 1, fractional)}

	got, err := checkMembership(context.Background(), membershipEngine(t), writerTree, rows)

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestCheckMembership_NamesWhatEachSideHoldsAlone(t *testing.T) {
	rows := []TreeNodeRow{memberRow(writerRoot, 0, writeTime), memberRow(writerOther, 1, writeTime)}

	got, err := checkMembership(context.Background(), membershipEngine(t), writerTree, rows)

	require.NoError(t, err)
	assert.Equal(t, "the engine holds 2 nodes under root "+writerRoot+"; the store holds 2 active rows; "+
		"first user IDs only in the engine: ["+writerChild+"]; only in the store: ["+writerOther+"]; "+
		"depth or enrolled_at differs for: []", got)
}

func TestCheckMembership_NamesADepthOrEnrolmentDifference(t *testing.T) {
	rows := []TreeNodeRow{memberRow(writerRoot, 0, writeTime), memberRow(writerChild, 1, writeTime.Add(time.Hour))}

	got, err := checkMembership(context.Background(), membershipEngine(t), writerTree, rows)

	require.NoError(t, err)
	assert.Contains(t, got, "depth or enrolled_at differs for: ["+writerChild+"]")
}

func TestCheckMembership_ListsAtMostFiveIDsInSortedOrder(t *testing.T) {
	rows := []TreeNodeRow{memberRow(writerRoot, 0, writeTime), memberRow(writerChild, 1, writeTime)}
	for i := 9; i >= 3; i-- {
		rows = append(rows, memberRow(testUserUUID(100+i), 1, writeTime))
	}

	got, err := checkMembership(context.Background(), membershipEngine(t), writerTree, rows)

	require.NoError(t, err)
	assert.Contains(t, got, "only in the store: ["+testUserUUID(103)+", "+testUserUUID(104)+", "+
		testUserUUID(105)+", "+testUserUUID(106)+", "+testUserUUID(107)+"]")
}

func TestCheckMembership_ReturnsAFailedQuery(t *testing.T) {
	engine := membershipEngine(t)
	engine.downlineErr = errors.New("transport is closed")
	rows := []TreeNodeRow{memberRow(writerRoot, 0, writeTime)}

	_, err := checkMembership(context.Background(), engine, writerTree, rows)

	assert.EqualError(t, err, "transport is closed")
}
