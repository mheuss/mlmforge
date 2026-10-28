package financial

import (
	"testing"

	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoFloatInFinancial(t *testing.T) {
	found, err := testutil.FindFloatIdents(".", nil)

	require.NoError(t, err)
	assert.Empty(t, found, "float32 or float64 in the non-test Go files of internal/financial")
}
