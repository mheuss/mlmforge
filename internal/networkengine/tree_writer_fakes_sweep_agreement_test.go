package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	sweepSeedsEnv            = "FAKE_SWEEP_SEEDS"
	sweepDefaultFirst        = 1
	sweepDefaultLast         = 200
	sweepNonLeafPromoteFloor = 600
	sweepMostChildrenFloor   = 500
	sweepNonLeafPct          = 90
	sweepMostChildrenPct     = 75
	sweepPromotePct          = 90
	sweepRemovalAttempts     = 6
)

// parseSweepSeeds returns the inclusive seed range a FAKE_SWEEP_SEEDS value names.
func parseSweepSeeds(value string) (int, int, error) {
	if value == "" {
		return 0, 0, fmt.Errorf("%s is set but empty", sweepSeedsEnv)
	}
	a, b, ok := strings.Cut(value, "-")
	first, errFirst := strconv.Atoi(a)
	last, errLast := strconv.Atoi(b)
	if ok && (errors.Is(errFirst, strconv.ErrRange) || errors.Is(errLast, strconv.ErrRange)) {
		return 0, 0, fmt.Errorf("%s=%q holds a number that does not fit in an int", sweepSeedsEnv, value)
	}
	if !ok || errFirst != nil || errLast != nil {
		return 0, 0, fmt.Errorf("%s=%q is not two integers joined by -", sweepSeedsEnv, value)
	}
	if first < 1 || last < 1 {
		return 0, 0, fmt.Errorf("%s=%q holds a number below 1", sweepSeedsEnv, value)
	}
	if first > last {
		return 0, 0, fmt.Errorf("%s=%q has %d above %d", sweepSeedsEnv, value, first, last)
	}
	return first, last, nil
}

// sweepPosition is the part of a user's position the sweep compares.
type sweepPosition struct {
	Parent, Sponsor string
	Slot            int
	Depth           uint32
	EnrolledAt      int64
	Err             string
}

// sweepStats counts the sweep's removals by outcome and by the shape of their target.
type sweepStats struct {
	accepted     map[PruningMode]int
	nonLeaf      map[PruningMode]int
	mostChildren int
	refused      int
}

// sweepCase is one seed's tree on both engines and the log of what was done to it.
type sweepCase struct {
	seed      int
	unbiased  bool
	width     int
	structure string
	client    *EngineClient
	fake      *fakeWriterEngine
	users     []string
	log       []string
	stopped   *bool
}

func sweepShort(user string) string {
	if len(user) < 24 {
		return user
	}
	return user[24:]
}

// checkWorker fails the seed and marks the run stopped when err carries no engine code.
func (c *sweepCase) checkWorker(t *testing.T, call string, err error) {
	t.Helper()
	var engineErr *EngineError
	if err == nil || errors.As(err, &engineErr) {
		return
	}
	*c.stopped = true
	t.Fatalf("seed %d: the worker's %s returned an error with no engine code: %v", c.seed, call, err)
}

func sweepPositionOf(e removalAgreementEngine, structure, user string) (sweepPosition, error) {
	pos, err := e.GetPosition(context.Background(), structure, user)
	if err != nil {
		return sweepPosition{Err: engineErrorOf(err)}, err
	}
	p := sweepPosition{Slot: pos.Position, Depth: pos.Depth, EnrolledAt: pos.EnrolledAt}
	if pos.ParentUserID != nil {
		p.Parent = *pos.ParentUserID
	}
	if pos.SponsorUserID != nil {
		p.Sponsor = *pos.SponsorUserID
	}
	return p, nil
}

// sweepTank returns the holding tank in a snapshot and whether its key is present.
func sweepTank(t *testing.T, data json.RawMessage) ([]string, bool) {
	t.Helper()
	var tree map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &tree))
	raw, ok := tree["holding_tank"]
	if !ok {
		return nil, false
	}
	var tank []HoldingTankEntryDTO
	require.NoError(t, json.Unmarshal(raw, &tank))
	var out []string
	for _, e := range tank {
		sponsor := ""
		if e.SponsorUserID != nil {
			sponsor = sweepShort(*e.SponsorUserID)
		}
		out = append(out, fmt.Sprintf("%s<-%s@%d", sweepShort(e.UserID), sponsor, e.EnrolledAt))
	}
	return out, true
}

func sweepResult(r *MatrixRemovalResult) string {
	if r == nil {
		return "<nil>"
	}
	promoted := ""
	if r.Promoted != nil {
		promoted = *r.Promoted
	}
	return fmt.Sprintf("removed=%s promoted=%s repositioned=%v tank=%v relocated=%+v responsored=%+v",
		r.Removed, promoted, r.Repositioned, r.MovedToTank, r.Relocated, r.Responsored)
}

