package networkengine

import "testing"

func TestMemoryCommissionRunStore(t *testing.T) {
	runCommissionRunStoreSuite(t, func(t *testing.T) CommissionRunStore {
		return NewMemoryCommissionRunStore()
	})
}
