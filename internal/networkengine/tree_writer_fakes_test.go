package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTime is the enrolment and removal time the writer tests use.
var writeTime = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

// Identifiers the writer unit tests share.
var (
	writerTree          = testTreeUUID(1)
	writerRoot          = testUserUUID(1)
	writerChild         = testUserUUID(2)
	writerOther         = testUserUUID(3)
	writerRemoved       = testUserUUID(4)
	writerAbsentSponsor = testUserUUID(9)
)

// fakeWriterEngine stands in for the engine in writer tests.
type fakeWriterEngine struct {
	trees    map[string]*fakeEngineTree
	checkErr error
	checks   []Mutation
	// calls logs the requests made, in order.
	calls []string
	// placements holds each placement call, in the form of its check.
	placements []Mutation
	// failAdd fails every add of the keyed user.
	failAdd map[string]error
	// downlineErr is what every GetDownline returns.
	downlineErr error
	// downlineErrOnce is what the next GetDownline returns, once.
	downlineErrOnce error
	// positionErr is what every GetPosition returns.
	positionErr error
	// failAddOnce fails the keyed user's next add, then forgets it.
	failAddOnce map[string]error
	// takeErr is what every TakeSnapshot returns.
	takeErr error
	// retypeBeforeTake, when set, makes TakeSnapshot drop the structure and
	// create it again as this type before reading it.
	retypeBeforeTake string
	// restoreErr is what every RestoreSnapshot returns.
	restoreErr error
	// dropErr is what every DropStructure returns.
	dropErr error
	// dropReportsAbsent makes DropStructure report no structure, as if another
	// call had dropped it first.
	dropReportsAbsent bool
}

type fakeEngineTree struct {
	treeType string
	width    int
	root     string
	nodes    map[string]EnginePosition
	// sponsored lists each sponsor's recruits in the order they were attached.
	sponsored map[string][]string
}

// checkRoot refuses a root this tree cannot take.
func (t *fakeEngineTree) checkRoot(userID string) error {
	if t.root != "" {
		return fakeEngineError(engineCodeRootAlreadyExists, "tree already has a root node")
	}
	if _, ok := t.nodes[userID]; ok {
		return fakeEngineError(engineCodeUserAlreadyExists, "user %s already exists in tree", userID)
	}
	return nil
}

// checkPlacement refuses a placement this tree cannot take.
func (t *fakeEngineTree) checkPlacement(op, userID, parentID, sponsorID string, position int, hasPosition bool) error {
	binary := op == "add_node" && t.treeType == treeTypeBinary
	at := op == "add_node_at"
	switch {
	case at && position < 0:
		return fakeEngineError("MISSING_PARAM", "missing position (required for add_node_at)")
	case at && t.treeType != treeTypeMatrix:
		return fakeEngineError("INVALID_PARAMS", "add_node_at is only supported for matrix trees")
	case binary && (!hasPosition || position < 0):
		return fakeEngineError("MISSING_PARAM", "missing position (required for binary)")
	case at && t.root == "":
		return fakeEngineError("TREE_EMPTY", "tree has no root node")
	case at && position >= t.width:
		return fakeEngineError("INVALID_POSITION", "position %d out of range for user %s (has %d children)",
			position, parentID, t.width)
	case binary && position > 1:
		return fakeEngineError("INVALID_POSITION", "position %d out of range for user %s (has 2 children)",
			position, parentID)
	}
	if _, ok := t.nodes[userID]; ok {
		return fakeEngineError(engineCodeUserAlreadyExists, "user %s already exists in tree", userID)
	}
	if _, ok := t.nodes[parentID]; !ok {
		return fakeEngineError(engineCodeUserNotFound, "user %s not found in tree", parentID)
	}
	if _, ok := t.nodes[sponsorID]; !ok {
		return fakeEngineError("SPONSOR_NOT_FOUND", "sponsor %s not found in tree", sponsorID)
	}
	if (binary || at) && t.slotTaken(parentID, position) {
		return fakeEngineError("POSITION_OCCUPIED", "position %d already occupied for user %s", position, parentID)
	}
	return nil
}

func (t *fakeEngineTree) slotTaken(parentID string, position int) bool {
	for _, node := range t.nodes {
		if node.ParentUserID != nil && *node.ParentUserID == parentID && node.Position == position {
			return true
		}
	}
	return false
}

// checkRemoval refuses a unilevel or binary removal this tree cannot make.
func (t *fakeEngineTree) checkRemoval(userID string) error {
	if _, ok := t.nodes[userID]; !ok {
		return fakeEngineError(engineCodeUserNotFound, "user %s not found in tree", userID)
	}
	if n := t.childCount(userID); n > 0 {
		return fakeEngineError("HAS_CHILDREN", "cannot remove user %s: has %d children (must remove children first)", userID, n)
	}
	return t.checkSponsorless(userID)
}

// checkMatrixRemoval refuses a matrix removal this tree cannot make.
func (t *fakeEngineTree) checkMatrixRemoval(userID string) error {
	if _, ok := t.nodes[userID]; !ok {
		return fakeEngineError(engineCodeUserNotFound, "user %s not found in tree", userID)
	}
	if t.root == userID {
		return fakeEngineError("CANNOT_REMOVE_ROOT", "cannot remove root node")
	}
	return t.checkSponsorless(userID)
}

// checkSponsorless refuses the removal of a user who has recruits and no sponsor.
func (t *fakeEngineTree) checkSponsorless(userID string) error {
	if n := len(t.sponsored[userID]); n > 0 && t.nodes[userID].SponsorUserID == nil {
		return fakeEngineError("SPONSORLESS_WITH_RECRUITS",
			"cannot remove user %s: no node in its sponsor chain survives this removal. Recruits that outlive it: %d",
			userID, n)
	}
	return nil
}

