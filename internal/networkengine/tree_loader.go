package networkengine

import (
	"container/heap"
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
)

// TreeLoader rebuilds the Rust engine's in-memory state from the
// adjacency table on startup.
type TreeLoader struct {
	store  TreeStore
	engine TreeMutator
}

func NewTreeLoader(store TreeStore, engine TreeMutator) *TreeLoader {
	return &TreeLoader{store: store, engine: engine}
}

// LoadTreeOption configures optional parameters for LoadTree.
type LoadTreeOption func(*loadTreeConfig)

type loadTreeConfig struct {
	matrixParamsSet bool
	matrixWidth     int
	matrixSpillover string
}

// WithMatrixParams supplies the width and spillover a matrix tree needs to be
// recreated. It is required when treeType is "matrix" (the adjacency store
// keeps per-node data, not the structure's width/spillover) and ignored
// otherwise.
func WithMatrixParams(width int, spillover string) LoadTreeOption {
	return func(c *loadTreeConfig) {
		c.matrixParamsSet = true
		c.matrixWidth = width
		c.matrixSpillover = spillover
	}
}

// LoadTree reads all active nodes for a tree from the store, validates that
// the set is reconstructable, orders it so every node follows its parent and
// sponsor, then replays it into the engine. Matrix trees replay through
// AddNodeAt so stored placements survive; their AddNode would re-derive
// placement by spillover. Matrix trees need width and spillover supplied
// through WithMatrixParams, which plain CreateTree does not carry.
func (l *TreeLoader) LoadTree(ctx context.Context, treeID, treeType string, opts ...LoadTreeOption) error {
	var cfg loadTreeConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	// Configuration is checked before the store is read, and before the
	// node-count check below. Tree type and matrix params describe the
	// structure, not its contents, so their validity does not depend on how
	// many rows came back. Checking them after an empty-set short circuit
	// would let a typo in startup wiring stay invisible until the first node
	// arrived.
	if err := validateTreeConfig(treeID, treeType, cfg); err != nil {
		return err
	}

	nodes, err := l.store.GetByTreeDepthOrdered(ctx, treeID)
	if err != nil {
		return fmt.Errorf("load tree %s: %w", treeID, err)
	}
	if len(nodes) == 0 {
		return nil
	}

	// Preflight. Nothing below this point runs until both phases succeed.
	if err := validateNodes(treeID, treeType, cfg, nodes); err != nil {
		return err
	}
	ordered, err := orderForReplay(treeID, nodes)
	if err != nil {
		return err
	}

	if treeType == treeTypeMatrix {
		if err := l.engine.CreateMatrixTree(ctx, treeID, cfg.matrixWidth, cfg.matrixSpillover); err != nil {
			return fmt.Errorf("create tree %s: %w", treeID, err)
		}
	} else if err := l.engine.CreateTree(ctx, treeID, treeType); err != nil {
		return fmt.Errorf("create tree %s: %w", treeID, err)
	}

	// Every failure from here on says what survived it. The structure now
	// exists and the worker has no operation to drop it (HEU-557), so a retry
	// reports TREE_EXISTS and the only real remedy is a process restart. How
	// much landed is the operator's sole input to that call. Validation
	// pre-empts every logical error the engine can raise below, so what
	// actually reaches these paths is transport failure: a worker crash, an
	// IPC timeout, a cancelled context.
	//
	// ordered[0] is the root: validation proved exactly one depth-0 node exists
	// and every other node has a non-nil parent that is not itself, so the root
	// is the only node with zero dependencies.
	root := ordered[0]
	if err := l.engine.AddRoot(ctx, treeID, root.UserID, root.EnrolledAt.Unix()); err != nil {
		return fmt.Errorf("add root %s (tree %s created but left empty): %w", root.UserID, treeID, err)
	}

	// The index names the node that failed, so "3 of 5" means two landed.
	total := len(ordered) - 1
	for i, node := range ordered[1:] {
		// validateNodes already proved these non-nil for every non-root, and
		// ordered[1:] excludes the root. Kept as guards anyway: this runs at
		// startup, where a nil deref panics the process instead of failing one
		// tree, and the invariant now spans two functions.
		if node.ParentID == nil || node.SponsorID == nil {
			return fmt.Errorf("node %s in tree %s has nil parent or sponsor (data corruption; %d of %d, tree left partly built)",
				node.UserID, treeID, i+1, total)
		}
		parentID, sponsorID := *node.ParentID, *node.SponsorID

		if treeType == treeTypeMatrix {
			// Unreachable for the same reason as the guard above: validateNodes
			// rejects a nil position on every non-root matrix node. Kept for
			// the same reason too — a nil deref here panics startup.
			if node.Position == nil {
				return fmt.Errorf("matrix node %s in tree %s has nil position (the adjacency row is incomplete; %d of %d, tree left partly built)",
					node.UserID, treeID, i+1, total)
			}
			if err := l.engine.AddNodeAt(ctx, treeID, node.UserID, parentID, sponsorID,
				*node.Position, node.EnrolledAt.Unix()); err != nil {
				return fmt.Errorf("add node %s (%d of %d, tree %s left partly built): %w",
					node.UserID, i+1, total, treeID, err)
			}
			continue
		}

		var addOpts []AddNodeOption
		if node.Position != nil {
			addOpts = append(addOpts, WithPosition(*node.Position))
		}
		if err := l.engine.AddNode(ctx, treeID, node.UserID, parentID, sponsorID,
			node.EnrolledAt.Unix(), addOpts...); err != nil {
			return fmt.Errorf("add node %s (%d of %d, tree %s left partly built): %w",
				node.UserID, i+1, total, treeID, err)
		}
	}
	return nil
}

