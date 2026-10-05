package platform

import (
	"testing"

	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/assert"
)

func TestAdvisoryLockNamespaces_HoldTheirFixedValues(t *testing.T) {
	assert.Equal(t, int32(1953654117), TreeLockNamespace)
	assert.Equal(t, int32(1835624306), MigrateLockNamespace)
}

func TestAdvisoryLocks_TheSameKeyInTwoDatabasesDoesNotConflict(t *testing.T) {
	first, second := newResetDatabase(t), newResetDatabase(t)
	_, release := testutil.HoldAdvisoryLock(t, first, MigrateLockNamespace, 0)
	defer release()

	assert.False(t, testutil.TryAdvisoryLock(t, first, MigrateLockNamespace, 0), "control: the key is held in the first database")
	assert.True(t, testutil.TryAdvisoryLock(t, second, MigrateLockNamespace, 0), "the key held in one database must be free in another")
}