func fakeMissingPruningMode() error {
	return fakeEngineError("MISSING_PARAM", `missing pruning_mode (must be "promote_earliest" or "holding_tank")`)
}

func (t *fakeEngineTree) childCount(userID string) int {
	n := 0
	for _, node := range t.nodes {
		if node.ParentUserID != nil && *node.ParentUserID == userID {
			n++
		}
	}
	return n
}

// moveRecruits takes userID off its sponsor's recruit list and moves its own
// recruits onto that sponsor, in list order.
func (t *fakeEngineTree) moveRecruits(userID string) []Responsored {
	moved := []Responsored{}
	sponsor := t.nodes[userID].SponsorUserID
	if sponsor == nil {
		delete(t.sponsored, userID)
		return moved
	}
	target := *sponsor
	t.dropRecruit(target, userID)
	for _, recruit := range t.sponsored[userID] {
		newSponsor := target
		pos := t.nodes[recruit]
		pos.SponsorUserID = &newSponsor
		t.nodes[recruit] = pos
		t.sponsored[target] = append(t.sponsored[target], recruit)
		moved = append(moved, Responsored{UserID: recruit, NewSponsorID: target})
	}
	delete(t.sponsored, userID)
	return moved
}

// dropRecruit takes userID off sponsorID's recruit list.
func (t *fakeEngineTree) dropRecruit(sponsorID, userID string) {
	list := slices.DeleteFunc(t.sponsored[sponsorID], func(id string) bool { return id == userID })
	if len(list) == 0 {
		delete(t.sponsored, sponsorID)
		return
	}
	t.sponsored[sponsorID] = list
}

var _ TreeRebuilder = (*fakeWriterEngine)(nil)

func newFakeWriterEngine() *fakeWriterEngine {
	return &fakeWriterEngine{trees: map[string]*fakeEngineTree{}, failAdd: map[string]error{}, failAddOnce: map[string]error{}}
}

func fakeEngineError(code, format string, args ...any) error {
	return &EngineError{Code: code, Message: fmt.Sprintf(format, args...)}
}

func fakeHandlerPanic() error {
	return fakeEngineError("INTERNAL_ERROR", "handler panicked unexpectedly")
}

func (f *fakeWriterEngine) tree(structure string) (*fakeEngineTree, error) {
	t, ok := f.trees[structure]
	if !ok {
		return nil, fakeEngineError("STRUCTURE_NOT_FOUND", "no tree named '%s'", structure)
	}
	return t, nil
}

func (f *fakeWriterEngine) CreateTree(_ context.Context, structure, treeType string) error {
	f.calls = append(f.calls, "create_tree "+treeType)
	if _, held := f.trees[structure]; !held && treeType == treeTypeMatrix {
		return fakeEngineError("MISSING_PARAM", "missing or invalid width (must be integer >= 2)")
	}
	return f.createTree(structure, treeType)
}

func (f *fakeWriterEngine) createTree(structure, treeType string) error {
	if _, ok := f.trees[structure]; ok {
		return fakeEngineError(engineCodeTreeExists, "tree '%s' already exists", structure)
	}
	f.trees[structure] = &fakeEngineTree{treeType: treeType, nodes: map[string]EnginePosition{}, sponsored: map[string][]string{}}
	return nil
}

func (f *fakeWriterEngine) CreateMatrixTree(_ context.Context, structure string, width int, spillover string) error {
	f.calls = append(f.calls, fmt.Sprintf("create_matrix_tree %d %s", width, spillover))
	if err := f.createTree(structure, treeTypeMatrix); err != nil {
		return err
	}
	f.trees[structure].width = width
	return nil
}

func (f *fakeWriterEngine) AddRoot(_ context.Context, structure, userID string, enrolledAt int64) error {
	if err, ok := f.failAddOnce[userID]; ok {
		delete(f.failAddOnce, userID)
		return err
	}
	if err, ok := f.failAdd[userID]; ok {
		return err
	}
	t, err := f.tree(structure)
	if err != nil {
		return err
	}
	if err := t.checkRoot(userID); err != nil {
		return err
	}
	t.root = userID
	t.nodes[userID] = EnginePosition{UserID: userID, EnrolledAt: enrolledAt}
	return nil
}

func (f *fakeWriterEngine) place(op, structure, userID, parentID, sponsorID string, position int, hasPosition bool, enrolledAt int64) error {
	if err, ok := f.failAddOnce[userID]; ok {
		delete(f.failAddOnce, userID)
		return err
	}
	if err, ok := f.failAdd[userID]; ok {
		return err
	}
	t, err := f.tree(structure)
	if err != nil {
		return err
	}
	if err := t.checkPlacement(op, userID, parentID, sponsorID, position, hasPosition); err != nil {
		return err
	}
	p, s := parentID, sponsorID
	t.nodes[userID] = EnginePosition{
		UserID: userID, ParentUserID: &p, SponsorUserID: &s,
		Position: position, Depth: t.nodes[parentID].Depth + 1, EnrolledAt: enrolledAt,
	}
	t.sponsored[sponsorID] = append(t.sponsored[sponsorID], userID)
	return nil
}

func (f *fakeWriterEngine) AddNode(_ context.Context, structure, userID, parentID, sponsorID string, enrolledAt int64, opts ...AddNodeOption) error {
	params := map[string]any{}
	for _, opt := range opts {
		opt(params)
	}
	position, hasPosition := params["position"].(int)
	f.placements = append(f.placements, CheckAddNode(userID, parentID, sponsorID, enrolledAt, opts...))
	return f.place("add_node", structure, userID, parentID, sponsorID, position, hasPosition, enrolledAt)
}

