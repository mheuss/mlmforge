package networkengine

import (
	"context"
	"encoding/json"
	"errors"
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
		if got.CompletedAt == nil || got.VoidedAt == nil || got.SupersededBy == nil {
			t.Fatalf("need all three optional fields set to test aliasing, got %+v", got)
		}
		*got.CompletedAt = time.Unix(0, 0).UTC()
		*got.VoidedAt = time.Unix(0, 0).UTC()
		*got.SupersededBy = uuid.Nil
		got.CarryForward[2] = 'X'

		again, err := s.GetRun(ctx, oldID)
		if err != nil {
			t.Fatalf("second GetRun: %v", err)
		}
		if again.CompletedAt.Equal(time.Unix(0, 0).UTC()) {
			t.Error("CompletedAt is aliased to store state")
		}
		if again.VoidedAt.Equal(time.Unix(0, 0).UTC()) {
			t.Error("VoidedAt is aliased to store state")
		}
		if *again.SupersededBy != newID {
			t.Errorf("SupersededBy = %v, want %s; it is aliased to store state", again.SupersededBy, newID)
		}
		if string(again.CarryForward) != `{"v":1}` {
			t.Errorf("CarryForward = %s, want the stored value; it is aliased", again.CarryForward)
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
}
