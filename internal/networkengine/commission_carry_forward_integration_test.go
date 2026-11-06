package networkengine

import (
	"context"
	"testing"

	"github.com/mlmforge/mlmforge/internal/dec"
)

// Postgres re-emits jsonb with its own key order, spacing and number text, so
// the reader is checked against what the real store hands back.
func TestCarryForwardSurvivesThePostgresStore(t *testing.T) {
	if pgContainer == nil {
		t.Skip("postgres container unavailable")
	}
	store := NewPostgresCommissionRunStore(pgContainer.NewPool(t))
	ctx := context.Background()

	t.Run("binary carry reads back as exact decimals", func(t *testing.T) {
		want := map[string]map[string]LegVolumesDTO{
			"primary-binary": {
				"11111111-1111-1111-1111-111111111111": {Left: dec.MustParse("120.0"), Right: dec.MustParse("80.5")},
				"22222222-2222-2222-2222-222222222222": {Left: dec.MustParse("0.1"), Right: dec.MustParse("1234567.89")},
			},
			"idle-binary": {},
		}
		raw, err := CarryForwardFrom(want)
		if err != nil {
			t.Fatalf("CarryForwardFrom: %v", err)
		}
		id, err := store.CreateRun(ctx, "2026-01", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := store.CompleteRun(ctx, id, raw); err != nil {
			t.Fatalf("CompleteRun: %v", err)
		}
		run, err := store.GetRun(ctx, id)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		got, err := ParseCarryForward(run.CarryForward)
		if err != nil {
			t.Fatalf("ParseCarryForward(%s): %v", run.CarryForward, err)
		}
		assertSameCarryForward(t, got, want)
		if got["idle-binary"] == nil {
			t.Fatalf("idle-binary read back as a nil map from %s, want a non-nil empty map", run.CarryForward)
		}
	})

	t.Run("no carry-forward reads back as nil", func(t *testing.T) {
		raw, err := CarryForwardFrom(nil)
		if err != nil {
			t.Fatalf("CarryForwardFrom: %v", err)
		}
		id, err := store.CreateRun(ctx, "2026-02", validHash)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if err := store.CompleteRun(ctx, id, raw); err != nil {
			t.Fatalf("CompleteRun: %v", err)
		}
		run, err := store.GetRun(ctx, id)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		got, err := ParseCarryForward(run.CarryForward)
		if err != nil {
			t.Fatalf("ParseCarryForward(%s): %v", run.CarryForward, err)
		}
		if got != nil {
			t.Fatalf("ParseCarryForward(%s) = %v, want nil", run.CarryForward, got)
		}
	})
}