func (f *fakeWriterEngine) AddNodeAt(_ context.Context, structure, userID, parentID, sponsorID string, position int, enrolledAt int64) error {
	f.placements = append(f.placements, CheckAddNodeAt(userID, parentID, sponsorID, position, enrolledAt))
	return f.place("add_node_at", structure, userID, parentID, sponsorID, position, true, enrolledAt)
}

func (f *fakeWriterEngine) RemoveNode(_ context.Context, structure, userID string) ([]Responsored, error) {
	t, err := f.tree(structure)
	if err != nil {
		return nil, err
	}
	if t.treeType == treeTypeMatrix {
		return nil, fakeMissingPruningMode()
	}
	if err := t.checkRemoval(userID); err != nil {
		return nil, err
	}
	moved := t.moveRecruits(userID)
	delete(t.nodes, userID)
	if t.root == userID {
		t.root = ""
	}
	return moved, nil
}

// RemoveMatrixNode removes a leaf. It refuses a node with children rather than
// modelling promotion or the holding tank.
func (f *fakeWriterEngine) RemoveMatrixNode(_ context.Context, structure, userID string, mode PruningMode) (*MatrixRemovalResult, error) {
	t, err := f.tree(structure)
	if err != nil {
		return nil, err
	}
	if err := t.checkMatrixRemoval(userID); err != nil {
		return nil, err
	}
	for _, n := range t.nodes {
		if n.ParentUserID != nil && *n.ParentUserID == userID {
			return nil, fmt.Errorf("fakeWriterEngine removes only a matrix leaf, and %s has children", userID)
		}
	}
	moved := t.moveRecruits(userID)
	delete(t.nodes, userID)
	result := &MatrixRemovalResult{Removed: userID, Repositioned: []string{}, MovedToTank: []string{},
		Relocated: []Relocation{}, Responsored: moved}
	if mode == PruningHoldingTank {
		result.MovedToTank = []string{userID}
	}
	return result, nil
}

func (f *fakeWriterEngine) GetPosition(_ context.Context, structure, userID string) (*EnginePosition, error) {
	if f.positionErr != nil {
		return nil, f.positionErr
	}
	t, err := f.tree(structure)
	if err != nil {
		return nil, err
	}
	pos, ok := t.nodes[userID]
	if !ok {
		return nil, fakeEngineError(engineCodeUserNotFound, "user %s not found in tree", userID)
	}
	return &pos, nil
}

// GetDownline returns the fake's nodes up to depth levels below userID, or
// every level when depth is 0.
func (f *fakeWriterEngine) GetDownline(_ context.Context, structure, userID string, depth uint32) ([]EngineNode, error) {
	if err := f.downlineErrOnce; err != nil {
		f.downlineErrOnce = nil
		return nil, err
	}
	if f.downlineErr != nil {
		return nil, f.downlineErr
	}
	t, err := f.tree(structure)
	if err != nil {
		return nil, err
	}
	if _, ok := t.nodes[userID]; !ok {
		return nil, fakeEngineError(engineCodeUserNotFound, "user %s not found in tree", userID)
	}
	// A children index avoids a scan of every node per level.
	children := map[string][]string{}
	for _, id := range slices.Sorted(maps.Keys(t.nodes)) {
		if parent := t.nodes[id].ParentUserID; parent != nil {
			children[*parent] = append(children[*parent], id)
		}
	}
	type visit struct {
		id    string
		level uint32
	}
	var out []EngineNode
	queue := []visit{{id: userID}}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		if depth != 0 && v.level >= depth {
			continue
		}
		for _, child := range children[v.id] {
			pos := t.nodes[child]
			out = append(out, EngineNode{UserID: child, Depth: pos.Depth, EnrolledAt: pos.EnrolledAt})
			queue = append(queue, visit{id: child, level: v.level + 1})
		}
	}
	return out, nil
}

func (f *fakeWriterEngine) CheckMutation(_ context.Context, structure string, m Mutation) error {
	f.calls = append(f.calls, "check "+m.op)
	f.checks = append(f.checks, m)
	t, err := f.tree(structure)
	if err != nil {
		return err
	}
	user, _ := m.params["user_id"].(string)
	switch m.op {
	case "add_root":
		if err := t.checkRoot(user); err != nil {
			return err
		}
	case "add_node", "add_node_at":
		parent, _ := m.params["parent_id"].(string)
		sponsor, _ := m.params["sponsor_id"].(string)
		position, hasPosition := m.params["position"].(int)
		if err := t.checkPlacement(m.op, user, parent, sponsor, position, hasPosition); err != nil {
			return err
		}
	case "remove_node":
		if t.treeType == treeTypeMatrix {
			if _, ok := m.params["pruning_mode"]; !ok {
				return fakeMissingPruningMode()
			}
			if err := t.checkMatrixRemoval(user); err != nil {
				return err
			}
		} else if err := t.checkRemoval(user); err != nil {
			return err
		}
	}
	return f.checkErr
}

// writerEnv holds the stores one test's writes share.
type writerEnv struct {
	events    platform.EventStore
	store     *MemoryTreeStore
	snapshots *MemorySnapshotStore
	locker    *MemoryTreeLocker
}

func newWriterEnv() *writerEnv {
	return &writerEnv{
		events:    platform.NewMemoryEventStore(),
		store:     NewMemoryTreeStore(),
		snapshots: NewMemorySnapshotStore(),
		locker:    NewMemoryTreeLocker(),
	}
}

