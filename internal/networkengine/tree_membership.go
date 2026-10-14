package networkengine

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// membershipListCap is how many user IDs one mismatch list names.
const membershipListCap = 5

// memberState holds one user's depth and enrolment second.
type memberState struct {
	depth      int
	enrolledAt int64
}

// checkMembership compares the engine's tree with a non-empty set of active
// rows, which must hold exactly one depth-0 row. It returns the observation
// when they differ, or "" when they match.
func checkMembership(ctx context.Context, engine TreeRebuilder, tree string, rows []TreeNodeRow) (string, error) {
	var root string
	store := make(map[string]memberState, len(rows))
	for _, row := range rows {
		if row.Depth == 0 {
			root = canonicalUser(row.UserID)
		}
		store[canonicalUser(row.UserID)] = memberState{depth: row.Depth, enrolledAt: row.EnrolledAt.Unix()}
	}
	pos, err := engine.GetPosition(ctx, tree, root)
	if err != nil {
		return "", err
	}
	down, err := engine.GetDownline(ctx, tree, root, 0)
	if err != nil {
		return "", err
	}
	held := map[string]memberState{
		canonicalUser(pos.UserID): {depth: int(pos.Depth), enrolledAt: pos.EnrolledAt},
	}
	for _, n := range down {
		held[canonicalUser(n.UserID)] = memberState{depth: int(n.Depth), enrolledAt: n.EnrolledAt}
	}

	var onlyEngine, onlyStore, differs []string
	for id, h := range held {
		s, ok := store[id]
		switch {
		case !ok:
			onlyEngine = append(onlyEngine, id)
		case s != h:
			differs = append(differs, id)
		}
	}
	for id := range store {
		if _, ok := held[id]; !ok {
			onlyStore = append(onlyStore, id)
		}
	}
	if len(onlyEngine) == 0 && len(onlyStore) == 0 && len(differs) == 0 {
		return "", nil
	}
	return fmt.Sprintf("the engine holds %d nodes under root %s; the store holds %d active rows; "+
		"first user IDs only in the engine: %s; only in the store: %s; depth or enrolled_at differs for: %s",
		len(held), root, len(store), firstIDs(onlyEngine), firstIDs(onlyStore), firstIDs(differs)), nil
}

// canonicalUser returns id in canonical UUID form, or unchanged when it does not parse.
func canonicalUser(id string) string {
	if u, err := uuid.Parse(id); err == nil {
		return u.String()
	}
	return id
}

// firstIDs renders the first membershipListCap IDs in sorted order.
func firstIDs(ids []string) string {
	slices.Sort(ids)
	if len(ids) > membershipListCap {
		ids = ids[:membershipListCap]
	}
	return "[" + strings.Join(ids, ", ") + "]"
}
