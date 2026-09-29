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
