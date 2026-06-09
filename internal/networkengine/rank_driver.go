package networkengine

import (
	"fmt"
	"sort"

	"github.com/google/uuid"
)

// distributorIDs parses the request's distributor map keys into sorted UUIDs.
// A non-UUID key is a loud error, named, before any engine call.
func distributorIDs(m map[string]DistributorPrimitivesDTO) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0, len(m))
	for k := range m {
		id, err := uuid.Parse(k)
		if err != nil {
			return nil, fmt.Errorf("rank driver: invalid distributor id %q: %w", k, err)
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids, nil
}