// writer builds a TreeWriter over a fresh engine.
func (e *writerEnv) writer(opts ...TreeWriterOption) (*TreeWriter, *fakeWriterEngine) {
	engine := newFakeWriterEngine()
	return NewTreeWriter(e.events, e.store, e.snapshots, engine, e.locker, opts...), engine
}

// mustAddRoot adds writerRoot as the root of writerTree through a writer.
func mustAddRoot(t *testing.T, env *writerEnv, treeType string) {
	t.Helper()
	req := AddRootRequest{
		TreeID: writerTree, UserID: writerRoot, SponsorID: writerRoot,
		TreeType: treeType, EnrolledAt: writeTime,
	}
	if treeType == treeTypeMatrix {
		width, spillover := 3, "breadth_first"
		req.MatrixWidth, req.MatrixSpillover = &width, &spillover
	}
	w, _ := env.writer()
	res, err := w.AddRoot(context.Background(), req)
	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
}

// streamEvents reads a whole stream.
func streamEvents(t *testing.T, events platform.EventStore, stream string) []platform.Event {
	t.Helper()
	got, err := events.ReadStream(context.Background(), stream, 1, 0)
	require.NoError(t, err)
	return got
}

// refusingLocker fails the test if the writer asks it for a lock.
type refusingLocker struct{ t *testing.T }

func (l refusingLocker) Lock(_ context.Context, treeID uuid.UUID) (func() error, error) {
	l.t.Errorf("Lock was called for tree %s", treeID)
	return nil, errors.New("refusingLocker: Lock was called")
}

// recordingLocker records each lock request in the engine's call log.
type recordingLocker struct {
	inner  TreeLocker
	engine *fakeWriterEngine
}

func (l recordingLocker) Lock(ctx context.Context, treeID uuid.UUID) (func() error, error) {
	l.engine.calls = append(l.engine.calls, "lock")
	return l.inner.Lock(ctx, treeID)
}

// deadlineErrLocker waits for its context to end, then returns err.
type deadlineErrLocker struct{ err error }

func (l deadlineErrLocker) Lock(ctx context.Context, _ uuid.UUID) (func() error, error) {
	<-ctx.Done()
	return nil, l.err
}

// releaseFailingLocker grants every lock and fails every release.
type releaseFailingLocker struct{ err error }

func (l releaseFailingLocker) Lock(context.Context, uuid.UUID) (func() error, error) {
	return func() error { return l.err }, nil
}

// scriptedEvents is a MemoryEventStore whose Append and ReadStream a test can
// script once its setup is done.
type scriptedEvents struct {
	*platform.MemoryEventStore
	// afterAppend runs once, after the next Append, failed or not.
	afterAppend func()
	// readAs replaces what ReadStream returns when read from readAsVersion.
	readAs        *platform.Event
	readAsVersion int64
	// appendErr is what the next Append returns. commitFirst writes its events
	// to the inner store before returning it.
	appendErr   error
	commitFirst bool
	// readErr is what the first ReadStream after a scripted Append failure
	// returns.
	readErr error
	failed  bool
}

func (s *scriptedEvents) Append(ctx context.Context, stream string, expected int64, events []platform.NewEvent) error {
	if s.appendErr != nil {
		err := s.appendErr
		s.appendErr, s.failed = nil, true
		if s.commitFirst {
			if innerErr := s.MemoryEventStore.Append(ctx, stream, expected, events); innerErr != nil {
				return innerErr
			}
		}
		s.runAfterAppend()
		return err
	}
	if err := s.MemoryEventStore.Append(ctx, stream, expected, events); err != nil {
		return err
	}
	s.runAfterAppend()
	return nil
}

func (s *scriptedEvents) runAfterAppend() {
	if s.afterAppend != nil {
		hook := s.afterAppend
		s.afterAppend = nil
		hook()
	}
}

func (s *scriptedEvents) ReadStream(ctx context.Context, stream string, from, limit int64) ([]platform.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.failed && s.readErr != nil {
		err := s.readErr
		s.readErr = nil
		return nil, err
	}
	if s.readAs != nil && from == s.readAsVersion {
		return []platform.Event{*s.readAs}, nil
	}
	return s.MemoryEventStore.ReadStream(ctx, stream, from, limit)
}

// mustPlace places user under writerRoot through a writer.
func mustPlace(t *testing.T, env *writerEnv, user string, position *int) WriteResult {
	t.Helper()
	w, _ := env.writer()
	res, err := w.Place(context.Background(), PlaceRequest{
		TreeID: writerTree, UserID: user, ParentID: writerRoot, SponsorID: writerRoot,
		Position: position, EnrolledAt: writeTime,
	})
	require.NoError(t, err)
	require.NoError(t, res.ProjectionErr)
	return res
}

// appendDirect appends one event to writerTree's stream without projecting it.
func appendDirect(t *testing.T, events platform.EventStore, eventType string, payload any) platform.Event {
	t.Helper()
	ctx := context.Background()
	stream := TreeStreamName(writerTree)
	last, err := events.ReadLastEvent(ctx, stream)
	require.NoError(t, err)
	var expected int64
	if last != nil {
		expected = last.Version
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NoError(t, events.Append(ctx, stream, expected, []platform.NewEvent{{
		ID: uuid.NewString(), Type: eventType, Payload: data,
	}}))
	stored, err := events.ReadStream(ctx, stream, expected+1, 1)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	return stored[0]
}

// orderLog records calls across wrapped stores in the order they happen.
type orderLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *orderLog) add(entry string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
}

func (l *orderLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.entries)
}