// supportedTreeTypes are the structures LoadTree knows how to replay. Anything
// else would reach the worker as an unknown type after the engine had already
// been mutated.
//
// This mirrors the worker's create_tree dispatch
// (engine/network-engine-worker/src/handlers/tree.rs) and is not generated
// from it. A new tree type has to be added in both places — and in
// handleNodePlaced's position-rule switch, whose default arm names itself
// when the rule is missing.
var supportedTreeTypes = map[string]bool{
	treeTypeUnilevel: true,
	treeTypeBinary:   true,
	treeTypeMatrix:   true,
}

// supportedSpillover are the spillover values create_tree accepts. The worker
// maps only "breadth_first", and MatrixTree::new rejects DepthFirst outright
// (MatrixTree::new), so this set has one member today.
var supportedSpillover = map[string]bool{
	"breadth_first": true,
}

// slotKey identifies one occupied child position. Two nodes claiming the same
// key means the adjacency rows disagree about who sits where. Matrix and
// binary index children by position; unilevel does not.
type slotKey struct {
	parentID string
	position int
}

// validateTreeConfig proves the load's configuration is sound: a supported tree
// type, and for a matrix tree a width and spillover the engine will accept.
//
// It is split from validateNodes because it reads no rows. LoadTree runs it
// before touching the store, so a misconfigured load costs no query and an
// empty tree reports the same configuration errors a populated one does.
func validateTreeConfig(treeID, treeType string, cfg loadTreeConfig) error {
	if !supportedTreeTypes[treeType] {
		return fmt.Errorf("tree %s has unsupported type %q", treeID, treeType)
	}
	if treeType != treeTypeMatrix {
		return nil
	}
	// The adjacency store keeps per-node data, not the structure's width and
	// spillover, so a matrix load cannot recreate the tree without them.
	if !cfg.matrixParamsSet {
		return fmt.Errorf("tree %s requires width and spillover (use WithMatrixParams)", treeID)
	}
	// MatrixTree::new rejects width < 2, and width is a u8 on the wire.
	if cfg.matrixWidth < 2 || cfg.matrixWidth > math.MaxUint8 {
		return fmt.Errorf("tree %s has matrix width %d outside the supported range 2..%d",
			treeID, cfg.matrixWidth, math.MaxUint8)
	}
	if !supportedSpillover[cfg.matrixSpillover] {
		return fmt.Errorf("tree %s has unsupported spillover %q", treeID, cfg.matrixSpillover)
	}
	return nil
}

