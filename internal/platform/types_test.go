package platform

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConcurrencyError_Error(t *testing.T) {
	err := &ConcurrencyError{
		Stream:          "order-abc123",
		ExpectedVersion: 5,
		ActualVersion:   7,
	}

	want := `concurrency conflict on stream "order-abc123": expected version 5, actual version 7`
	assert.Equal(t, want, err.Error())
}

func TestConcurrencyError_ImplementsErrorInterface(t *testing.T) {
	var _ error = (*ConcurrencyError)(nil)
}
