package networkengine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

// TestCommissionLifecycleEndToEnd walks the full ADR-013 path: run a period,
// complete it, then re-run it and confirm the old run is archived rather than
// destroyed while the period's live results switch over cleanly.
//
// "End to end" means the store's own lifecycle, not a request path. Nothing
// calls this store in production until HEU-592 lands, so the test drives the
// interface directly and the amounts are constants rather than engine output.
// It does not show that computed commissions persist — only that the
// lifecycle they will travel through behaves as designed.
func TestCommissionLifecycleEndToEnd(t *testing.T) {
	if pgContainer == nil {
		t.Skip("postgres container unavailable")
	}
	store := NewPostgresCommissionRunStore(pgContainer.NewPool(t))
	ctx := context.Background()

	earner := uuid.New()

	// --- First run for the period ---
	firstID, err := store.CreateRun(ctx, "2026-01", validHash)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	t.Logf("run 1 opened: %s", firstID)

	if err := store.SaveResults(ctx, firstID, "unilevel", []CommissionResultInput{
		{EarnerID: earner, DollarAmount: 100, Detail: []byte(`{"v":1,"level":1,"rate":0.05}`)},
	}); err != nil {
		t.Fatalf("SaveResults unilevel: %v", err)
	}
	if err := store.SaveResults(ctx, firstID, "binary", []CommissionResultInput{
		{EarnerID: earner, DollarAmount: 50, Detail: []byte(`{"v":1,"ratio":1.0,"capped":false}`)},
	}); err != nil {
		t.Fatalf("SaveResults binary: %v", err)
	}

	// Partial results stay invisible until the run completes.
	if live, err := store.GetLiveResults(ctx, "2026-01"); err != nil {
		t.Fatalf("GetLiveResults mid-run: %v", err)
	} else if len(live) != 0 {
		t.Fatalf("mid-run live results = %d, want 0", len(live))
	}
	t.Log("mid-run: 0 live results, as expected")

	// The rows exist, they are just not live. Without this the invisibility
	// check above would also pass against a store that wrote nothing.
	staged, err := store.GetResults(ctx, firstID)
	if err != nil {
		t.Fatalf("GetResults mid-run: %v", err)
	}
	if len(staged) != 2 {
		t.Fatalf("mid-run stored rows = %d, want 2; invisibility must mean hidden, not absent", len(staged))
	}

	carry := []byte(`{"v":1,"structures":{"binary":{"kind":"binary_legs"}}}`)
	if err := store.CompleteRun(ctx, firstID, carry); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}

	live, err := store.GetLiveResults(ctx, "2026-01")
	if err != nil {
		t.Fatalf("GetLiveResults after completion: %v", err)
	}
	if len(live) != 2 {
		t.Fatalf("live results = %d, want 2", len(live))
	}
	// Per structure, not just the total. One earner holds both rows, so a sum
	// alone would pass against a store that swapped the two amounts between
	// structures. This is also the only place in the repo that reads
	// Structure off a GetLiveResults return rather than GetResults.
	byStructure := map[string]float64{}
	for _, r := range live {
		byStructure[r.Structure] += r.DollarAmount
	}
	if byStructure["unilevel"] != 100 || byStructure["binary"] != 50 {
		t.Fatalf("live results by structure = %v, want unilevel 100 and binary 50", byStructure)
	}
	total := live[0].DollarAmount + live[1].DollarAmount
	if total != 150 {
		t.Fatalf("live total = %v, want 150", total)
	}
	t.Logf("run 1 complete: %d live results totalling %v", len(live), total)

	// --- Re-run the same period with a changed plan ---
	secondID, err := store.ReplaceRun(ctx, firstID, otherHash)
	if err != nil {
		t.Fatalf("ReplaceRun: %v", err)
	}
	t.Logf("run 2 opened: %s, superseding %s", secondID, firstID)

	// The replacement carries the new plan's identity, which is the whole
	// point of re-running: a different plan produced these numbers.
	fresh, err := store.GetRun(ctx, secondID)
	if err != nil {
		t.Fatalf("GetRun on the replacement: %v", err)
	}
	if fresh.PlanHash != otherHash {
		t.Fatalf("replacement PlanHash = %q, want %q", fresh.PlanHash, otherHash)
	}
	if fresh.PeriodID != "2026-01" {
		t.Fatalf("replacement PeriodID = %q, want the period it replaced", fresh.PeriodID)
	}

	if err := store.SaveResults(ctx, secondID, "unilevel", []CommissionResultInput{
		{EarnerID: earner, DollarAmount: 120, Detail: []byte(`{"v":1,"level":1,"rate":0.06}`)},
	}); err != nil {
		t.Fatalf("SaveResults on replacement: %v", err)
	}
	if err := store.CompleteRun(ctx, secondID, nil); err != nil {
		t.Fatalf("CompleteRun on replacement: %v", err)
	}

	// The period's live results are the replacement's only.
	live, err = store.GetLiveResults(ctx, "2026-01")
	if err != nil {
		t.Fatalf("GetLiveResults after replacement: %v", err)
	}
	if len(live) != 1 || live[0].DollarAmount != 120 {
		t.Fatalf("live results = %+v, want one row of 120", live)
	}
	if live[0].RunID != secondID {
		t.Fatalf("live row belongs to run %s, want the replacement %s", live[0].RunID, secondID)
	}
	t.Logf("run 2 complete: %d live result of %v", len(live), live[0].DollarAmount)

	// The voided run is archived, not destroyed.
	old, err := store.GetRun(ctx, firstID)
	if err != nil {
		t.Fatalf("GetRun on the voided run: %v", err)
	}
	if old.Status != RunStatusVoided {
		t.Fatalf("old run status = %q, want voided", old.Status)
	}
	if old.SupersededBy == nil || *old.SupersededBy != secondID {
		t.Fatalf("old.SupersededBy = %v, want %s", old.SupersededBy, secondID)
	}
	if old.CompletedAt == nil {
		t.Fatal("voiding erased the old run's completion time")
	}
	// Parsed, not just non-empty. A store that replaced the carry-forward
	// with {} on void would satisfy a length check while having erased the
	// thing the check claims to protect.
	var carryBack struct {
		V          int `json:"v"`
		Structures struct {
			Binary struct {
				Kind string `json:"kind"`
			} `json:"binary"`
		} `json:"structures"`
	}
	if err := json.Unmarshal(old.CarryForward, &carryBack); err != nil {
		t.Fatalf("the voided run's carry-forward is not readable JSON: %v", err)
	}
	if carryBack.V != 1 || carryBack.Structures.Binary.Kind != "binary_legs" {
		t.Fatalf("voiding altered the old run's carry-forward: %s", old.CarryForward)
	}
	// The old run also keeps its own plan identity, so a dispute can say
	// which plan produced the superseded numbers.
	if old.PlanHash != validHash {
		t.Fatalf("old run PlanHash = %q, want the plan it ran under %q", old.PlanHash, validHash)
	}

	archived, err := store.GetResults(ctx, firstID)
	if err != nil {
		t.Fatalf("GetResults on the voided run: %v", err)
	}
	if len(archived) != 2 {
		t.Fatalf("archived results = %d, want 2 — a voided run's results must survive", len(archived))
	}
	// Both structures survive, not just the one the replacement rewrote.
	// Amounts and detail, not just labels. Archiving that kept the rows but
	// dropped what they said would be worthless for the dispute resolution
	// these rows are retained for.
	archivedAmounts := map[string]float64{}
	for _, r := range archived {
		archivedAmounts[r.Structure] += r.DollarAmount
		var detail map[string]any
		if err := json.Unmarshal(r.Detail, &detail); err != nil {
			t.Fatalf("archived %s detail is not readable JSON: %v", r.Structure, err)
		}
		if detail["v"] != float64(1) {
			t.Fatalf("archived %s detail lost its version: %s", r.Structure, r.Detail)
		}
	}
	if archivedAmounts["unilevel"] != 100 || archivedAmounts["binary"] != 50 {
		t.Fatalf("archived amounts = %v, want the original unilevel 100 and binary 50", archivedAmounts)
	}
	t.Logf("run 1 archived: voided, superseded_by set, %d results still readable", len(archived))
}