// validateNodes proves the node set is structurally consistent before any
// engine mutation. A tree that fails here leaves the engine untouched, so the
// load can be retried once the data is corrected. This matters because the
// worker has no operation to drop a structure (HEU-557): a partially built
// tree would be stuck until the process restarts.
//
// Structural consistency is only half of "reconstructable". This function
// proves every non-root node's parent and sponsor exists in the set; it does
// not prove they can be replayed in an order that satisfies the engine. A node
// may legally sit shallower than its own sponsor, and the engine resolves a
// sponsor at insert time, so depth order alone can still fail mid-replay.
// orderForReplay closes that half.
//
// cfg supplies matrix width and spillover for the slot rule below.
// validateTreeConfig has already proved both are in range. Binary positions are
// fixed at 0 and 1; unilevel nodes carry no position.
func validateNodes(treeID, treeType string, cfg loadTreeConfig, nodes []TreeNodeRow) error {
	// Pointers, not values: a distributor network can hold hundreds of
	// thousands of rows, and TreeNodeRow is wide. nodes is never mutated here,
	// so the pointers stay valid for the life of the call.
	byID := make(map[string]*TreeNodeRow, len(nodes))
	var root *TreeNodeRow

	for i := range nodes {
		n := &nodes[i]
		if _, dup := byID[n.UserID]; dup {
			return fmt.Errorf("tree %s has duplicate user %s (data corruption?)", treeID, n.UserID)
		}
		byID[n.UserID] = n
		if n.Depth == 0 {
			if root != nil {
				return fmt.Errorf("tree %s has more than one depth-0 root (%s and %s)",
					treeID, root.UserID, n.UserID)
			}
			root = n
		}
	}
	if root == nil {
		return fmt.Errorf("tree %s has no depth-0 root node (data corruption?)", treeID)
	}

	// Root invariants. AddRoot takes neither a parent nor a position, so a row
	// carrying either describes a topology the engine cannot reproduce. Root
	// sponsorship is deliberately exempt: AddRoot takes no sponsor and the
	// engine root carries sponsor: None, so whatever a root row stores is
	// projection metadata that reload drops. See docs/development/network-engine.md.
	if root.ParentID != nil {
		return fmt.Errorf("root %s in tree %s has parent %s (the engine root has no parent)",
			root.UserID, treeID, *root.ParentID)
	}
	if root.Position != nil {
		return fmt.Errorf("root %s in tree %s has position %d (the engine root occupies no slot)",
			root.UserID, treeID, *root.Position)
	}

	// Slot rule per tree type, derived once. Every supported type is named
	// explicitly so that adding a fourth to supportedTreeTypes without deciding
	// its slot rule fails here with an actionable message, rather than
	// inheriting limit 0 and rejecting every node with "outside the range
	// 0..-1". The comment on supportedTreeTypes says to expect a fourth.
	var limit int
	hasSlots := true
	switch treeType {
	case treeTypeUnilevel:
		// Unilevel appends to the parent's child list; children carry no slot.
		hasSlots = false
	case treeTypeBinary:
		// Binary is a width-2 matrix as far as slot validation is concerned.
		limit = 2
	case treeTypeMatrix:
		limit = cfg.matrixWidth
	default:
		return fmt.Errorf("tree %s has type %q with no slot rule (add one to validateNodes)",
			treeID, treeType)
	}

	// Only sized when it will be used. A size hint allocates buckets eagerly,
	// and unilevel skips the slot block entirely — sizing it there would strand
	// megabytes on the tree type most likely to carry hundreds of thousands of
	// rows. Left nil for unilevel, which is safe because the skip below
	// guarantees no read or write.
	var occupied map[slotKey]string
	if hasSlots {
		occupied = make(map[slotKey]string, len(nodes))
	}

	for i := range nodes {
		n := &nodes[i]
		if n.UserID == root.UserID {
			continue
		}
		if n.ParentID == nil || n.SponsorID == nil {
			return fmt.Errorf("node %s in tree %s has nil parent or sponsor (data corruption?)",
				n.UserID, treeID)
		}
		// Only the root may reference itself. A non-root self-sponsor resolves
		// against byID and would pass an existence check, then fail in the
		// engine: the same call that creates the node resolves its sponsor, so
		// the sponsor does not exist yet.
		if *n.ParentID == n.UserID {
			return fmt.Errorf("node %s in tree %s is its own parent", n.UserID, treeID)
		}
		if *n.SponsorID == n.UserID {
			return fmt.Errorf("node %s in tree %s is its own sponsor", n.UserID, treeID)
		}
		parent, ok := byID[*n.ParentID]
		if !ok {
			return fmt.Errorf("node %s in tree %s references parent %s that is not in the tree",
				n.UserID, treeID, *n.ParentID)
		}
		if _, ok := byID[*n.SponsorID]; !ok {
			return fmt.Errorf("node %s in tree %s references sponsor %s that is not in the tree",
				n.UserID, treeID, *n.SponsorID)
		}
		if n.Depth != parent.Depth+1 {
			return fmt.Errorf("node %s in tree %s has depth %d but parent %s has depth %d",
				n.UserID, treeID, n.Depth, parent.UserID, parent.Depth)
		}

		// A unilevel row that carries a position is not rejected — see HEU-563.
		if !hasSlots {
			continue
		}

		if n.Position == nil {
			return fmt.Errorf("%s node %s in tree %s has nil position (the adjacency row is incomplete)",
				treeType, n.UserID, treeID)
		}
		pos := *n.Position
		if pos < 0 || pos >= limit {
			return fmt.Errorf("%s node %s in tree %s has position %d outside the range 0..%d",
				treeType, n.UserID, treeID, pos, limit-1)
		}
		key := slotKey{parentID: *n.ParentID, position: pos}
		if other, taken := occupied[key]; taken {
			return fmt.Errorf("%s nodes %s and %s in tree %s both claim parent %s position %d",
				treeType, other, n.UserID, treeID, *n.ParentID, pos)
		}
		occupied[key] = n.UserID
	}
	return nil
}