// compare returns the differences between the worker and the fake, and the worker's position of each present user.
func (c *sweepCase) compare(t *testing.T) ([]string, map[string]sweepPosition) {
	t.Helper()
	ctx := context.Background()
	var diffs []string
	workerSnap, err := c.client.TakeSnapshot(ctx, c.structure)
	c.checkWorker(t, "TakeSnapshot", err)
	require.NoError(t, err, "the worker's snapshot")
	fakeSnap, err := c.fake.TakeSnapshot(ctx, c.structure)
	require.NoError(t, err, "the fake's snapshot")
	workerTank, workerKey := sweepTank(t, workerSnap.Data)
	fakeTank, fakeKey := sweepTank(t, fakeSnap.Data)
	if workerKey != fakeKey {
		diffs = append(diffs, fmt.Sprintf("holding_tank key present: worker %v, fake %v", workerKey, fakeKey))
	}
	if fmt.Sprint(workerTank) != fmt.Sprint(fakeTank) {
		diffs = append(diffs, fmt.Sprintf("holding tank: worker %v, fake %v", workerTank, fakeTank))
	}
	workerRecruits := workerListsOf(t, workerSnap.Data)
	var fakeFields fakeSnapshot
	require.NoError(t, json.Unmarshal(fakeSnap.Data, &fakeFields), "the fake's snapshot")
	fakeRecruits := fakeFields.Sponsored
	if fmt.Sprint(workerRecruits) != fmt.Sprint(fakeRecruits) {
		diffs = append(diffs, fmt.Sprintf("recruit lists: worker %v, fake %v", workerRecruits, fakeRecruits))
	}
	present := map[string]sweepPosition{}
	for _, u := range c.users {
		workerPos, workerErr := sweepPositionOf(c.client, c.structure, u)
		c.checkWorker(t, "GetPosition", workerErr)
		fakePos, _ := sweepPositionOf(c.fake, c.structure, u)
		if workerPos != fakePos {
			diffs = append(diffs, fmt.Sprintf("position of %s: worker %+v, fake %+v", sweepShort(u), workerPos, fakePos))
		}
		if workerErr == nil {
			present[u] = workerPos
		}
	}
	return diffs, present
}