// indexOf returns the position of the first matching entry, or -1.
func (l *orderLog) indexOf(entry string) int {
	return slices.Index(l.snapshot(), entry)
}

// waitFor reports whether the entry appeared within timeout.
func (l *orderLog) waitFor(entry string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if l.indexOf(entry) >= 0 {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// loggingStore records each insert and each full-tree load.
type loggingStore struct {
	TreeStore
	log  *orderLog
	name string
}

func (s *loggingStore) ProjectInsert(ctx context.Context, node TreeNodeRow, eventVersion int64) error {
	s.log.add(s.name + " insert " + node.UserID)
	return s.TreeStore.ProjectInsert(ctx, node, eventVersion)
}

func (s *loggingStore) ProjectedRows(ctx context.Context, treeID string) (int64, bool, []TreeNodeRow, error) {
	s.log.add(s.name + " load")
	return s.TreeStore.ProjectedRows(ctx, treeID)
}

// loggingEvents records each append by the version it asks for.
type loggingEvents struct {
	platform.EventStore
	log *orderLog
}

func (e *loggingEvents) Append(ctx context.Context, stream string, expected int64, events []platform.NewEvent) error {
	e.log.add(fmt.Sprintf("append at %d", expected+1))
	return e.EventStore.Append(ctx, stream, expected, events)
}

// hookLocker runs before once, then asks inner for the lock.
type hookLocker struct {
	inner  TreeLocker
	before func()
}

func (l *hookLocker) Lock(ctx context.Context, treeID uuid.UUID) (func() error, error) {
	if l.before != nil {
		hook := l.before
		l.before = nil
		hook()
	}
	return l.inner.Lock(ctx, treeID)
}

// immediateErrLocker returns err without waiting.
type immediateErrLocker struct{ err error }

func (l immediateErrLocker) Lock(context.Context, uuid.UUID) (func() error, error) {
	return nil, l.err
}

func TestFakeWriterEngine_GetDownlineReturnsEveryDescendant(t *testing.T) {
	engine := newFakeWriterEngine()
	ctx := context.Background()
	require.NoError(t, engine.CreateTree(ctx, writerTree, treeTypeUnilevel))
	require.NoError(t, engine.AddRoot(ctx, writerTree, writerRoot, 1))
	require.NoError(t, engine.AddNode(ctx, writerTree, writerChild, writerRoot, writerRoot, 2))
	require.NoError(t, engine.AddNode(ctx, writerTree, writerOther, writerChild, writerChild, 3))

	got, err := engine.GetDownline(ctx, writerTree, writerRoot, 0)

	require.NoError(t, err)
	assert.Equal(t, []EngineNode{
		{UserID: writerChild, Depth: 1, EnrolledAt: 2},
		{UserID: writerOther, Depth: 2, EnrolledAt: 3},
	}, got)
}

func TestFakeWriterEngine_GetDownlineStopsAtTheDepthAsked(t *testing.T) {
	engine := newFakeWriterEngine()
	ctx := context.Background()
	require.NoError(t, engine.CreateTree(ctx, writerTree, treeTypeUnilevel))
	require.NoError(t, engine.AddRoot(ctx, writerTree, writerRoot, 1))
	require.NoError(t, engine.AddNode(ctx, writerTree, writerChild, writerRoot, writerRoot, 2))
	require.NoError(t, engine.AddNode(ctx, writerTree, writerOther, writerChild, writerChild, 3))

	got, err := engine.GetDownline(ctx, writerTree, writerRoot, 1)

	require.NoError(t, err)
	assert.Equal(t, []EngineNode{{UserID: writerChild, Depth: 1, EnrolledAt: 2}}, got)
}

func TestFakeWriterEngine_ListsRecruitsInTheOrderTheyArePlaced(t *testing.T) {
	engine := newRootedFakeEngine(t, treeTypeUnilevel)
	ctx := context.Background()
	require.NoError(t, engine.AddNode(ctx, writerTree, writerChild, writerRoot, writerRoot, 2))
	require.NoError(t, engine.AddNode(ctx, writerTree, writerOther, writerRoot, writerChild, 3))
	require.NoError(t, engine.AddNode(ctx, writerTree, writerRemoved, writerRoot, writerRoot, 4))

	assert.Equal(t, map[string][]string{
		writerRoot:  {writerChild, writerRemoved},
		writerChild: {writerOther},
	}, engine.trees[writerTree].sponsored)
}

func TestFakeWriterEngine_PlacesIntoARestoredRootOnlyTree(t *testing.T) {
	engine := newFakeWriterEngine()
	ctx := context.Background()
	data := `{"root":"` + writerRoot + `","nodes":{"` + writerRoot + `":{"user_id":"` + writerRoot + `"}}}`
	require.NoError(t, engine.RestoreSnapshot(ctx, writerTree, treeTypeUnilevel, json.RawMessage(data)))

	require.NoError(t, engine.AddNode(ctx, writerTree, writerChild, writerRoot, writerRoot, 2))

	assert.Equal(t, []string{writerChild}, engine.trees[writerTree].sponsored[writerRoot])
}

// fakeSnapshot is the fake engine's snapshot payload.
type fakeSnapshot struct {
	Width     int                       `json:"width,omitempty"`
	Root      string                    `json:"root"`
	Nodes     map[string]EnginePosition `json:"nodes"`
	Sponsored map[string][]string       `json:"sponsored,omitempty"`
}

func (f *fakeWriterEngine) TakeSnapshot(ctx context.Context, structure string) (*SnapshotResultDTO, error) {
	f.calls = append(f.calls, "take_snapshot")
	if f.takeErr != nil {
		return nil, f.takeErr
	}
	if _, held := f.trees[structure]; !held {
		return nil, fakeEngineError("STRUCTURE_NOT_FOUND", "structure '%s' not found", structure)
	}
	if f.retypeBeforeTake != "" {
		if _, err := f.DropStructure(ctx, structure); err != nil {
			return nil, err
		}
		if err := f.CreateTree(ctx, structure, f.retypeBeforeTake); err != nil {
			return nil, err
		}
	}
	t, err := f.tree(structure)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(fakeSnapshot{Width: t.width, Root: t.root, Nodes: t.nodes, Sponsored: t.sponsored})
	if err != nil {
		return nil, err
	}
	return &SnapshotResultDTO{TreeType: t.treeType, Data: data}, nil
}

func (f *fakeWriterEngine) RestoreSnapshot(_ context.Context, structure, treeType string, data json.RawMessage) error {
	f.calls = append(f.calls, "restore_snapshot")
	if f.restoreErr != nil {
		return f.restoreErr
	}
	if _, ok := f.trees[structure]; ok {
		return fakeEngineError(engineCodeTreeExists,
			"structure '%s' already exists; drop it with drop_structure before restoring", structure)
	}
	var snap fakeSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return fakeEngineError("INVALID_PARAMS", "%s", err)
	}
	if treeType == treeTypeMatrix {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err == nil {
			if _, ok := fields["width"]; !ok {
				return fakeEngineError("INVALID_PARAMS", "failed to deserialize matrix snapshot: missing field `width`")
			}
		}
		if snap.Width < 2 {
			return fakeEngineError("INCONSISTENT_SNAPSHOT", "the tree declares a width of %d; a matrix width is at least 2", snap.Width)
		}
	}
	if snap.Nodes == nil {
		snap.Nodes = map[string]EnginePosition{}
	}
	if snap.Sponsored == nil {
		snap.Sponsored = map[string][]string{}
	}
	if err := checkRestored(snap); err != nil {
		return err
	}
	f.trees[structure] = &fakeEngineTree{treeType: treeType, width: snap.Width, root: snap.Root, nodes: snap.Nodes,
		sponsored: snap.Sponsored}
	return nil
}

func (f *fakeWriterEngine) DropStructure(_ context.Context, structure string) (bool, error) {
	f.calls = append(f.calls, "drop_structure")
	if f.dropErr != nil {
		return false, f.dropErr
	}
	if f.dropReportsAbsent {
		delete(f.trees, structure)
		return false, nil
	}
	_, ok := f.trees[structure]
	delete(f.trees, structure)
	return ok, nil
}

// checkRestored refuses a snapshot that fails one of its root, parent, sponsor or recruit-list checks.
func checkRestored(snap fakeSnapshot) error {
	if _, ok := snap.Nodes[snap.Root]; snap.Root != "" && !ok {
		return fakeEngineError("INCONSISTENT_SNAPSHOT", "root names %s; the snapshot holds no such node", snap.Root)
	}
	ids := slices.Sorted(maps.Keys(snap.Nodes))
	owners := slices.Sorted(maps.Keys(snap.Sponsored))
	for _, id := range ids {
		parent := snap.Nodes[id].ParentUserID
		if parent == nil {
			continue
		}
		if _, ok := snap.Nodes[*parent]; !ok {
			return fakeEngineError("INCONSISTENT_SNAPSHOT", "parent on node %s names %s; the snapshot holds no such node",
				id, *parent)
		}
	}
	for _, id := range ids {
		sponsor := snap.Nodes[id].SponsorUserID
		if sponsor == nil {
			continue
		}
		if _, ok := snap.Nodes[*sponsor]; !ok {
			return fakeEngineError("INCONSISTENT_SNAPSHOT", "sponsor on node %s names %s; the snapshot holds no such node",
				id, *sponsor)
		}
	}
	for _, owner := range owners {
		for _, recruit := range snap.Sponsored[owner] {
			if _, ok := snap.Nodes[recruit]; !ok {
				return fakeEngineError("INCONSISTENT_SNAPSHOT",
					"the recruit list of %s names %s; the snapshot holds no such node", owner, recruit)
			}
		}
	}
	for _, owner := range owners {
		if _, ok := snap.Nodes[owner]; !ok {
			return fakeEngineError("INCONSISTENT_SNAPSHOT", "a recruit list belongs to %s; the snapshot holds no such node",
				owner)
		}
	}
	if snap.Root == "" && len(snap.Nodes) > 0 {
		return fakeEngineError("INCONSISTENT_SNAPSHOT", "the snapshot holds %d nodes and names no root", len(snap.Nodes))
	}
	for _, id := range ids {
		if snap.Nodes[id].ParentUserID == nil && id != snap.Root {
			return fakeEngineError("INCONSISTENT_SNAPSHOT", "node %s names no parent and is not the root %s", id, snap.Root)
		}
	}
	for _, owner := range owners {
		seen := map[string]bool{}
		for _, recruit := range snap.Sponsored[owner] {
			if seen[recruit] {
				return fakeEngineError("INCONSISTENT_SNAPSHOT", "the recruit list of %s names %s more than once",
					owner, recruit)
			}
			seen[recruit] = true
			sponsor := snap.Nodes[recruit].SponsorUserID
			if sponsor == nil {
				return fakeEngineError("INCONSISTENT_SNAPSHOT", "the recruit list of %s names %s, who has no sponsor",
					owner, recruit)
			}
			if *sponsor != owner {
				return fakeEngineError("INCONSISTENT_SNAPSHOT", "the recruit list of %s names %s, whose sponsor is %s",
					owner, recruit, *sponsor)
			}
		}
	}
	for _, id := range ids {
		sponsor := snap.Nodes[id].SponsorUserID
		if sponsor != nil && !slices.Contains(snap.Sponsored[*sponsor], id) {
			return fakeEngineError("INCONSISTENT_SNAPSHOT", "%s names sponsor %s; the recruit list of %s does not name it",
				id, *sponsor, *sponsor)
		}
	}
	return nil
}

func TestFakeWriterEngine_RestoresWhatItSnapshotted(t *testing.T) {
	engine := newFakeWriterEngine()
	ctx := context.Background()
	require.NoError(t, engine.CreateTree(ctx, writerTree, treeTypeUnilevel))
	require.NoError(t, engine.AddRoot(ctx, writerTree, writerRoot, 1))
	taken, err := engine.TakeSnapshot(ctx, writerTree)
	require.NoError(t, err)
	assert.Equal(t, treeTypeUnilevel, taken.TreeType)
	dropped, err := engine.DropStructure(ctx, writerTree)
	require.NoError(t, err)
	require.True(t, dropped)

	require.NoError(t, engine.RestoreSnapshot(ctx, writerTree, taken.TreeType, taken.Data))

	pos, err := engine.GetPosition(ctx, writerTree, writerRoot)
	require.NoError(t, err)
	assert.Equal(t, int64(1), pos.EnrolledAt)
	assert.True(t, isEngineCode(engine.AddRoot(ctx, writerTree, writerOther, 2), engineCodeRootAlreadyExists))
	assert.True(t, isEngineCode(engine.CheckMutation(ctx, writerTree,
		CheckAddNodeAt(writerOther, writerRoot, writerRoot, 0, 2)), "INVALID_PARAMS"),
		"add_node_at on the restored tree was not refused with INVALID_PARAMS; the restored tree type is %q",
		engine.trees[writerTree].treeType)
	err = engine.RestoreSnapshot(ctx, writerTree, taken.TreeType, taken.Data)
	assert.True(t, isEngineCode(err, engineCodeTreeExists))
}

func TestFakeWriterEngine_AcceptsAFreeSlotBesideAHeldOne(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		treeType string
		place    func(*fakeWriterEngine) error
		check    Mutation
	}{
		"matrix": {treeTypeMatrix,
			func(e *fakeWriterEngine) error {
				return e.AddNodeAt(ctx, writerTree, writerOther, writerRoot, writerRoot, 1, 3)
			},
			CheckAddNodeAt(writerOther, writerRoot, writerRoot, 1, 3)},
		"binary": {treeTypeBinary,
			func(e *fakeWriterEngine) error {
				return e.AddNode(ctx, writerTree, writerOther, writerRoot, writerRoot, 3, WithPosition(1))
			},
			CheckAddNode(writerOther, writerRoot, writerRoot, 3, WithPosition(1))},
	} {
		t.Run(name, func(t *testing.T) {
			engine := newFakeWriterEngine()
			agreementWithChild(tc.treeType)(t, engine)

			require.NoError(t, engine.CheckMutation(ctx, writerTree, tc.check))
			require.NoError(t, tc.place(engine))
			assert.Equal(t, 1, engine.trees[writerTree].nodes[writerOther].Position)
		})
	}
}