// replayQueue is a min-heap of nodes eligible to replay next, ordered
// shallowest first, then earliest enrolled, then user ID. The tuple makes the
// output deterministic regardless of how the store broke ties.
//
// Each user is pushed at most once: a node enters only when its final
// dependency clears, and each dependency clears once. Less has no tiebreak
// beyond UserID, so two rows sharing a user ID would order arbitrarily —
// validateNodes rejects that case before this runs.
//
// Holds pointers for the same reason validateNodes does: TreeNodeRow is wide
// and a distributor network can hold hundreds of thousands of rows.
type replayQueue []*TreeNodeRow

func (q replayQueue) Len() int      { return len(q) }
func (q replayQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }

func (q replayQueue) Less(i, j int) bool {
	if q[i].Depth != q[j].Depth {
		return q[i].Depth < q[j].Depth
	}
	if !q[i].EnrolledAt.Equal(q[j].EnrolledAt) {
		return q[i].EnrolledAt.Before(q[j].EnrolledAt)
	}
	return q[i].UserID < q[j].UserID
}

func (q *replayQueue) Push(x any) { *q = append(*q, x.(*TreeNodeRow)) }

func (q *replayQueue) Pop() any {
	old := *q
	last := len(old) - 1
	item := old[last]
	old[last] = nil // let the row go if the caller drops the queue
	*q = old[:last]
	return item
}

// orderForReplay returns nodes ordered so that every node appears after both
// its parent and its sponsor. Depth order alone is not enough: an explicitly
// placed node can sit at a shallower depth than its sponsor, and every tree
// type rejects a sponsor that is not yet present.
//
// Kahn's algorithm over a priority queue, so the result is deterministic.
// Every node contributes at most two edges, so e <= 2n and the pass is
// O(n log n).
//
// validateNodes must run first. This assumes every parent and sponsor
// reference resolves within nodes, and that only the root self-references.
func orderForReplay(treeID string, nodes []TreeNodeRow) ([]*TreeNodeRow, error) {
	// remaining[u] = how many nodes must be replayed before u.
	// dependents[u] = users waiting on u.
	//
	// A counter rather than a set of names: a node has at most two distinct
	// dependencies (parent and sponsor), so the only duplicate possible is
	// parent == sponsor, which the loop below checks directly. A map per node
	// cost ~7 allocations each for the same answer.
	remaining := make(map[string]int, len(nodes))
	dependents := make(map[string][]string, len(nodes))
	byID := make(map[string]*TreeNodeRow, len(nodes))

	for i := range nodes {
		byID[nodes[i].UserID] = &nodes[i]
	}
	for _, n := range nodes {
		// The root's stored sponsor is projection metadata, never a replay
		// dependency: AddRoot takes no sponsor and the engine root carries
		// sponsor: None. Treating it as one would make the root wait on a node
		// that is itself downstream of the root, and reject the whole tree as a
		// cycle. The root is identified by a nil parent, which validateNodes
		// has already proven unique.
		refs := []*string{n.ParentID}
		if n.ParentID != nil {
			refs = append(refs, n.SponsorID)
		}
		var seen *string
		for _, ref := range refs {
			// Skipping a self-reference drops the edge, so a non-root that
			// named itself would order cleanly here and then fail in the
			// engine mid-replay, with the tree already partly built. Nothing
			// downstream catches that: validateNodes rejecting non-root
			// self-references is the only thing standing between this loop and
			// a partially mutated engine.
			if ref == nil || *ref == n.UserID {
				continue
			}
			// Skip parent == sponsor, the shape automatic spillover produces
			// for most nodes. A memory optimization, not a correctness guard:
			// counting the edge twice also appends to dependents twice, so both
			// decrements land and the node still unblocks. It halves the
			// dependents entries on a spillover-shaped tree.
			if seen != nil && *seen == *ref {
				continue
			}
			seen = ref
			remaining[n.UserID]++
			dependents[*ref] = append(dependents[*ref], n.UserID)
		}
	}

	ready := &replayQueue{}
	for i := range nodes {
		if remaining[nodes[i].UserID] == 0 {
			*ready = append(*ready, &nodes[i])
		}
	}
	heap.Init(ready)

	ordered := make([]*TreeNodeRow, 0, len(nodes))
	for ready.Len() > 0 {
		n := heap.Pop(ready).(*TreeNodeRow)
		ordered = append(ordered, n)

		for _, d := range dependents[n.UserID] {
			remaining[d]--
			if remaining[d] == 0 {
				heap.Push(ready, byID[d])
			}
		}
	}

	if len(ordered) != len(nodes) {
		return nil, cycleError(treeID, nodes, ordered, byID)
	}
	return ordered, nil
}