// sweepSeed builds one seed's layout on both engines, then removes from it and compares after each step.
func sweepSeed(t *testing.T, client *EngineClient, seed int, stats *sweepStats, stopped *bool) {
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(uint64(seed), 1090))
	c := &sweepCase{
		seed: seed, unbiased: seed%4 == 0, structure: testTreeUUID(10000 + seed),
		client: client, fake: newFakeWriterEngine(), stopped: stopped,
	}
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		testName, _, _ := strings.Cut(t.Name(), "/")
		kind := "biased"
		if c.unbiased {
			kind = "unbiased"
		}
		t.Logf("seed %d (%s), width %d\n  %s\nrerun: %s=%d-%d go test -run '^%s$/^seed_%d$' ./internal/networkengine/",
			c.seed, kind, c.width, strings.Join(c.log, "\n  "), sweepSeedsEnv, c.seed, c.seed, testName, c.seed)
	})

	var n int
	if c.unbiased {
		c.width = 2 + rng.IntN(3)
		n = 3 + rng.IntN(25)
	} else {
		c.width = 3 + rng.IntN(2)
		n = 8 + rng.IntN(20)
	}
	root := testUserUUID(500)
	err := client.CreateMatrixTree(ctx, c.structure, c.width, "breadth_first")
	c.checkWorker(t, "CreateMatrixTree", err)
	require.NoError(t, err, "the worker's CreateMatrixTree")
	t.Cleanup(func() {
		if *c.stopped {
			return
		}
		dropped, err := client.DropStructure(ctx, c.structure)
		var engineErr *EngineError
		if err != nil && !errors.As(err, &engineErr) {
			*c.stopped = true
			t.Errorf("seed %d: the worker's DropStructure returned an error with no engine code: %v", c.seed, err)
			return
		}
		if err != nil || !dropped {
			t.Errorf("seed %d: the worker's DropStructure returned dropped=%v, err=%v", c.seed, dropped, err)
		}
	})
	require.NoError(t, c.fake.CreateMatrixTree(ctx, c.structure, c.width, "breadth_first"), "the fake's CreateMatrixTree")
	err = client.AddRoot(ctx, c.structure, root, 100)
	c.checkWorker(t, "AddRoot", err)
	require.NoError(t, err, "the worker's AddRoot")
	require.NoError(t, c.fake.AddRoot(ctx, c.structure, root, 100), "the fake's AddRoot")

	c.users = []string{root}
	children := map[string]int{root: 0}
	slotsUsed := map[string]map[int]bool{root: {}}
	ids := rng.Perm(n)
	for i := range n {
		u := testUserUUID(600 + ids[i])
		var parents []string
		for _, p := range c.users {
			if children[p] < c.width {
				parents = append(parents, p)
			}
		}
		parent := parents[rng.IntN(len(parents))]
		var free []int
		for s := range c.width {
			if !slotsUsed[parent][s] {
				free = append(free, s)
			}
		}
		slot := free[rng.IntN(len(free))]
		sponsor := c.users[rng.IntN(len(c.users))]
		enrolledAt := int64(200 + rng.IntN(6)*10)
		c.log = append(c.log, fmt.Sprintf("place %s parent=%s sponsor=%s slot=%d at=%d",
			sweepShort(u), sweepShort(parent), sweepShort(sponsor), slot, enrolledAt))
		err := client.AddNodeAt(ctx, c.structure, u, parent, sponsor, slot, enrolledAt)
		c.checkWorker(t, "AddNodeAt", err)
		require.NoError(t, err, "the worker's AddNodeAt")
		require.NoError(t, c.fake.AddNodeAt(ctx, c.structure, u, parent, sponsor, slot, enrolledAt), "the fake's AddNodeAt")
		c.users = append(c.users, u)
		children[parent]++
		slotsUsed[parent][slot] = true
		slotsUsed[u] = map[int]bool{}
		children[u] = 0
	}

	diffs, state := c.compare(t)
	if len(diffs) > 0 {
		t.Errorf("seed %d: differences after the placements:\n  %s", seed, strings.Join(diffs, "\n  "))
		return
	}

	nonLeafPct, promotePct := sweepNonLeafPct, sweepPromotePct
	if c.unbiased {
		nonLeafPct, promotePct = 0, 50
	}
	for step := 1; step <= sweepRemovalAttempts; step++ {
		kids := map[string]int{}
		for _, p := range state {
			if p.Parent != "" {
				kids[p.Parent]++
			}
		}
		var cands, nonLeaf []string
		for _, u := range c.users {
			if _, ok := state[u]; ok && u != root {
				cands = append(cands, u)
				if kids[u] > 0 {
					nonLeaf = append(nonLeaf, u)
				}
			}
		}
		if len(cands) == 0 {
			return
		}
		pick := cands
		if len(nonLeaf) > 0 && rng.IntN(100) < nonLeafPct {
			pick = nonLeaf
			if !c.unbiased && rng.IntN(100) < sweepMostChildrenPct {
				pick = withMostChildren(nonLeaf, kids)
			}
		}
		x := pick[rng.IntN(len(pick))]
		mode := PruningHoldingTank
		if rng.IntN(100) < promotePct {
			mode = PruningPromoteEarliest
		}
		hasKids := kids[x] > 0
		mostAmongCands := 0
		for _, u := range cands {
			mostAmongCands = max(mostAmongCands, kids[u])
		}
		c.log = append(c.log, fmt.Sprintf("remove %s mode=%s", sweepShort(x), mode))

		check := CheckRemoveMatrixNode(x, mode)
		workerCheck := client.CheckMutation(ctx, c.structure, check)
		c.checkWorker(t, "CheckMutation", workerCheck)
		fakeCheck := c.fake.CheckMutation(ctx, c.structure, check)
		workerResult, workerErr := client.RemoveMatrixNode(ctx, c.structure, x, mode)
		c.checkWorker(t, "RemoveMatrixNode", workerErr)
		fakeResult, fakeErr := c.fake.RemoveMatrixNode(ctx, c.structure, x, mode)

		var stepDiffs []string
		if engineErrorOf(workerCheck) != engineErrorOf(fakeCheck) {
			stepDiffs = append(stepDiffs, fmt.Sprintf("check: worker %q, fake %q", engineErrorOf(workerCheck), engineErrorOf(fakeCheck)))
		}
		if engineErrorOf(workerErr) != engineErrorOf(fakeErr) || (workerErr == nil) != (fakeErr == nil) {
			stepDiffs = append(stepDiffs, fmt.Sprintf("apply error: worker %v, fake %v", workerErr, fakeErr))
		}
		if workerErr == nil && fakeErr == nil {
			stats.accepted[mode]++
			if hasKids && kids[x] == mostAmongCands {
				stats.mostChildren++
			}
			if hasKids {
				stats.nonLeaf[mode]++
			}
			if !reflect.DeepEqual(workerResult, fakeResult) {
				workerText, fakeText := sweepResult(workerResult), sweepResult(fakeResult)
				if workerText == fakeText {
					workerText, fakeText = fmt.Sprintf("%#v", *workerResult), fmt.Sprintf("%#v", *fakeResult)
				}
				stepDiffs = append(stepDiffs, fmt.Sprintf("result:\n    worker %s\n    fake   %s", workerText, fakeText))
			}
		} else {
			stats.refused++
		}
		compared, next := c.compare(t)
		stepDiffs = append(stepDiffs, compared...)
		if len(stepDiffs) > 0 {
			t.Errorf("seed %d: differences after removal %d of %d:\n  %s", seed, step, sweepRemovalAttempts, strings.Join(stepDiffs, "\n  "))
			return
		}
		state = next
	}
}

