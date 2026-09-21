package networkengine

import (
	"errors"
	"fmt"
	"testing"
)

// The three fields are all strings and the format arguments are hand-ordered,
// so transposing two of them compiles and stays silent. Pinning the whole
// message is what catches that; a Contains check on one field would not.
func TestRemovalNotProjectedErrorNamesEachFieldInItsOwnPlace(t *testing.T) {
	err := &RemovalNotProjectedError{TreeID: "TREE", UserID: "USER", EventID: "EVENT"}

	want := "engine reports USER absent from tree TREE, and an active row remains; event EVENT"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestRemovalNotProjectedErrorSurvivesWrapping(t *testing.T) {
	inner := &RemovalNotProjectedError{TreeID: "TREE", UserID: "USER", EventID: "EVENT"}
	wrapped := fmt.Errorf("handle node_removed: %w", inner)

	var target *RemovalNotProjectedError
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As should unwrap through fmt.Errorf")
	}
	if target.EventID != "EVENT" {
		t.Fatalf("EventID = %q, want EVENT", target.EventID)
	}
}

// Each sentinel below is a branch the consumer picks between. Two holding the
// same text would read as one condition in a log and tell an operator the wrong
// thing about which write was refused.
func TestTreeStoreSentinelsAreDistinct(t *testing.T) {
	sentinels := map[string]error{
		"ErrNodeAlreadyProjected": ErrNodeAlreadyProjected,
		"ErrActiveUserConflict":   ErrActiveUserConflict,
		"ErrSlotConflict":         ErrSlotConflict,
		"ErrReplayedPlacement":    ErrReplayedPlacement,
		"ErrRootConflict":         ErrRootConflict,
	}

	seen := make(map[string]string, len(sentinels))
	for name, err := range sentinels {
		if err == nil {
			t.Fatalf("%s is nil", name)
		}
		if other, dup := seen[err.Error()]; dup {
			t.Fatalf("%s and %s share the message %q", name, other, err.Error())
		}
		seen[err.Error()] = name
	}

	for name, err := range sentinels {
		for otherName, other := range sentinels {
			if name == otherName {
				continue
			}
			if errors.Is(err, other) {
				t.Fatalf("errors.Is(%s, %s) is true; the branches cannot be told apart", name, otherName)
			}
		}
	}
}
