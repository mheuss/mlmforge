package networkengine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// userReadStore answers GetNode for one user with a scripted row and error,
// and counts those reads. Every other read goes to the inner store.
type userReadStore struct {
	*MemoryTreeStore
	user  string
	row   *TreeNodeRow
	err   error
	reads int
}

func (s *userReadStore) GetNode(ctx context.Context, treeID, userID string) (*TreeNodeRow, error) {
	if userID != s.user {
		return s.MemoryTreeStore.GetNode(ctx, treeID, userID)
	}
	s.reads++
	return s.row, s.err
}

func newUserReadConsumer(t *testing.T, tr *reconcileTransport, store *userReadStore) *TreeEventConsumer {
	t.Helper()
	seedParent(t, store.MemoryTreeStore)
	c := NewTreeEventConsumer(store, newEngineClientWithTransport(tr))
	c.retryDelay = 0
	return c
}

func TestHandleNodePlaced_ReconcileRetriesWhenTheRowReadFails(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeUserAlreadyExists, Message: "taken"},
		position:    enginePlaced(nil),
	}
	store := &userReadStore{MemoryTreeStore: NewMemoryTreeStore(), user: posUser, err: errors.New("row-read-marker")}
	c := newUserReadConsumer(t, tr, store)

	err := deliver(context.Background(), c, makeEvent(EventTypeNodePlaced, placedPayload(treeTypeBinary, intPtr(1))))

	require.Error(t, err)
	assert.Len(t, tr.mutationOps, c.maxRetries+1, "add calls made")
	assert.Equal(t, c.maxRetries+1, store.reads, "reads of the user's row")
	assert.Equal(t, 0, tr.positionOps, "get_position calls made")
	assert.True(t, isEngineCode(err, engineCodeUserAlreadyExists), "got %v", err)
}

func TestHandleNodePlaced_ReconcileDivergesWhenTheStoreHasNoActiveRow(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeUserAlreadyExists, Message: "taken"},
		position:    enginePlaced(nil),
	}
	store := &userReadStore{MemoryTreeStore: NewMemoryTreeStore(), user: posUser}
	c := newUserReadConsumer(t, tr, store)
	event := makeEvent(EventTypeNodePlaced, placedPayload(treeTypeBinary, intPtr(1)))

	err := deliver(context.Background(), c, event)

	require.EqualError(t, err, fmt.Sprintf(
		"engine holds %s in tree %s and the store has no active row for them; event %s",
		posUser, consumerTree, event.ID))
	assert.Len(t, tr.mutationOps, 1, "add calls made")
	assert.Equal(t, 0, tr.positionOps, "get_position calls made")
}

// undoFailingStore fails every UndoRootProjection with err, whatever the
// context.
type undoFailingStore struct {
	*MemoryTreeStore
	err      error
	attempts int
}

func (s *undoFailingStore) UndoRootProjection(context.Context, string, string, int64) error {
	s.attempts++
	return s.err
}

func TestHandleRootAdded_ReconcileNamesAFailedCompensatingDelete(t *testing.T) {
	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeRootAlreadyExists},
		position:    &EnginePosition{UserID: posUser, Depth: 3, EnrolledAt: posEnrolled.Unix()},
	}
	undoErr := errors.New("undo-marker")
	store := &undoFailingStore{MemoryTreeStore: NewMemoryTreeStore(), err: undoErr}
	c := NewTreeEventConsumer(store, newEngineClientWithTransport(tr))
	c.retryDelay = 0

	err := deliver(context.Background(), c, makeEvent(EventTypeRootAdded, rootPayload()))

	require.EqualError(t, err, fmt.Sprintf(
		"engine refused add_root in tree %s, get_position put %s at depth 3 enrolled %d, against %d in this event, "+
			"and deleting the row this event inserted failed: undo-marker",
		consumerTree, posUser, posEnrolled.Unix(), posEnrolled.Unix()))
	assert.ErrorIs(t, err, undoErr)
	assert.Len(t, tr.mutationOps, 1, "add_root calls made")
	assert.Equal(t, 1, store.attempts, "UndoRootProjection calls made")
	assert.NotNil(t, activeRow(t, store.MemoryTreeStore, posUser), "active row for "+posUser)
}

// readBackFailingStore fails every GetNodeIncludingRemoved with err.
type readBackFailingStore struct {
	*MemoryTreeStore
	err   error
	reads int
}

func (s *readBackFailingStore) GetNodeIncludingRemoved(context.Context, string, string) (*TreeNodeRow, error) {
	s.reads++
	return nil, s.err
}

func TestHandleRootAdded_CancelledInsertReportsAFailedReadBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr := &reconcileTransport{
		mutationErr: &EngineError{Code: engineCodeRootAlreadyExists},
		onMutation:  cancel,
	}
	store := &readBackFailingStore{MemoryTreeStore: NewMemoryTreeStore(), err: errors.New("read-back-marker")}
	c := NewTreeEventConsumer(store, newEngineClientWithTransport(tr))
	c.retryDelay = time.Minute

	err := deliver(ctx, c, makeEvent(EventTypeRootAdded, rootPayload()))

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled, "context.Canceled in the error chain")
	assert.Regexp(t, "; reading back the row for "+posUser+" in tree "+consumerTree+" failed: read-back-marker$",
		err.Error(), "end of the returned error")
	assert.Equal(t, 1, store.reads, "GetNodeIncludingRemoved calls made")
}
