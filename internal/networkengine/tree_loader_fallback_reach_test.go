package networkengine_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The labels treeLoadFallback puts in front of a message it built itself. A
// message the loader produced must not start with either.
var fallbackLabels = []string{"tree load rejected", "tree load incomplete"}

// failingMutator fails the first engine call it is asked to make. The loader
// needs a TreeMutator to reach its post-create exits.
type failingMutator struct{}

func (failingMutator) CreateTree(context.Context, string, string) error {
	return errors.New("worker said no")
}
func (failingMutator) CreateMatrixTree(context.Context, string, int, string) error {
	return errors.New("worker said no")
}
func (failingMutator) AddRoot(context.Context, string, string, int64) error {
	return errors.New("worker said no")
}
func (failingMutator) AddNode(context.Context, string, string, string, string, int64, ...networkengine.AddNodeOption) error {
	return errors.New("worker said no")
}
func (failingMutator) AddNodeAt(context.Context, string, string, string, string, int, int64) error {
	return errors.New("worker said no")
}
func (failingMutator) RemoveNode(context.Context, string, string) ([]networkengine.Responsored, error) {
	return nil, errors.New("worker said no")
}

// The fallback is safe only because every value this package constructs sets
// its message. This drives real failures through LoadTree and asserts none of
// them renders a fallback label.
func TestLoadTree_RealFailuresNeverRenderTheFallback(t *testing.T) {
	root := func(treeID, userID string) networkengine.TreeNodeRow {
		return networkengine.TreeNodeRow{
			ID: userID, TreeID: treeID, UserID: userID, Depth: 0,
			EnrolledAt: time.Unix(1, 0),
		}
	}

	tests := []struct {
		name     string
		treeType string
		rows     []networkengine.TreeNodeRow
		engine   networkengine.TreeMutator
		opts     []networkengine.LoadTreeOption
	}{
		{
			name:     "unsupported tree type, rejected before the store read",
			treeType: "unsupported-type",
		},
		{
			name:     "matrix without params, rejected before the store read",
			treeType: "matrix",
		},
		{
			name:     "two roots, rejected by node validation",
			treeType: "unilevel",
			rows:     []networkengine.TreeNodeRow{root("t", "u0"), root("t", "u1")},
		},
		{
			name:     "create fails, incomplete at the create stage",
			treeType: "unilevel",
			rows:     []networkengine.TreeNodeRow{root("t", "u0")},
			engine:   failingMutator{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := networkengine.NewMemoryTreeStore()
			for _, r := range tt.rows {
				require.NoError(t, store.InsertNode(t.Context(), r))
			}

			err := networkengine.NewTreeLoader(store, tt.engine).
				LoadTree(t.Context(), "t", tt.treeType, tt.opts...)

			require.Error(t, err)
			for _, label := range fallbackLabels {
				assert.False(t, strings.HasPrefix(err.Error(), label),
					"message built by the loader carries the fallback label %q: %s", label, err)
			}
		})
	}
}

// A message the fallback builds does start with a label. Without this, the
// assertion above would pass against a renderer that emits no label at all.
func TestFallbackLabels_AreWhatTheFallbackActuallyEmits(t *testing.T) {
	rejected := (&networkengine.TreeLoadRejectedError{TreeID: "t"}).Error()
	incomplete := (&networkengine.TreeLoadIncompleteError{TreeID: "t"}).Error()

	assert.True(t, strings.HasPrefix(rejected, fallbackLabels[0]), "got %q", rejected)
	assert.True(t, strings.HasPrefix(incomplete, fallbackLabels[1]), "got %q", incomplete)
}