func TestFakeWriterEngine_RestoresATreeAfterASponsorIsRemoved(t *testing.T) {
	engine := newFakeWriterEngine()
	ctx := context.Background()
	require.NoError(t, engine.CreateTree(ctx, writerTree, treeTypeUnilevel))
	require.NoError(t, engine.AddRoot(ctx, writerTree, writerRoot, 1))
	require.NoError(t, engine.AddNode(ctx, writerTree, writerChild, writerRoot, writerRoot, 2))
	require.NoError(t, engine.AddNode(ctx, writerTree, writerOther, writerRoot, writerChild, 3))
	_, err := engine.RemoveNode(ctx, writerTree, writerChild)
	require.NoError(t, err)
	taken, err := engine.TakeSnapshot(ctx, writerTree)
	require.NoError(t, err)
	_, err = engine.DropStructure(ctx, writerTree)
	require.NoError(t, err)

	require.NoError(t, engine.RestoreSnapshot(ctx, writerTree, taken.TreeType, taken.Data))

	pos, err := engine.GetPosition(ctx, writerTree, writerOther)
	require.NoError(t, err)
	assert.Equal(t, int64(3), pos.EnrolledAt)
	require.NotNil(t, pos.SponsorUserID)
	assert.Equal(t, writerRoot, *pos.SponsorUserID)
}

