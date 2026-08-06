package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Valid digests for tests. plan_hash must be "sha256:" plus exactly 64 hex
// digits, enforced both by the table CHECK and by validateRunInput, so short
// stand-ins like "sha256:abc" are rejected everywhere.
var (
	validHash = "sha256:" + strings.Repeat("a", 64)
	otherHash = "sha256:" + strings.Repeat("b", 64)
)

// runCommissionRunStoreSuite is the shared behavioral contract. Both the
// memory and Postgres implementations must pass it identically. newStore
// returns a fresh, empty store on each call.
func runCommissionRunStoreSuite(t *testing.T, newStore func(t *testing.T) CommissionRunStore) {
	t.Helper()

	t.Run("CreateRun then GetRun returns a running run", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		id, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		run, err := s.GetRun(ctx, id)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if run.Status != RunStatusRunning {
			t.Fatalf("Status = %q, want running", run.Status)
		}
		if run.PeriodID != "2026-01" || run.PlanHash != validHash {
			t.Fatalf("run = %+v, want period 2026-01 and hash %s", run, validHash)
		}
		if run.CompletedAt != nil || run.VoidedAt != nil || run.SupersededBy != nil {
			t.Fatalf("a new run should have nil completed/voided/superseded, got %+v", run)
		}
		if run.StartedAt.IsZero() {
			t.Fatal("StartedAt should be set")
		}
	})

	t.Run("GetRun on an unknown id returns RunNotFoundError", func(t *testing.T) {
		s := newStore(t)
		_, err := s.GetRun(context.Background(), uuid.New())
		var target *RunNotFoundError
		if !errors.As(err, &target) {
			t.Fatalf("want *RunNotFoundError, got %v", err)
		}
	})

	// Both implementations validate before touching storage, so malformed
	// input fails the same way in memory as it would against the table CHECK.
	t.Run("CreateRun rejects malformed input", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		if _, err := s.CreateRun(ctx, "", validHash); err == nil {
			t.Fatal("an empty period should be rejected")
		}
		if _, err := s.CreateRun(ctx, "2026-01", "sha256:anything"); err == nil {
			t.Fatal("a malformed plan hash should be rejected")
		}
	})

	t.Run("a second run for the same period is rejected with the winner's id", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		first, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("first CreateRun: %v", err)
		}
		_, err = s.CreateRun(ctx, "2026-01", otherHash)
		var target *LiveRunExistsError
		if !errors.As(err, &target) {
			t.Fatalf("want *LiveRunExistsError, got %v", err)
		}
		if target.ExistingRunID != first {
			t.Fatalf("ExistingRunID = %s, want %s", target.ExistingRunID, first)
		}
	})

	// A complete run still holds its period. This is what forces ReplaceRun
	// to void before inserting.
	t.Run("a completed run still blocks the period", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		first, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.CompleteRun(ctx, first, nil); err != nil {
			t.Fatalf("CompleteRun: %v", err)
		}
		_, err = s.CreateRun(ctx, "2026-01", otherHash)
		var target *LiveRunExistsError
		if !errors.As(err, &target) {
			t.Fatalf("want *LiveRunExistsError, got %v", err)
		}
	})

	t.Run("different periods do not collide", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		if _, err := s.CreateRun(ctx, "2026-01", validHash); err != nil {
			t.Fatalf("first: %v", err)
		}
		if _, err := s.CreateRun(ctx, "2026-02", validHash); err != nil {
			t.Fatalf("second period should be allowed, got: %v", err)
		}
	})

	t.Run("GetActiveRun finds a running run and returns nil for an unknown period", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		id, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		active, err := s.GetActiveRun(ctx, "2026-01")
		if err != nil {
			t.Fatalf("GetActiveRun: %v", err)
		}
		if active == nil || active.ID != id {
			t.Fatalf("GetActiveRun = %+v, want run %s", active, id)
		}

		none, err := s.GetActiveRun(ctx, "2099-12")
		if err != nil {
			t.Fatalf("GetActiveRun on an unknown period should not error, got: %v", err)
		}
		if none != nil {
			t.Fatalf("want nil for a period with no run, got %+v", none)
		}
	})

	t.Run("CompleteRun sets status and completed_at", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		id, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.CompleteRun(ctx, id, nil); err != nil {
			t.Fatalf("CompleteRun: %v", err)
		}
		run, err := s.GetRun(ctx, id)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if run.Status != RunStatusComplete {
			t.Fatalf("Status = %q, want complete", run.Status)
		}
		if run.CompletedAt == nil {
			t.Fatal("CompletedAt should be set after CompleteRun")
		}
	})

	t.Run("CompleteRun on an unknown id returns RunNotFoundError", func(t *testing.T) {
		s := newStore(t)
		err := s.CompleteRun(context.Background(), uuid.New(), nil)
		var target *RunNotFoundError
		if !errors.As(err, &target) {
			t.Fatalf("want *RunNotFoundError, got %v", err)
		}
	})

	// BR7. Both carry-over shapes the calculators actually produce must
	// survive the round trip with their numbers intact: binary leg volumes
	// from CalculateBinaryPairing, and board cycle counts from
	// CalculateBoardCommissions.
	//
	// Assert on parsed values, never on raw bytes. Postgres normalizes JSONB
	// on write — key order is not preserved and 120.0 comes back as 120 —
	// while the memory store hands back the exact bytes it was given. A
	// byte-equality assertion would pass in memory and fail against Postgres,
	// which is the opposite of what this suite is for.
	t.Run("CompleteRun round-trips carry-forward values", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		id, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		cf := []byte(`{
			"v": 1,
			"structures": {
				"primary-binary": {
					"kind": "binary_legs",
					"carry": {"11111111-1111-1111-1111-111111111111": {"left": 120.0, "right": 80.5}}
				},
				"leader-board": {
					"kind": "board_cycles",
					"counts": {"22222222-2222-2222-2222-222222222222": 3}
				}
			}
		}`)
		if err := s.CompleteRun(ctx, id, cf); err != nil {
			t.Fatalf("CompleteRun: %v", err)
		}
		run, err := s.GetRun(ctx, id)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if len(run.CarryForward) == 0 {
			t.Fatal("CarryForward should round-trip")
		}

		var got struct {
			V          int `json:"v"`
			Structures struct {
				Binary struct {
					Kind  string `json:"kind"`
					Carry map[string]struct {
						Left  float64 `json:"left"`
						Right float64 `json:"right"`
					} `json:"carry"`
				} `json:"primary-binary"`
				Board struct {
					Kind   string         `json:"kind"`
					Counts map[string]int `json:"counts"`
				} `json:"leader-board"`
			} `json:"structures"`
		}
		if err := json.Unmarshal(run.CarryForward, &got); err != nil {
			t.Fatalf("carry-forward is not readable JSON: %v", err)
		}

		if got.V != 1 {
			t.Fatalf("v = %d, want 1", got.V)
		}
		legs, ok := got.Structures.Binary.Carry["11111111-1111-1111-1111-111111111111"]
		if !ok {
			t.Fatal("binary carry entry missing after round trip")
		}
		if legs.Left != 120.0 || legs.Right != 80.5 {
			t.Fatalf("legs = %+v, want left 120 and right 80.5", legs)
		}
		if got.Structures.Board.Counts["22222222-2222-2222-2222-222222222222"] != 3 {
			t.Fatalf("board counts = %v, want 3", got.Structures.Board.Counts)
		}
	})

	t.Run("CompleteRun accepts a nil carry-forward", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		id, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.CompleteRun(ctx, id, nil); err != nil {
			t.Fatalf("CompleteRun with nil carry-forward: %v", err)
		}
		run, err := s.GetRun(ctx, id)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if run.CarryForward != nil {
			t.Fatalf("CarryForward = %#v, want nil", run.CarryForward)
		}
	})

	// A returned run is the caller's. Postgres scans fresh values on every
	// read, so a store handing back its own pointers would let a caller
	// rewrite persisted state through a value it was merely shown.
	t.Run("mutating a returned run does not change the store", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		oldID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.CompleteRun(ctx, oldID, json.RawMessage(`{"v":1}`)); err != nil {
			t.Fatalf("CompleteRun: %v", err)
		}
		newID, err := s.ReplaceRun(ctx, oldID, otherHash)
		if err != nil {
			t.Fatalf("ReplaceRun: %v", err)
		}

		got, err := s.GetRun(ctx, oldID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if got.CompletedAt == nil || got.VoidedAt == nil || got.SupersededBy == nil ||
			len(got.CarryForward) < 3 {
			t.Fatalf("need all four reference fields populated to test aliasing, got %+v", got)
		}
		poison := time.Unix(0, 0).UTC()
		*got.CompletedAt = poison
		*got.VoidedAt = poison
		*got.SupersededBy = uuid.Nil
		got.CarryForward[2] = 'X'

		again, err := s.GetRun(ctx, oldID)
		if err != nil {
			t.Fatalf("second GetRun: %v", err)
		}
		// Guard before dereferencing. A nil here would panic and take the
		// whole test binary down, hiding every later subtest — the worst
		// failure mode for the implementation this suite is meant to serve.
		if again.CompletedAt == nil || again.VoidedAt == nil || again.SupersededBy == nil {
			t.Fatalf("the second read lost a field the first one had: %+v", again)
		}
		if again.CompletedAt.Equal(poison) {
			t.Error("CompletedAt is aliased to store state")
		}
		if again.VoidedAt.Equal(poison) {
			t.Error("VoidedAt is aliased to store state")
		}
		if *again.SupersededBy != newID {
			t.Errorf("SupersededBy = %v, want %s; it is aliased to store state", again.SupersededBy, newID)
		}
		// Parsed, not byte-compared. Postgres normalizes JSONB on read —
		// `{"v":1}` comes back as `{"v": 1}` — so a byte assertion here would
		// pass in memory and fail against the real store, which is the exact
		// divergence this suite exists to catch.
		var parsed map[string]any
		if err := json.Unmarshal(again.CarryForward, &parsed); err != nil {
			t.Fatalf("stored carry-forward is not readable JSON: %v", err)
		}
		if parsed["v"] != float64(1) {
			t.Errorf("CarryForward = %s, want key \"v\"; it is aliased to store state", again.CarryForward)
		}
	})

	// The mirror of the case above, on the write side. A store that keeps the
	// caller's slice lets a later mutation rewrite persisted state. Postgres
	// copies the bytes on write, so it satisfies this for free.
	t.Run("mutating the carry-forward passed to CompleteRun does not change the store", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		id, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		cf := json.RawMessage(`{"v":1}`)
		if err := s.CompleteRun(ctx, id, cf); err != nil {
			t.Fatalf("CompleteRun: %v", err)
		}
		cf[2] = 'X'

		run, err := s.GetRun(ctx, id)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(run.CarryForward, &got); err != nil {
			t.Fatalf("stored carry-forward is not readable JSON: %v", err)
		}
		if _, ok := got["v"]; !ok {
			t.Errorf("stored carry-forward = %s, want key \"v\"; the store kept the caller's slice",
				run.CarryForward)
		}
	})

	// An empty non-nil slice means the same as nil, and must read back as
	// nil. Postgres stores it as NULL and returns nil; a memory store that
	// preserved the empty slice would disagree on a value this suite
	// compares.
	t.Run("an empty carry-forward reads back as nil", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		id, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.CompleteRun(ctx, id, json.RawMessage{}); err != nil {
			t.Fatalf("CompleteRun with an empty carry-forward: %v", err)
		}
		run, err := s.GetRun(ctx, id)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if run.CarryForward != nil {
			t.Fatalf("CarryForward = %#v, want nil", run.CarryForward)
		}
	})

	t.Run("CompleteRun rejects a non-object carry-forward", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		id, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.CompleteRun(ctx, id, json.RawMessage(`[1,2]`)); err == nil {
			t.Fatal("an array carry-forward should be rejected")
		}
		if err := s.CompleteRun(ctx, id, json.RawMessage(`null`)); err == nil {
			t.Fatal("a JSON null carry-forward should be rejected")
		}
		// Without this the test passes even if the first call wrongly
		// completed the run: the second would then fail as not-running, which
		// is still a non-nil error.
		run, err := s.GetRun(ctx, id)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if run.Status != RunStatusRunning {
			t.Fatalf("Status = %q, want the rejected calls to have left it running", run.Status)
		}
	})

	// BR2 and NFR5. The guarantee is supposed to come from the database, not
	// from an application check, so it has to hold under real concurrency.
	// Sequential calls would pass even with a naive read-then-write.
	t.Run("concurrent CreateRun yields exactly one winner", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		const racers = 8
		var wg sync.WaitGroup
		ids := make([]uuid.UUID, racers)
		errs := make([]error, racers)

		wg.Add(racers)
		for i := 0; i < racers; i++ {
			go func(i int) {
				defer wg.Done()
				ids[i], errs[i] = s.CreateRun(ctx, "2026-01", validHash)
			}(i)
		}
		wg.Wait()

		var winners []uuid.UUID
		var losers int
		for i := 0; i < racers; i++ {
			if errs[i] == nil {
				winners = append(winners, ids[i])
				continue
			}
			var target *LiveRunExistsError
			if !errors.As(errs[i], &target) {
				t.Fatalf("racer %d: want *LiveRunExistsError, got %v", i, errs[i])
			}
			losers++
		}
		if len(winners) != 1 {
			t.Fatalf("got %d winners, want exactly 1", len(winners))
		}
		if losers != racers-1 {
			t.Fatalf("got %d losers, want %d", losers, racers-1)
		}

		// Every loser must point at the same winning run, which is what makes
		// the error recoverable rather than merely informative.
		for i := 0; i < racers; i++ {
			var target *LiveRunExistsError
			if errors.As(errs[i], &target) && target.ExistingRunID != winners[0] {
				t.Fatalf("racer %d points at %s, want the winner %s",
					i, target.ExistingRunID, winners[0])
			}
		}
	})

	t.Run("CompleteRun twice returns RunNotRunningError", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		id, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.CompleteRun(ctx, id, nil); err != nil {
			t.Fatalf("first CompleteRun: %v", err)
		}
		err = s.CompleteRun(ctx, id, nil)
		var target *RunNotRunningError
		if !errors.As(err, &target) {
			t.Fatalf("want *RunNotRunningError, got %v", err)
		}
		if target.Status != RunStatusComplete {
			t.Fatalf("Status = %q, want complete", target.Status)
		}
	})

	t.Run("VoidRun on a completed run keeps completed_at", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		id, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.CompleteRun(ctx, id, nil); err != nil {
			t.Fatalf("CompleteRun: %v", err)
		}
		if err := s.VoidRun(ctx, id); err != nil {
			t.Fatalf("VoidRun: %v", err)
		}
		run, err := s.GetRun(ctx, id)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if run.Status != RunStatusVoided {
			t.Fatalf("Status = %q, want voided", run.Status)
		}
		if run.CompletedAt == nil {
			t.Fatal("voiding must not erase completed_at")
		}
		if run.VoidedAt == nil {
			t.Fatal("VoidedAt should be set")
		}
		if run.SupersededBy != nil {
			t.Fatal("SupersededBy should stay nil when voiding without a replacement")
		}
	})

	t.Run("VoidRun is idempotent", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		id, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.VoidRun(ctx, id); err != nil {
			t.Fatalf("first VoidRun: %v", err)
		}
		if err := s.VoidRun(ctx, id); err != nil {
			t.Fatalf("second VoidRun should be a no-op, got: %v", err)
		}
	})

	t.Run("VoidRun on an unknown id returns RunNotFoundError", func(t *testing.T) {
		s := newStore(t)
		err := s.VoidRun(context.Background(), uuid.New())
		var target *RunNotFoundError
		if !errors.As(err, &target) {
			t.Fatalf("want *RunNotFoundError, got %v", err)
		}
	})

	t.Run("voiding frees the period for a new run", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		id, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.VoidRun(ctx, id); err != nil {
			t.Fatalf("VoidRun: %v", err)
		}
		if _, err := s.CreateRun(ctx, "2026-01", otherHash); err != nil {
			t.Fatalf("CreateRun after voiding should succeed, got: %v", err)
		}
		active, err := s.GetActiveRun(ctx, "2026-01")
		if err != nil {
			t.Fatalf("GetActiveRun: %v", err)
		}
		if active == nil || active.ID == id {
			t.Fatalf("GetActiveRun should return the new run, got %+v", active)
		}
	})

	t.Run("ReplaceRun voids the old run and links it to the new one", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		oldID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.CompleteRun(ctx, oldID, nil); err != nil {
			t.Fatalf("CompleteRun: %v", err)
		}
		newID, err := s.ReplaceRun(ctx, oldID, otherHash)
		if err != nil {
			t.Fatalf("ReplaceRun: %v", err)
		}
		if newID == oldID {
			t.Fatal("ReplaceRun must mint a new run id")
		}

		old, err := s.GetRun(ctx, oldID)
		if err != nil {
			t.Fatalf("GetRun old: %v", err)
		}
		if old.Status != RunStatusVoided {
			t.Fatalf("old.Status = %q, want voided", old.Status)
		}
		if old.SupersededBy == nil || *old.SupersededBy != newID {
			t.Fatalf("old.SupersededBy = %v, want %s", old.SupersededBy, newID)
		}
		if old.CompletedAt == nil {
			t.Fatal("ReplaceRun must not erase the old run's completed_at")
		}
		if old.VoidedAt == nil {
			t.Fatal("VoidedAt should be set on the replaced run")
		}

		fresh, err := s.GetRun(ctx, newID)
		if err != nil {
			t.Fatalf("GetRun new: %v", err)
		}
		if fresh.Status != RunStatusRunning {
			t.Fatalf("new.Status = %q, want running", fresh.Status)
		}
		if fresh.PeriodID != "2026-01" {
			t.Fatalf("new.PeriodID = %q, want the old run's period", fresh.PeriodID)
		}
		if fresh.PlanHash != otherHash {
			t.Fatalf("new.PlanHash = %q, want %s", fresh.PlanHash, otherHash)
		}
	})

	// A run's lifecycle has to read forward. If the replacement's start is
	// stamped before the old run's void, anyone reconstructing the sequence
	// from timestamps sees the successor beginning before its predecessor
	// ended — in a table that exists to be an audit record.
	t.Run("the replacement starts no earlier than the old run was voided", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		oldID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		newID, err := s.ReplaceRun(ctx, oldID, otherHash)
		if err != nil {
			t.Fatalf("ReplaceRun: %v", err)
		}

		old, err := s.GetRun(ctx, oldID)
		if err != nil {
			t.Fatalf("GetRun old: %v", err)
		}
		fresh, err := s.GetRun(ctx, newID)
		if err != nil {
			t.Fatalf("GetRun new: %v", err)
		}
		if old.VoidedAt == nil {
			t.Fatal("the replaced run should carry VoidedAt")
		}
		if fresh.StartedAt.Before(*old.VoidedAt) {
			t.Errorf("replacement StartedAt %v precedes the old run's VoidedAt %v by %v",
				fresh.StartedAt, *old.VoidedAt, old.VoidedAt.Sub(fresh.StartedAt))
		}
	})

	// The one-active-run invariant under a real race. Sequential cases would
	// pass even without the row lock that actually enforces it: drop
	// FOR UPDATE and this is the only case that notices.
	t.Run("concurrent ReplaceRun yields exactly one winner", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		oldID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}

		const racers = 6
		var wg sync.WaitGroup
		ids := make([]uuid.UUID, racers)
		errs := make([]error, racers)
		wg.Add(racers)
		for i := 0; i < racers; i++ {
			go func(i int) {
				defer wg.Done()
				ids[i], errs[i] = s.ReplaceRun(ctx, oldID, otherHash)
			}(i)
		}
		wg.Wait()

		var winners []uuid.UUID
		for i := 0; i < racers; i++ {
			if errs[i] == nil {
				winners = append(winners, ids[i])
				continue
			}
			// Every loser must lose the documented way. A raw driver error
			// here means the typed contract does not hold under contention.
			var target *RunNotRunningError
			if !errors.As(errs[i], &target) {
				t.Errorf("racer %d: want *RunNotRunningError, got %v", i, errs[i])
			}
		}
		if len(winners) != 1 {
			t.Fatalf("got %d winners, want exactly 1", len(winners))
		}

		// The period must hold exactly the winner, and the old run must point
		// at it.
		active, err := s.GetActiveRun(ctx, "2026-01")
		if err != nil {
			t.Fatalf("GetActiveRun: %v", err)
		}
		if active == nil || active.ID != winners[0] {
			t.Fatalf("GetActiveRun = %+v, want the winning replacement %s", active, winners[0])
		}
		old, err := s.GetRun(ctx, oldID)
		if err != nil {
			t.Fatalf("GetRun old: %v", err)
		}
		if old.SupersededBy == nil || *old.SupersededBy != winners[0] {
			t.Fatalf("old.SupersededBy = %v, want the winner %s", old.SupersededBy, winners[0])
		}
	})

	t.Run("ReplaceRun works on a running run", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		oldID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if _, err := s.ReplaceRun(ctx, oldID, otherHash); err != nil {
			t.Fatalf("ReplaceRun on a running run should succeed, got: %v", err)
		}
	})

	// ReplaceRun accepts running or complete, so the error must say so.
	// Reporting a bare "not running" against a voided run would tell an
	// operator the wrong requirement.
	t.Run("ReplaceRun on an already-voided run names both allowed states", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		oldID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.VoidRun(ctx, oldID); err != nil {
			t.Fatalf("VoidRun: %v", err)
		}
		_, err = s.ReplaceRun(ctx, oldID, otherHash)
		var target *RunNotRunningError
		if !errors.As(err, &target) {
			t.Fatalf("want *RunNotRunningError, got %v", err)
		}
		if target.Status != RunStatusVoided {
			t.Fatalf("Status = %q, want voided", target.Status)
		}
		// Assert the structured field, not the rendered message. Allowed is
		// what the Postgres implementation must also populate; the wording is
		// pinned once in the errors test.
		want := []CommissionRunStatus{RunStatusRunning, RunStatusComplete}
		if !slices.Equal(target.Allowed, want) {
			t.Fatalf("Allowed = %v, want %v", target.Allowed, want)
		}
	})

	// A bad hash must be rejected before anything mutates, or the old run
	// ends up voided with no replacement and the period is left empty.
	t.Run("ReplaceRun with a bad hash leaves the old run intact", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		oldID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if _, err := s.ReplaceRun(ctx, oldID, "sha256:anything"); err == nil {
			t.Fatal("a malformed plan hash should be rejected")
		}
		old, err := s.GetRun(ctx, oldID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if old.Status != RunStatusRunning {
			t.Fatalf("old.Status = %q, want the run left untouched as running", old.Status)
		}
		active, err := s.GetActiveRun(ctx, "2026-01")
		if err != nil {
			t.Fatalf("GetActiveRun: %v", err)
		}
		if active == nil || active.ID != oldID {
			t.Fatalf("the period should still hold the original run, got %+v", active)
		}
	})

	t.Run("ReplaceRun on an unknown id returns RunNotFoundError", func(t *testing.T) {
		s := newStore(t)
		_, err := s.ReplaceRun(context.Background(), uuid.New(), otherHash)
		var target *RunNotFoundError
		if !errors.As(err, &target) {
			t.Fatalf("want *RunNotFoundError, got %v", err)
		}
	})

	t.Run("SaveResults then GetResults round-trips in id order", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		runID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		a, b := uuid.New(), uuid.New()
		in := []CommissionResultInput{
			{EarnerID: a, DollarAmount: 10.5, Detail: []byte(`{"v":1,"level":1}`)},
			{EarnerID: b, DollarAmount: 20.25, Detail: []byte(`{"v":1,"level":2}`)},
		}
		if err := s.SaveResults(ctx, runID, "primary", in); err != nil {
			t.Fatalf("SaveResults: %v", err)
		}
		got, err := s.GetResults(ctx, runID)
		if err != nil {
			t.Fatalf("GetResults: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
		if got[0].EarnerID != a || got[1].EarnerID != b {
			t.Fatal("results must come back in insertion (id ascending) order")
		}
		if got[0].DollarAmount != 10.5 || got[1].DollarAmount != 20.25 {
			t.Fatalf("amounts = %v, %v", got[0].DollarAmount, got[1].DollarAmount)
		}
		if got[0].RunID != runID || got[0].Structure != "primary" {
			t.Fatalf("run id and structure should be stamped from the call, got %+v", got[0])
		}
		if got[0].ID == 0 || got[1].ID <= got[0].ID {
			t.Fatalf("ids should be assigned and increasing, got %d and %d", got[0].ID, got[1].ID)
		}
	})

	// The detail column is the reason the three earning shapes can share one
	// table, so it has to survive the trip with its values intact. Parsed, not
	// byte-compared: Postgres normalizes JSONB.
	t.Run("SaveResults round-trips detail values", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		runID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.SaveResults(ctx, runID, "primary", []CommissionResultInput{
			{EarnerID: uuid.New(), DollarAmount: 1, Detail: []byte(
				`{"v":1,"level":3,"rate":0.05,"compressed":true,"source":"11111111-1111-1111-1111-111111111111"}`)},
		}); err != nil {
			t.Fatalf("SaveResults: %v", err)
		}
		got, err := s.GetResults(ctx, runID)
		if err != nil {
			t.Fatalf("GetResults: %v", err)
		}
		var detail struct {
			V          int     `json:"v"`
			Level      int     `json:"level"`
			Rate       float64 `json:"rate"`
			Compressed bool    `json:"compressed"`
			Source     string  `json:"source"`
		}
		if err := json.Unmarshal(got[0].Detail, &detail); err != nil {
			t.Fatalf("detail is not readable JSON: %v", err)
		}
		if detail.V != 1 || detail.Level != 3 || detail.Rate != 0.05 ||
			!detail.Compressed || detail.Source != "11111111-1111-1111-1111-111111111111" {
			t.Fatalf("detail = %+v, want every field preserved", detail)
		}
	})

	t.Run("SaveResults twice for one structure leaves one copy", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		runID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		in := []CommissionResultInput{
			{EarnerID: uuid.New(), DollarAmount: 1, Detail: []byte(`{"v":1}`)},
		}
		if err := s.SaveResults(ctx, runID, "primary", in); err != nil {
			t.Fatalf("first SaveResults: %v", err)
		}
		if err := s.SaveResults(ctx, runID, "primary", in); err != nil {
			t.Fatalf("retry SaveResults: %v", err)
		}
		got, err := s.GetResults(ctx, runID)
		if err != nil {
			t.Fatalf("GetResults: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1 — a retry must not duplicate earnings", len(got))
		}
	})

	t.Run("structures accumulate independently under one run", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		runID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		one := []CommissionResultInput{{EarnerID: uuid.New(), DollarAmount: 1, Detail: []byte(`{"v":1}`)}}
		two := []CommissionResultInput{{EarnerID: uuid.New(), DollarAmount: 2, Detail: []byte(`{"v":1}`)}}
		if err := s.SaveResults(ctx, runID, "unilevel", one); err != nil {
			t.Fatalf("SaveResults unilevel: %v", err)
		}
		if err := s.SaveResults(ctx, runID, "binary", two); err != nil {
			t.Fatalf("SaveResults binary: %v", err)
		}
		// Replacing one structure must not touch the other.
		if err := s.SaveResults(ctx, runID, "unilevel", one); err != nil {
			t.Fatalf("replace unilevel: %v", err)
		}
		got, err := s.GetResults(ctx, runID)
		if err != nil {
			t.Fatalf("GetResults: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2 (one per structure)", len(got))
		}
		// The survivor of the untouched structure must be the binary row.
		var sawBinary bool
		for _, r := range got {
			if r.Structure == "binary" && r.DollarAmount == 2 {
				sawBinary = true
			}
		}
		if !sawBinary {
			t.Fatalf("replacing unilevel destroyed the binary rows, got %+v", got)
		}
		// Order must still be ascending across structures after a replace,
		// and the untouched structure must sort first. Ids are never reused,
		// so a replaced structure's new rows always land after rows that were
		// already there — the property a payout reader would depend on.
		if got[0].ID >= got[1].ID {
			t.Fatalf("ids must stay ascending after a replace, got %d then %d", got[0].ID, got[1].ID)
		}
		if got[0].Structure != "binary" {
			t.Fatalf("got[0].Structure = %q, want the untouched structure first; "+
				"a replaced structure must not reuse ids", got[0].Structure)
		}
	})

	t.Run("SaveResults with an empty slice clears that structure", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		runID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		in := []CommissionResultInput{{EarnerID: uuid.New(), DollarAmount: 1, Detail: []byte(`{"v":1}`)}}
		if err := s.SaveResults(ctx, runID, "primary", in); err != nil {
			t.Fatalf("SaveResults: %v", err)
		}
		if err := s.SaveResults(ctx, runID, "primary", nil); err != nil {
			t.Fatalf("SaveResults empty: %v", err)
		}
		got, err := s.GetResults(ctx, runID)
		if err != nil {
			t.Fatalf("GetResults: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("len = %d, want 0", len(got))
		}
	})

	t.Run("SaveResults on a completed run returns RunNotRunningError", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		runID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.CompleteRun(ctx, runID, nil); err != nil {
			t.Fatalf("CompleteRun: %v", err)
		}
		err = s.SaveResults(ctx, runID, "primary", []CommissionResultInput{
			{EarnerID: uuid.New(), DollarAmount: 1, Detail: []byte(`{"v":1}`)},
		})
		var target *RunNotRunningError
		if !errors.As(err, &target) {
			t.Fatalf("want *RunNotRunningError, got %v", err)
		}
	})

	t.Run("SaveResults on an unknown run returns RunNotFoundError", func(t *testing.T) {
		s := newStore(t)
		err := s.SaveResults(context.Background(), uuid.New(), "primary",
			[]CommissionResultInput{{EarnerID: uuid.New(), DollarAmount: 1, Detail: []byte(`{"v":1}`)}})
		var target *RunNotFoundError
		if !errors.As(err, &target) {
			t.Fatalf("want *RunNotFoundError, got %v", err)
		}
	})

	t.Run("GetResults on an unknown run returns RunNotFoundError", func(t *testing.T) {
		s := newStore(t)
		_, err := s.GetResults(context.Background(), uuid.New())
		var target *RunNotFoundError
		if !errors.As(err, &target) {
			t.Fatalf("want *RunNotFoundError, got %v", err)
		}
	})

	t.Run("a running run's results are invisible to GetLiveResults", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		runID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.SaveResults(ctx, runID, "primary", []CommissionResultInput{
			{EarnerID: uuid.New(), DollarAmount: 1, Detail: []byte(`{"v":1}`)},
		}); err != nil {
			t.Fatalf("SaveResults: %v", err)
		}
		live, err := s.GetLiveResults(ctx, "2026-01")
		if err != nil {
			t.Fatalf("GetLiveResults: %v", err)
		}
		if len(live) != 0 {
			t.Fatalf("len = %d, want 0 — completion is the visibility flip", len(live))
		}
		// Invisible must mean hidden, not absent. Without this the assertion
		// above passes against a store whose SaveResults writes nothing.
		stored, err := s.GetResults(ctx, runID)
		if err != nil {
			t.Fatalf("GetResults: %v", err)
		}
		if len(stored) != 1 {
			t.Fatalf("stored rows = %d, want 1; the row must exist while being invisible", len(stored))
		}
	})

	t.Run("completing a run makes its results live", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		runID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.SaveResults(ctx, runID, "primary", []CommissionResultInput{
			{EarnerID: uuid.New(), DollarAmount: 7, Detail: []byte(`{"v":1}`)},
		}); err != nil {
			t.Fatalf("SaveResults: %v", err)
		}
		if err := s.CompleteRun(ctx, runID, nil); err != nil {
			t.Fatalf("CompleteRun: %v", err)
		}
		live, err := s.GetLiveResults(ctx, "2026-01")
		if err != nil {
			t.Fatalf("GetLiveResults: %v", err)
		}
		if len(live) != 1 || live[0].DollarAmount != 7 {
			t.Fatalf("live = %+v, want one row of 7", live)
		}
	})

	t.Run("a voided run's results stay readable but stop being live", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		runID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.SaveResults(ctx, runID, "primary", []CommissionResultInput{
			{EarnerID: uuid.New(), DollarAmount: 7, Detail: []byte(`{"v":1}`)},
		}); err != nil {
			t.Fatalf("SaveResults: %v", err)
		}
		if err := s.CompleteRun(ctx, runID, nil); err != nil {
			t.Fatalf("CompleteRun: %v", err)
		}
		if err := s.VoidRun(ctx, runID); err != nil {
			t.Fatalf("VoidRun: %v", err)
		}

		live, err := s.GetLiveResults(ctx, "2026-01")
		if err != nil {
			t.Fatalf("GetLiveResults: %v", err)
		}
		if len(live) != 0 {
			t.Fatalf("live = %d rows, want 0 after voiding", len(live))
		}

		archived, err := s.GetResults(ctx, runID)
		if err != nil {
			t.Fatalf("GetResults: %v", err)
		}
		if len(archived) != 1 {
			t.Fatal("a voided run's results must remain readable for audit")
		}
	})

	// ADR-013 scenario 2 end to end: the replacement's results become live and
	// the superseded run's stay readable but invisible.
	t.Run("after ReplaceRun the new run's results become the live ones", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		oldID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.SaveResults(ctx, oldID, "primary", []CommissionResultInput{
			{EarnerID: uuid.New(), DollarAmount: 7, Detail: []byte(`{"v":1}`)},
		}); err != nil {
			t.Fatalf("SaveResults old: %v", err)
		}
		if err := s.CompleteRun(ctx, oldID, nil); err != nil {
			t.Fatalf("CompleteRun old: %v", err)
		}

		newID, err := s.ReplaceRun(ctx, oldID, otherHash)
		if err != nil {
			t.Fatalf("ReplaceRun: %v", err)
		}
		if err := s.SaveResults(ctx, newID, "primary", []CommissionResultInput{
			{EarnerID: uuid.New(), DollarAmount: 9, Detail: []byte(`{"v":1}`)},
		}); err != nil {
			t.Fatalf("SaveResults new: %v", err)
		}
		if err := s.CompleteRun(ctx, newID, nil); err != nil {
			t.Fatalf("CompleteRun new: %v", err)
		}

		live, err := s.GetLiveResults(ctx, "2026-01")
		if err != nil {
			t.Fatalf("GetLiveResults: %v", err)
		}
		if len(live) != 1 || live[0].DollarAmount != 9 {
			t.Fatalf("live = %+v, want only the replacement's row of 9", live)
		}
		archived, err := s.GetResults(ctx, oldID)
		if err != nil {
			t.Fatalf("GetResults old: %v", err)
		}
		if len(archived) != 1 || archived[0].DollarAmount != 7 {
			t.Fatalf("the superseded run's results must stay readable, got %+v", archived)
		}
	})

	// The interface requires GetLiveResults to resolve the run and read its
	// rows atomically, so a replacement landing mid-read cannot produce a
	// mix. That is the guarantee letting a million-row write happen outside
	// the replacement transaction.
	//
	// What this buys, honestly. The memory store passes trivially because its
	// RWMutex makes the interleaving impossible.
	//
	// Note what is NOT a violation: resolving the live run and then reading
	// that run's rows in a second round trip. A complete run's rows are
	// frozen — SaveResults refuses a non-running run and voiding never
	// deletes — so the second read returns exactly the state that was live at
	// the first. Stale by a moment, but a valid linearization point.
	//
	// The shape this case exists for is a paged or keyset read that
	// re-resolves the live run between pages, which is the plausible one at
	// the million-row scale the interface doc has in mind. The simpler
	// failure — a period-keyed row query with no run filter, which sees a
	// voided run's surviving rows — is already pinned sequentially by "after
	// ReplaceRun the new run's results become the live ones".
	t.Run("GetLiveResults never mixes rows from two runs", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		rows := func(amount float64) []CommissionResultInput {
			out := make([]CommissionResultInput, 3)
			for i := range out {
				out[i] = CommissionResultInput{
					EarnerID: uuid.New(), DollarAmount: amount, Detail: []byte(`{"v":1}`),
				}
			}
			return out
		}

		oldID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.SaveResults(ctx, oldID, "primary", rows(7)); err != nil {
			t.Fatalf("SaveResults old: %v", err)
		}
		if err := s.CompleteRun(ctx, oldID, nil); err != nil {
			t.Fatalf("CompleteRun old: %v", err)
		}

		var wg sync.WaitGroup
		var mu sync.Mutex
		var problems []string
		var sawOld int
		report := func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			problems = append(problems, fmt.Sprintf(format, args...))
		}

		// Without a barrier the writer finishes before the goroutines are
		// even scheduled, so the readers only ever see the void window and
		// the settled new state — the transition this case is named for never
		// happens. Each reader signals once it has seen the pre-replacement
		// run, and the writer waits for all of them.
		var started sync.WaitGroup
		const readers = 4
		started.Add(readers)

		done := make(chan struct{})
		wg.Add(readers)
		for i := 0; i < readers; i++ {
			go func() {
				defer wg.Done()
				// Once, and via defer, so a reader that bails out early still
				// releases the writer instead of hanging it forever.
				var once sync.Once
				signal := func() { once.Do(started.Done) }
				defer signal()

				for {
					select {
					case <-done:
						return
					default:
					}
					live, err := s.GetLiveResults(ctx, "2026-01")
					if err != nil {
						report("GetLiveResults: %v", err)
						return
					}
					if len(live) == 0 {
						// Legal: the period has no completed run during the
						// window between voiding the old one and completing
						// its replacement.
						continue
					}
					if len(live) != 3 {
						report("saw %d rows, want a whole run's 3 — a partial read", len(live))
						return
					}
					runID, amount := live[0].RunID, live[0].DollarAmount
					for _, r := range live[1:] {
						if r.RunID != runID {
							report("rows from two runs in one read: %s and %s", runID, r.RunID)
							return
						}
						if r.DollarAmount != amount {
							report("amounts %v and %v in one read", amount, r.DollarAmount)
							return
						}
					}
					if runID == oldID {
						mu.Lock()
						sawOld++
						mu.Unlock()
					}
					// The writer is still blocked, so this first clean read
					// is necessarily of the pre-replacement run.
					signal()
				}
			}()
		}

		started.Wait()
		newID, err := s.ReplaceRun(ctx, oldID, otherHash)
		if err != nil {
			report("ReplaceRun: %v", err)
		} else {
			if err := s.SaveResults(ctx, newID, "primary", rows(9)); err != nil {
				report("SaveResults new: %v", err)
			}
			if err := s.CompleteRun(ctx, newID, nil); err != nil {
				report("CompleteRun new: %v", err)
			}
		}
		close(done)
		wg.Wait()

		for _, p := range problems {
			t.Error(p)
		}
		// Proves the barrier did its job. Without it this is zero and the
		// case degenerates into polling an already-settled store.
		if sawOld == 0 {
			t.Error("no reader observed the pre-replacement run; the transition was never exercised")
		}

		// The replacement must be what is live once the dust settles.
		live, err := s.GetLiveResults(ctx, "2026-01")
		if err != nil {
			t.Fatalf("final GetLiveResults: %v", err)
		}
		if len(live) != 3 || live[0].DollarAmount != 9 {
			t.Fatalf("final live = %+v, want the replacement's three rows of 9", live)
		}
	})

	t.Run("GetLiveResults on an unknown period is empty, not an error", func(t *testing.T) {
		s := newStore(t)
		got, err := s.GetLiveResults(context.Background(), "2099-12")
		if err != nil {
			t.Fatalf("GetLiveResults: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("len = %d, want 0", len(got))
		}
	})

	// Both implementations must refuse the same malformed input. Without
	// these, the memory store accepts values Postgres rejects and a test that
	// passes in memory fails in production — the exact trap this suite
	// exists to close. CreateRun and CompleteRun are covered above.
	t.Run("SaveResults rejects malformed input", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		runID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		cases := []struct {
			name      string
			structure string
			input     []CommissionResultInput
		}{
			{"empty structure", "", nil},
			{"nil earner id", "primary", []CommissionResultInput{
				{EarnerID: uuid.Nil, DollarAmount: 1, Detail: []byte(`{"v":1}`)}}},
			{"NaN amount", "primary", []CommissionResultInput{
				{EarnerID: uuid.New(), DollarAmount: math.NaN(), Detail: []byte(`{"v":1}`)}}},
			{"positive infinity", "primary", []CommissionResultInput{
				{EarnerID: uuid.New(), DollarAmount: math.Inf(1), Detail: []byte(`{"v":1}`)}}},
			{"negative infinity", "primary", []CommissionResultInput{
				{EarnerID: uuid.New(), DollarAmount: math.Inf(-1), Detail: []byte(`{"v":1}`)}}},
			{"array detail", "primary", []CommissionResultInput{
				{EarnerID: uuid.New(), DollarAmount: 1, Detail: []byte(`[1,2]`)}}},
			{"JSON null detail", "primary", []CommissionResultInput{
				{EarnerID: uuid.New(), DollarAmount: 1, Detail: []byte(`null`)}}},
			{"nil detail", "primary", []CommissionResultInput{
				{EarnerID: uuid.New(), DollarAmount: 1, Detail: nil}}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if err := s.SaveResults(ctx, runID, tc.structure, tc.input); err == nil {
					t.Fatal("expected an error")
				}
			})
		}
		// A rejected batch must write nothing.
		got, err := s.GetResults(ctx, runID)
		if err != nil {
			t.Fatalf("GetResults: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("rejected batches left %d rows behind", len(got))
		}
	})

	// A batch is all-or-nothing. Postgres gets this from its transaction; the
	// memory store has to validate every row before writing any.
	t.Run("SaveResults rejects a batch for one bad row and writes none", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		runID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.SaveResults(ctx, runID, "primary", []CommissionResultInput{
			{EarnerID: uuid.New(), DollarAmount: 1, Detail: []byte(`{"v":1}`)},
			{EarnerID: uuid.New(), DollarAmount: math.NaN(), Detail: []byte(`{"v":1}`)},
		}); err == nil {
			t.Fatal("a batch with one bad row should be rejected")
		}
		got, err := s.GetResults(ctx, runID)
		if err != nil {
			t.Fatalf("GetResults: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("the good row from a rejected batch was written; got %d rows", len(got))
		}
	})

	// json.RawMessage is a byte slice. A store that keeps the caller's array
	// lets a later mutation rewrite persisted state. Postgres copies the bytes
	// on write, so the memory store must too.
	t.Run("mutating a detail passed to SaveResults does not change the store", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		runID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		detail := []byte(`{"v":1,"level":3}`)
		if err := s.SaveResults(ctx, runID, "primary", []CommissionResultInput{
			{EarnerID: uuid.New(), DollarAmount: 1, Detail: detail},
		}); err != nil {
			t.Fatalf("SaveResults: %v", err)
		}
		copy(detail, []byte(`{"v":9`))

		got, err := s.GetResults(ctx, runID)
		if err != nil {
			t.Fatalf("GetResults: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		var parsed map[string]any
		if err := json.Unmarshal(got[0].Detail, &parsed); err != nil {
			t.Fatalf("stored detail is not readable JSON: %v", err)
		}
		// Assert on "v", the key the mutation above actually overwrites.
		// Checking "level" would be vacuous: the six-byte copy cannot reach
		// it, so the assertion would hold whether or not the store cloned.
		if parsed["v"] != float64(1) {
			t.Fatalf("stored detail = %s; mutating the input changed stored state", got[0].Detail)
		}
	})

	// The read side of the same property.
	t.Run("mutating a returned detail does not change the store", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		runID, err := s.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := s.SaveResults(ctx, runID, "primary", []CommissionResultInput{
			{EarnerID: uuid.New(), DollarAmount: 1, Detail: []byte(`{"v":1,"level":3}`)},
		}); err != nil {
			t.Fatalf("SaveResults: %v", err)
		}
		got, err := s.GetResults(ctx, runID)
		if err != nil {
			t.Fatalf("GetResults: %v", err)
		}
		if len(got) != 1 || len(got[0].Detail) < 6 {
			t.Fatalf("need a populated detail to test aliasing, got %+v", got)
		}
		copy(got[0].Detail, []byte(`{"v":7`))

		again, err := s.GetResults(ctx, runID)
		if err != nil {
			t.Fatalf("GetResults again: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(again[0].Detail, &parsed); err != nil {
			t.Fatalf("stored detail is not readable JSON: %v", err)
		}
		if parsed["v"] != float64(1) {
			t.Fatalf("stored detail = %s; mutating a returned row changed stored state", again[0].Detail)
		}
	})
}
