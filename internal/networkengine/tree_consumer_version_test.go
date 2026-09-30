package networkengine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleRootAdded_CompensationRestoresTheProjectedVersion(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeUserAlreadyExists},
		position:    &EnginePosition{UserID: posUser, Depth: 3, EnrolledAt: posEnrolled.Unix()},
	}
	c, store := newRootConsumer(tr)
	event := makeEvent(EventTypeRootAdded, rootPayload())

	err := c.HandleEvent(context.Background(), event)

	require.Error(t, err)
	assert.Nil(t, activeRow(t, store, posUser), "the row this call inserted is undone")
	version, found, verr := store.ProjectedVersion(context.Background(), "tree1")
	require.NoError(t, verr)
	assert.True(t, found)
	assert.Equal(t, event.Version-1, version)
}

func TestHandleRootAdded_CompensationRestoresTheVersionBeforeALaterRoot(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeUserAlreadyExists},
		position:    &EnginePosition{UserID: posUser, Depth: 3, EnrolledAt: posEnrolled.Unix()},
	}
	c, store := newRootConsumer(tr)
	ctx := context.Background()
	earlier := "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb"
	require.NoError(t, store.ProjectInsert(ctx, TreeNodeRow{
		ID: "eeeeeeee-eeee-eeee-eeee-000000000001", TreeID: "tree1", UserID: earlier,
		SponsorID: &earlier, EnrolledAt: posEnrolled,
	}, 1))
	require.NoError(t, store.ProjectRemoval(ctx, "tree1", earlier, "eeeeeeee-eeee-eeee-eeee-000000000002", 2, nil))
	event := makeEvent(EventTypeRootAdded, rootPayload())
	event.Version = 3

	err := c.HandleEvent(ctx, event)

	require.Error(t, err)
	assert.Nil(t, activeRow(t, store, posUser), "the row this call inserted is undone")
	version, found, verr := store.ProjectedVersion(ctx, "tree1")
	require.NoError(t, verr)
	assert.True(t, found)
	assert.Equal(t, int64(2), version)
}