func TestFakeWriterEngine_RefusesASnapshotWithAnAbsentParent(t *testing.T) {
	engine := newFakeWriterEngine()
	data := `{"root":"` + writerRoot + `","nodes":{"` + writerRoot + `":{"user_id":"` + writerRoot + `"},"` +
		writerOther + `":{"user_id":"` + writerOther + `","parent_user_id":"` + writerChild + `"}}}`

	err := engine.RestoreSnapshot(context.Background(), writerTree, treeTypeUnilevel, json.RawMessage(data))

	requireEngineRefusal(t, err, "INCONSISTENT_SNAPSHOT",
		"parent on node "+writerOther+" names "+writerChild+"; the snapshot holds no such node")
}

func TestFakeWriterEngine_RestoresAMatrixWithItsWidth(t *testing.T) {
	engine := newFakeWriterEngine()
	ctx := context.Background()
	require.NoError(t, engine.CreateMatrixTree(ctx, writerTree, 5, "breadth_first"))
	require.NoError(t, engine.AddRoot(ctx, writerTree, writerRoot, 1))
	taken, err := engine.TakeSnapshot(ctx, writerTree)
	require.NoError(t, err)
	_, err = engine.DropStructure(ctx, writerTree)
	require.NoError(t, err)

	require.NoError(t, engine.RestoreSnapshot(ctx, writerTree, taken.TreeType, taken.Data))

	require.NoError(t, engine.CheckMutation(ctx, writerTree, CheckAddNodeAt(writerChild, writerRoot, writerRoot, 4, 2)))
	err = engine.CheckMutation(ctx, writerTree, CheckAddNodeAt(writerChild, writerRoot, writerRoot, 5, 2))
	requireEngineRefusal(t, err, "INVALID_POSITION", "position 5 out of range for user "+writerRoot+" (has 5 children)")
}

