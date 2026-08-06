package networkengine

import "testing"

func TestPostgresCommissionRunStore(t *testing.T) {
	if pgContainer == nil {
		t.Skip("postgres container unavailable")
	}
	runCommissionRunStoreSuite(t, func(t *testing.T) CommissionRunStore {
		return NewPostgresCommissionRunStore(pgContainer.NewPool(t))
	})
}
