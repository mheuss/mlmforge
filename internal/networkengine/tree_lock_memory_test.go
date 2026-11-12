package networkengine

import "testing"

func TestMemoryTreeLocker_SharedBehaviour(t *testing.T) {
	runTreeLockerSuite(t, func(t *testing.T) TreeLocker {
		return NewMemoryTreeLocker()
	})
}