// withMostChildren returns the users in nonLeaf with the highest child count, in their original order.
func withMostChildren(nonLeaf []string, kids map[string]int) []string {
	most := 0
	for _, u := range nonLeaf {
		most = max(most, kids[u])
	}
	var top []string
	for _, u := range nonLeaf {
		if kids[u] == most {
			top = append(top, u)
		}
	}
	return top
}

// relocationChains reports each relocation into a slot that another relocation in the same result vacates, and counts the relocations that change slot.
func relocationChains(before map[string]sweepPosition, relocated []Relocation) ([]string, int) {
	type slot struct {
		parent   string
		position int
	}
	old := map[string]sweepPosition{}
	for u, p := range before {
		old[uuidKey(u)] = p
	}
	vacates := map[slot]string{}
	moved := 0
	for _, r := range relocated {
		p, ok := old[uuidKey(r.UserID)]
		from, to := slot{uuidKey(p.Parent), p.Slot}, slot{uuidKey(r.ParentID), r.Position}
		if ok && from != to {
			vacates[from] = r.UserID
			moved++
		}
	}
	var chains []string
	for _, r := range relocated {
		if vacater, ok := vacates[slot{uuidKey(r.ParentID), r.Position}]; ok {
			chains = append(chains, fmt.Sprintf("relocation chain: %s moves into parent=%s position=%d, which %s vacates",
				r.UserID, r.ParentID, r.Position, vacater))
		}
	}
	return chains, moved
}

// runMatrixRemovalSweep sweeps seeds first to last against client, one subtest per seed.
func runMatrixRemovalSweep(t *testing.T, client *EngineClient, first, last int) (*sweepStats, int) {
	t.Helper()
	stats := &sweepStats{accepted: map[PruningMode]int{}, nonLeaf: map[PruningMode]int{}}
	ran := 0
	for seed := first; seed <= last; seed++ {
		stopped := false
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			ran++
			sweepSeed(t, client, seed, stats, &stopped)
		})
		if stopped {
			if seed < last {
				t.Fatalf("seed %d: a worker call returned an error with no engine code. Seeds %d-%d were not run.", seed, seed+1, last)
			}
			t.Fatalf("seed %d: a worker call returned an error with no engine code.", seed)
		}
	}
	return stats, ran
}

func TestFakeWriterEngine_SweepsMatrixRemovalsAsTheWorkerDoes(t *testing.T) {
	first, last := sweepDefaultFirst, sweepDefaultLast
	value, fromEnv := os.LookupEnv(sweepSeedsEnv)
	source := sweepSeedsEnv + " is unset"
	if fromEnv {
		var err error
		first, last, err = parseSweepSeeds(value)
		require.NoError(t, err)
		source = sweepSeedsEnv + "=" + value
	}
	t.Logf("sweeping seeds %d-%d (%s)", first, last, source)

	client := startWorker(t)
	stats, ran := runMatrixRemovalSweep(t, client, first, last)
	t.Logf("ran %d seeds. Accepted removals: %v, of which non-leaf: %v, of which the target had the most children: %d. Refused by either engine: %d",
		ran, stats.accepted, stats.nonLeaf, stats.mostChildren, stats.refused)
	if !fromEnv && ran == last-first+1 {
		assert.GreaterOrEqual(t, stats.nonLeaf[PruningPromoteEarliest], sweepNonLeafPromoteFloor,
			"accepted non-leaf promote_earliest removals over seeds %d-%d", first, last)
		assert.GreaterOrEqual(t, stats.mostChildren, sweepMostChildrenFloor,
			"accepted removals whose target had the most children among the candidates, over seeds %d-%d", first, last)
	}
}

