package networkengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDistributorIDs(t *testing.T) {
	t.Run("two valid UUIDs parse to sorted slice", func(t *testing.T) {
		m := map[string]DistributorPrimitivesDTO{
			"00000000-0000-0000-0000-000000000002": {},
			"00000000-0000-0000-0000-000000000001": {},
		}
		ids, err := distributorIDs(m)
		require.NoError(t, err)
		require.Len(t, ids, 2)
		assert.True(t, ids[0].String() < ids[1].String(), "expected ascending order by string")
		assert.Equal(t, "00000000-0000-0000-0000-000000000001", ids[0].String())
		assert.Equal(t, "00000000-0000-0000-0000-000000000002", ids[1].String())
	})

	t.Run("bad UUID key returns error naming the key", func(t *testing.T) {
		m := map[string]DistributorPrimitivesDTO{
			"not-a-uuid": {},
		}
		_, err := distributorIDs(m)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not-a-uuid")
	})

	t.Run("empty map returns empty slice and no error", func(t *testing.T) {
		m := map[string]DistributorPrimitivesDTO{}
		ids, err := distributorIDs(m)
		require.NoError(t, err)
		assert.Empty(t, ids)
	})
}