func TestFakeWriterEngine_RefusesAMatrixSnapshotWithoutAUsableWidth(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		data          string
		code, message string
	}{
		"no width": {`{"root":"","nodes":{}}`, "INVALID_PARAMS",
			"failed to deserialize matrix snapshot: missing field `width`"},
		"width 0": {`{"width":0,"root":"","nodes":{}}`, "INCONSISTENT_SNAPSHOT",
			"the tree declares a width of 0; a matrix width is at least 2"},
		"width 1": {`{"width":1,"root":"","nodes":{}}`, "INCONSISTENT_SNAPSHOT",
			"the tree declares a width of 1; a matrix width is at least 2"},
	} {
		t.Run(name, func(t *testing.T) {
			engine := newFakeWriterEngine()

			err := engine.RestoreSnapshot(ctx, writerTree, treeTypeMatrix, json.RawMessage(tc.data))

			requireEngineRefusal(t, err, tc.code, tc.message)
			assert.NotContains(t, engine.trees, writerTree)
		})
	}
	t.Run("width 2", func(t *testing.T) {
		engine := newFakeWriterEngine()

		require.NoError(t, engine.RestoreSnapshot(ctx, writerTree, treeTypeMatrix,
			json.RawMessage(`{"width":2,"root":"","nodes":{}}`)))
	})
	t.Run("no width, onto a held structure", func(t *testing.T) {
		engine := newFakeWriterEngine()
		require.NoError(t, engine.CreateMatrixTree(ctx, writerTree, 3, "breadth_first"))

		err := engine.RestoreSnapshot(ctx, writerTree, treeTypeMatrix, json.RawMessage(`{"root":"","nodes":{}}`))

		requireEngineRefusal(t, err, engineCodeTreeExists,
			"structure '"+writerTree+"' already exists; drop it with drop_structure before restoring")
	})
}

func TestFakeWriterEngine_CreatesAMatrixOnlyWithAWidth(t *testing.T) {
	binary := findWorkerBinary(t)
	ctx := context.Background()
	client, err := NewEngineClient(ctx, binary)
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()
	fake := newFakeWriterEngine()

	workerErr := client.CreateTree(ctx, writerTree, treeTypeMatrix)
	fakeErr := fake.CreateTree(ctx, writerTree, treeTypeMatrix)

	require.Equal(t, "MISSING_PARAM", engineCodeOf(workerErr), "the worker's answer")
	assert.Equal(t, engineErrorOf(workerErr), engineErrorOf(fakeErr))
	assert.NotContains(t, fake.trees, writerTree)

	require.NoError(t, client.CreateMatrixTree(ctx, writerTree, 3, "breadth_first"))
	require.NoError(t, fake.CreateMatrixTree(ctx, writerTree, 3, "breadth_first"))
	workerErr = client.CreateTree(ctx, writerTree, treeTypeMatrix)
	fakeErr = fake.CreateTree(ctx, writerTree, treeTypeMatrix)

	require.Equal(t, engineCodeTreeExists, engineCodeOf(workerErr), "the worker's answer for a held structure")
	assert.Equal(t, engineErrorOf(workerErr), engineErrorOf(fakeErr), "a held structure")
}

func TestFakeWriterEngine_TakesNoSnapshotOfAnAbsentStructure(t *testing.T) {
	binary := findWorkerBinary(t)
	ctx := context.Background()
	client, err := NewEngineClient(ctx, binary)
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()
	fake := newFakeWriterEngine()

	_, workerErr := client.TakeSnapshot(ctx, writerTree)
	_, fakeErr := fake.TakeSnapshot(ctx, writerTree)
	fake.retypeBeforeTake = treeTypeBinary
	_, retypedErr := fake.TakeSnapshot(ctx, writerTree)

	require.Equal(t, "STRUCTURE_NOT_FOUND", engineCodeOf(workerErr), "the worker's answer")
	assert.Equal(t, engineErrorOf(workerErr), engineErrorOf(fakeErr))
	assert.Equal(t, engineErrorOf(workerErr), engineErrorOf(retypedErr), "with a retype set")
	assert.NotContains(t, fake.trees, writerTree)
}

func TestFakeWriterEngine_RefusesAMatrixRemovalCheckWithNoMode(t *testing.T) {
	ctx := context.Background()
	f := newFakeWriterEngine()
	require.NoError(t, f.CreateMatrixTree(ctx, writerTree, 3, "breadth_first"))
	require.NoError(t, f.AddRoot(ctx, writerTree, writerRoot, writeTime.Unix()))
	require.NoError(t, f.AddNodeAt(ctx, writerTree, writerChild, writerRoot, writerRoot, 0, writeTime.Unix()))

	assert.True(t, isEngineCode(f.CheckMutation(ctx, writerTree, CheckRemoveNode(writerChild)), "MISSING_PARAM"))
	assert.NoError(t, f.CheckMutation(ctx, writerTree, CheckRemoveMatrixNode(writerChild, PruningHoldingTank)))
}