func TestParseSweepSeeds(t *testing.T) {
	for _, row := range []struct {
		value       string
		first, last int
	}{
		{"1-200", 1, 200},
		{"203-203", 203, 203},
		{"1-5000", 1, 5000},
	} {
		first, last, err := parseSweepSeeds(row.value)
		require.NoError(t, err, row.value)
		require.Equal(t, [2]int{row.first, row.last}, [2]int{first, last}, row.value)
	}
	for _, row := range []struct{ value, message string }{
		{"", "FAKE_SWEEP_SEEDS is set but empty"},
		{"203", `FAKE_SWEEP_SEEDS="203" is not two integers joined by -`},
		{"a-3", `FAKE_SWEEP_SEEDS="a-3" is not two integers joined by -`},
		{"1-2-3", `FAKE_SWEEP_SEEDS="1-2-3" is not two integers joined by -`},
		{"0-5", `FAKE_SWEEP_SEEDS="0-5" holds a number below 1`},
		{"1-0", `FAKE_SWEEP_SEEDS="1-0" holds a number below 1`},
		{"1-99999999999999999999", `FAKE_SWEEP_SEEDS="1-99999999999999999999" holds a number that does not fit in an int`},
		{"5-1", `FAKE_SWEEP_SEEDS="5-1" has 5 above 1`},
	} {
		_, _, err := parseSweepSeeds(row.value)
		require.EqualError(t, err, row.message, "value %q", row.value)
	}
}

func TestRelocationChains(t *testing.T) {
	root, a, b, c := hexUser(1), hexUser(2), hexUser(3), hexUser(4)
	layout := func(spell func(string) string) map[string]sweepPosition {
		return map[string]sweepPosition{
			spell(root): {},
			spell(a):    {Parent: spell(root), Slot: 0, Depth: 1},
			spell(b):    {Parent: spell(root), Slot: 1, Depth: 1},
			spell(c):    {Parent: spell(a), Slot: 0, Depth: 2},
		}
	}
	same := func(s string) string { return s }
	up := strings.ToUpper
	chain := func(mover, parent string, position int, vacater string) string {
		return fmt.Sprintf("relocation chain: %s moves into parent=%s position=%d, which %s vacates", mover, parent, position, vacater)
	}
	for _, row := range []struct {
		name      string
		before    map[string]sweepPosition
		relocated []Relocation
		chains    []string
		moved     int
	}{
		{"a moves into the slot b vacates", layout(same),
			[]Relocation{{UserID: b, ParentID: root, Position: 2, Depth: 1}, {UserID: a, ParentID: root, Position: 1, Depth: 1}},
			[]string{chain(a, root, 1, b)}, 2},
		{"the chain with the relocations in upper case", layout(same),
			[]Relocation{{UserID: up(b), ParentID: up(root), Position: 2, Depth: 1}, {UserID: up(a), ParentID: up(root), Position: 1, Depth: 1}},
			[]string{chain(up(a), up(root), 1, up(b))}, 2},
		{"the chain with the positions before in upper case", layout(up),
			[]Relocation{{UserID: b, ParentID: root, Position: 2, Depth: 1}, {UserID: a, ParentID: root, Position: 1, Depth: 1}},
			[]string{chain(a, root, 1, b)}, 2},
		{"a and b swap slots", layout(same),
			[]Relocation{{UserID: a, ParentID: root, Position: 1, Depth: 1}, {UserID: b, ParentID: root, Position: 0, Depth: 1}},
			[]string{chain(a, root, 1, b), chain(b, root, 0, a)}, 2},
		{"c keeps its slot and changes depth", layout(same),
			[]Relocation{{UserID: c, ParentID: a, Position: 0, Depth: 1}}, nil, 0},
		{"c moves into the slot of a, which is removed and not relocated", layout(same),
			[]Relocation{{UserID: c, ParentID: root, Position: 0, Depth: 1}}, nil, 1},
		{"a moves into an empty slot", layout(same),
			[]Relocation{{UserID: a, ParentID: root, Position: 2, Depth: 1}}, nil, 1},
		{"the count is per relocation, with a listed twice", layout(same),
			[]Relocation{{UserID: a, ParentID: root, Position: 2, Depth: 1}, {UserID: a, ParentID: root, Position: 2, Depth: 1}}, nil, 2},
	} {
		t.Run(row.name, func(t *testing.T) {
			chains, moved := relocationChains(row.before, row.relocated)
			assert.Equal(t, row.chains, chains)
			assert.Equal(t, row.moved, moved)
		})
	}
}
