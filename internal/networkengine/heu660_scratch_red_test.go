package networkengine

import "testing"

// TestHEU660ScratchDeliberateFailure exists to prove that a failing Go test
// turns the CI step red. It is reverted in the commit immediately after the one
// that added it. If you are reading this on a branch other than the HEU-660
// scratch commit, it escaped and should be deleted.
//
// Why it is needed: the Test step is a pipeline, and a pipeline reports its
// last command's status. Without pipefail a failing suite reports success. This
// test is the only thing on the branch that demonstrates the difference on a
// real runner, because every other run is green by design.
func TestHEU660ScratchDeliberateFailure(t *testing.T) {
	t.Fatal("deliberate failure, HEU-660 scratch commit: proving the CI step reports red")
}