// cycleError explains why a replay order could not be produced. It runs only
// on the failure path, so it recomputes what it needs rather than making the
// successful pass carry it.
//
// BR-4 requires naming the offending nodes. Listing the stuck set does not
// satisfy that: the set is the cycle members plus everything blocked behind
// them, and the blocked nodes usually outnumber the cycle by a wide margin, so
// any prefix of it can name only bystanders. Instead this walks to a concrete
// cycle and names that, keeping the stuck count as the blast radius.
//
// The walk terminates. Every stuck node has at least one dependency that was
// never emitted, that dependency is itself stuck, and the stuck set is finite,
// so following unmet dependencies must revisit a node within len(stuck) steps.
// The revisit closes a real cycle.
func cycleError(treeID string, nodes []TreeNodeRow, ordered []*TreeNodeRow, byID map[string]*TreeNodeRow) error {
	emitted := make(map[string]struct{}, len(ordered))
	for _, n := range ordered {
		emitted[n.UserID] = struct{}{}
	}

	var stuck []string
	for _, n := range nodes {
		if _, done := emitted[n.UserID]; !done {
			stuck = append(stuck, n.UserID)
		}
	}
	// Sorted so a given corrupt tree always reports the same cycle, even when
	// it contains several.
	sort.Strings(stuck)

	// Every node was emitted yet the counts disagree, so two rows shared a user
	// ID: both were pushed, one key landed in emitted. validateNodes rejects
	// duplicates, so this needs a broken precondition to reach — but the walk
	// below indexes stuck[0], and a panic here takes down startup rather than
	// failing one tree.
	if len(stuck) == 0 {
		return fmt.Errorf("tree %s: replay produced %d of %d nodes with no unreplayable node (duplicate user IDs?)",
			treeID, len(ordered), len(nodes))
	}

	// unmet returns a dependency of id that was never emitted, or "" if there
	// is none. A stuck node always has one; the root is never stuck, so a
	// stuck node's parent is non-nil and both refs are real dependencies.
	unmet := func(id string) string {
		n, ok := byID[id]
		if !ok {
			return ""
		}
		for _, ref := range []*string{n.ParentID, n.SponsorID} {
			if ref == nil || *ref == id {
				continue
			}
			if _, done := emitted[*ref]; !done {
				return *ref
			}
		}
		return ""
	}

	seenAt := make(map[string]int, len(stuck))
	var path []string
	for cur := stuck[0]; cur != ""; cur = unmet(cur) {
		if idx, ok := seenAt[cur]; ok {
			cycle := append(append([]string{}, path[idx:]...), cur)
			return fmt.Errorf(
				"tree %s: %d of %d nodes cannot be replayed because their parent/sponsor references form a cycle: %s",
				treeID, len(stuck), len(nodes), strings.Join(cycle, " -> "))
		}
		seenAt[cur] = len(path)
		path = append(path, cur)
	}

	// The walk ran out of unmet dependencies instead of closing a loop, so the
	// last hop named a user it could not follow. Name that user and, when the
	// walk took more than one step, whoever referenced it — not every node
	// blocked behind them. validateNodes rejects both causes (dangling
	// references and duplicate rows), so reaching here needs a broken
	// precondition.
	//
	// Deliberately one branch rather than separating "target absent" from
	// "target present but stalled": the second is reachable only through
	// duplicate rows, and an exit no fixture reaches does not survive rewrites.
	//
	// The wording is exact for the absent case and loose for the stalled one,
	// where byID's surviving copy resolves both refs and the unmet edge belongs
	// to the shadowed row the message cannot name. Only duplicate rows reach
	// that, and validateNodes rejects them.
	stopped := ""
	if len(path) > 0 {
		stopped = path[len(path)-1]
	}
	via := ""
	if len(path) >= 2 {
		via = fmt.Sprintf(", reached from %s", path[len(path)-2])
	}
	return fmt.Errorf(
		"tree %s: %d of %d nodes cannot be replayed (the replay order stops at %s%s, whose parent or sponsor cannot be resolved)",
		treeID, len(stuck), len(nodes), stopped, via)
}
