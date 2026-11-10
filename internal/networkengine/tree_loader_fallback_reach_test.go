package networkengine_test

import (
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The label a rejection built outside this package renders with.
var fallbackLabels = []string{"tree load rejected"}

// A message the fallback builds starts with the label.
func TestFallbackLabels_AreWhatTheFallbackActuallyEmits(t *testing.T) {
	rejected := (&networkengine.TreeLoadRejectedError{TreeID: "t"}).Error()

	assert.True(t, strings.HasPrefix(rejected, fallbackLabels[0]), "got %q", rejected)
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
func TestTreeLoadRejectedError_ExternalCycleStillOverflows(t *testing.T) {
	if os.Getenv("HEU797_CYCLE_CHILD") == "1" {
		debug.SetMaxStack(1 << 20)
		outer := &networkengine.TreeLoadRejectedError{TreeID: "x", Kind: networkengine.TreeLoadDataInvalid}
		holder := &parentRenderer{}
		outer.Err = holder
		holder.parent = outer
		_ = outer.Error()
		return
	}

	child := exec.Command(os.Args[0], "-test.run=^TestTreeLoadRejectedError_ExternalCycleStillOverflows$")
	child.Env = append(os.Environ(), "HEU797_CYCLE_CHILD=1")
	out, err := child.CombinedOutput()

	require.Error(t, err, "the child was expected to crash, and did not")
	assert.Contains(t, string(out), "fatal error: stack overflow")
}
