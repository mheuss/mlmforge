package networkengine

import (
	"context"

	"github.com/google/uuid"
)

// TreeLocker grants exclusive use of one tree.
type TreeLocker interface {
	Lock(ctx context.Context, treeID uuid.UUID) (unlock func() error, err error)
}
