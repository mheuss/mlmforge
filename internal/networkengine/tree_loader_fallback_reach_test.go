package networkengine_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
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

var _ networkengine.TreeMutator = failingMutator{}

// failingMutator fails one named engine call and lets the rest succeed, so a
// test can choose which post-create exit the loader reaches.
type failingMutator struct{ failOn string }

func (m failingMutator) fail(call string) error {
	if m.failOn == call {
		return errors.New("worker said no")
	}
	return nil
}

func (m failingMutator) CreateTree(context.Context, string, string) error {
	return m.fail("CreateTree")
}
func (m failingMutator) CreateMatrixTree(context.Context, string, int, string) error {
	return m.fail("CreateMatrixTree")
}
func (m failingMutator) AddRoot(context.Context, string, string, int64) error {
	return m.fail("AddRoot")
}
func (m failingMutator) AddNode(context.Context, string, string, string, string, int64, ...networkengine.AddNodeOption) error {
	return m.fail("AddNode")
}
func (m failingMutator) AddNodeAt(context.Context, string, string, string, string, int, int64) error {
	return m.fail("AddNodeAt")
}
func (m failingMutator) RemoveNode(context.Context, string, string) ([]networkengine.Responsored, error) {
	return nil, m.fail("RemoveNode")
}

// twoRootStore serves a fixed row set from GetByTreeDepthOrdered, the one store
// method LoadTree reads. It answers for the tree it was built for and fails for
// any other, so a caller reading the wrong tree cannot pass.
type twoRootStore struct {
	networkengine.TreeStore
	treeID string
	rows   []networkengine.TreeNodeRow
}

func (s twoRootStore) GetByTreeDepthOrdered(_ context.Context, treeID string) ([]networkengine.TreeNodeRow, error) {
	if treeID != s.treeID {
		return nil, fmt.Errorf("twoRootStore holds tree %s, asked for %s", s.treeID, treeID)
	}
	return s.rows, nil
}

// The fallback is safe only because every value this package constructs sets
// its message. This drives a failure at each stage LoadTree can fail at, and
// asserts none of them renders a fallback label. The table names the stage each
// case reaches, so a reader can see the coverage rather than infer it from the
// test name.
func TestLoadTree_FailuresAtEveryStageNeverRenderTheFallback(t *testing.T) {
	root := func(treeID, userID string) networkengine.TreeNodeRow {
		return networkengine.TreeNodeRow{
			ID: userID, TreeID: treeID, UserID: userID, Depth: 0,
			EnrolledAt: time.Unix(1, 0),
		}
	}

	child := func(treeID, userID, parentID string) networkengine.TreeNodeRow {
		return networkengine.TreeNodeRow{
			ID: userID, TreeID: treeID, UserID: userID, Depth: 1,
			ParentID: &parentID, SponsorID: &parentID,
			EnrolledAt: time.Unix(2, 0),
		}
	}

	tests := []struct {
		name     string
		treeType string
		rows     []networkengine.TreeNodeRow
		engine   networkengine.TreeMutator
		opts     []networkengine.LoadTreeOption
		// Exactly one of these is set. Asserting it stops a case passing
		// because the loader failed earlier than the case name claims.
		wantKind  networkengine.TreeLoadRejectionKind
		wantStage networkengine.TreeLoadStage
		// direct hands the rows to the loader without inserting them.
		direct bool
	}{
		{
			name:     "unsupported tree type, rejected before the store read",
			treeType: "unsupported-type",
			wantKind: networkengine.TreeLoadConfigInvalid,
		},
		{
			name:     "matrix without params, rejected before the store read",
			treeType: "matrix",
			wantKind: networkengine.TreeLoadConfigInvalid,
		},
		{
			name:     "two roots, rejected by node validation",
			treeType: "unilevel",
			rows:     []networkengine.TreeNodeRow{root("t", "u0"), root("t", "u1")},
			direct:   true,
			wantKind: networkengine.TreeLoadDataInvalid,
		},
		{
			name:      "create fails, incomplete at the create stage",
			treeType:  "unilevel",
			rows:      []networkengine.TreeNodeRow{root("t", "u0")},
			engine:    failingMutator{failOn: "CreateTree"},
			wantStage: networkengine.TreeLoadStageCreate,
		},
		{
			name:      "root placement fails, incomplete at the root stage",
			treeType:  "unilevel",
			rows:      []networkengine.TreeNodeRow{root("t", "u0")},
			engine:    failingMutator{failOn: "AddRoot"},
			wantStage: networkengine.TreeLoadStageRoot,
		},
		{
			name:      "a later placement fails, incomplete at the nodes stage",
			treeType:  "unilevel",
			rows:      []networkengine.TreeNodeRow{root("t", "u0"), child("t", "u1", "u0")},
			engine:    failingMutator{failOn: "AddNode"},
			wantStage: networkengine.TreeLoadStageNodes,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var store networkengine.TreeStore = networkengine.NewMemoryTreeStore()
			if tt.direct {
				store = twoRootStore{
					TreeStore: networkengine.NewMemoryTreeStore(),
					treeID:    "t",
					rows:      tt.rows,
				}
			} else {
				for _, r := range tt.rows {
					require.NoError(t, store.InsertNode(t.Context(), r))
				}
			}

			err := networkengine.NewTreeLoader(store, tt.engine).
				LoadTree(t.Context(), "t", tt.treeType, tt.opts...)

			require.Error(t, err)

			if tt.wantKind != "" {
				var rejected *networkengine.TreeLoadRejectedError
				require.ErrorAs(t, err, &rejected)
				require.Equal(t, tt.wantKind, rejected.Kind, "case did not reach the exit it names")
			} else {
				var incomplete *networkengine.TreeLoadIncompleteError
				require.ErrorAs(t, err, &incomplete)
				require.Equal(t, tt.wantStage, incomplete.Stage, "case did not reach the stage it names")
			}

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

// parentRenderer renders the value that holds it. Two of these form a cycle
// that runs entirely through Error, which no guard inside Error can see.
type parentRenderer struct{ parent error }

func (p *parentRenderer) Error() string { return p.parent.Error() }

// The cycle guard reaches this package's own types and stops there. A cause of
// any other type is called, so a cycle closed through a caller's own Error
// method still recurses. This pins where that boundary is.
//
// The child process is expected to die. A fatal stack overflow cannot be
// recovered in-process, so asserting it needs a subprocess. SetMaxStack keeps
// the crash under a second.
func TestLoadTree_ExternalCycleStillOverflows(t *testing.T) {
	if os.Getenv("HEU797_CYCLE_CHILD") == "1" {
		debug.SetMaxStack(1 << 20)
		outer := &networkengine.TreeLoadRejectedError{TreeID: "x", Kind: networkengine.TreeLoadDataInvalid}
		holder := &parentRenderer{}
		outer.Err = holder
		holder.parent = outer
		_ = outer.Error()
		return
	}

	child := exec.Command(os.Args[0], "-test.run=^TestLoadTree_ExternalCycleStillOverflows$")
	child.Env = append(os.Environ(), "HEU797_CYCLE_CHILD=1")
	out, err := child.CombinedOutput()

	require.Error(t, err, "the child was expected to crash, and did not")
	assert.Contains(t, string(out), "fatal error: stack overflow")
}
