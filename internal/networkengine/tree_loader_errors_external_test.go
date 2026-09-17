package networkengine_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTreeLoadRejectedError_ExternalLiteralRendersNonEmpty(t *testing.T) {
	err := &networkengine.TreeLoadRejectedError{
		TreeID: "t1",
		Kind:   networkengine.TreeLoadDataInvalid,
	}

	require.NotEmpty(t, err.Error())
	assert.Contains(t, err.Error(), "t1")
	assert.Contains(t, err.Error(), string(networkengine.TreeLoadDataInvalid))
}

func TestTreeLoadIncompleteError_ExternalLiteralRendersNonEmpty(t *testing.T) {
	err := &networkengine.TreeLoadIncompleteError{
		TreeID:    "t2",
		Stage:     networkengine.TreeLoadStageNodes,
		Confirmed: 2,
		Attempted: 3,
		Total:     4,
	}

	require.NotEmpty(t, err.Error())
	assert.Contains(t, err.Error(), "t2")
	assert.Contains(t, err.Error(), string(networkengine.TreeLoadStageNodes))
}

func TestTreeLoadErrors_WrappedExternalLiteralCarriesText(t *testing.T) {
	rejected := &networkengine.TreeLoadRejectedError{TreeID: "t3", Kind: networkengine.TreeLoadConfigInvalid}

	wrapped := fmt.Errorf("load failed: %w", rejected)

	assert.NotEqual(t, "load failed: ", wrapped.Error())
	assert.Contains(t, wrapped.Error(), "t3")
}

func TestTreeLoadErrors_ZeroValueRendersNonEmpty(t *testing.T) {
	rejected := &networkengine.TreeLoadRejectedError{}
	incomplete := &networkengine.TreeLoadIncompleteError{}

	assert.NotEmpty(t, rejected.Error())
	assert.NotEmpty(t, incomplete.Error())
}

func TestTreeLoadErrors_ExternalLiteralStillUnwraps(t *testing.T) {
	cause := errors.New("connection refused")
	rejected := &networkengine.TreeLoadRejectedError{TreeID: "t4", Kind: networkengine.TreeLoadStoreReadFailed, Err: cause}

	assert.ErrorIs(t, rejected, cause)
	assert.Contains(t, rejected.Error(), "connection refused")
}
