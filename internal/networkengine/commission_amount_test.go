package networkengine

import (
	"context"
	"math"
	"math/rand"
	"testing"

	"github.com/google/uuid"
)

// TestDollarAmountRoundTripsThroughNumeric is the property behind design
// decision 5: float64 in Go, NUMERIC in Postgres, lossless in both
// directions. Shortest-round-trip formatting on write and ParseFloat on read
// must reproduce the original bits.
func TestDollarAmountRoundTripsThroughNumeric(t *testing.T) {
	if pgContainer == nil {
		t.Skip("postgres container unavailable")
	}
	store := NewPostgresCommissionRunStore(pgContainer.NewPool(t))
	ctx := context.Background()

	runID, err := store.CreateRun(ctx, "2026-01", validHash)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Fixed seed keeps the test deterministic, per the project's testing
	// rules. Same pattern as history_window_test.go:204, which passes the
	// configured linters.
	rng := rand.New(rand.NewSource(20260805))

	amounts := []float64{
		0, 1, -1, 0.1, 0.2, 0.3,
		1.0 / 3.0,
		123456.789,
		math.SmallestNonzeroFloat64,
		math.MaxFloat64 / 1e300,
		// Clawbacks are why negatives must survive, and the finite-range
		// CHECK is the constraint most likely to be tightened by mistake.
		-0.01, -123456.789,
		// Values needing all 17 significant digits, where a shorter
		// formatting would silently lose the low bits.
		0.1 + 0.2,
		math.Nextafter(1, 2),
		math.Nextafter(0.1, 1),
	}
	for i := 0; i < 200; i++ {
		amounts = append(amounts, rng.NormFloat64()*1000)
	}

	in := make([]CommissionResultInput, len(amounts))
	for i, a := range amounts {
		in[i] = CommissionResultInput{
			EarnerID:     uuid.New(),
			DollarAmount: a,
			Detail:       []byte(`{"v":1}`),
		}
	}
	if err := store.SaveResults(ctx, runID, "primary", in); err != nil {
		t.Fatalf("SaveResults: %v", err)
	}

	got, err := store.GetResults(ctx, runID)
	if err != nil {
		t.Fatalf("GetResults: %v", err)
	}
	if len(got) != len(amounts) {
		t.Fatalf("len = %d, want %d", len(got), len(amounts))
	}
	for i, r := range got {
		// Compare bits, not values: NaN would compare unequal to itself and
		// -0.0 equals +0.0, so == would hide exactly the cases worth pinning.
		if math.Float64bits(r.DollarAmount) != math.Float64bits(amounts[i]) {
			t.Errorf("row %d: got %v, want %v (bits %x vs %x)",
				i, r.DollarAmount, amounts[i],
				math.Float64bits(r.DollarAmount), math.Float64bits(amounts[i]))
		}
	}
}

// Negative zero is the one documented exception to the lossless claim:
// NUMERIC carries no sign of zero, so -0.0 comes back as +0.0. Harmless for
// money, since -0.0 == 0.0 in Go, but the interface doc states it and an
// undocumented exception is how a claim rots. Pinned separately from the
// round-trip property, which asserts bit equality and would fail on it.
func TestNegativeZeroLosesItsSign(t *testing.T) {
	if pgContainer == nil {
		t.Skip("postgres container unavailable")
	}
	store := NewPostgresCommissionRunStore(pgContainer.NewPool(t))
	ctx := context.Background()

	runID, err := store.CreateRun(ctx, "2026-01", validHash)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := store.SaveResults(ctx, runID, "primary", []CommissionResultInput{
		{EarnerID: uuid.New(), DollarAmount: math.Copysign(0, -1), Detail: []byte(`{"v":1}`)},
	}); err != nil {
		t.Fatalf("SaveResults: %v", err)
	}
	got, err := store.GetResults(ctx, runID)
	if err != nil {
		t.Fatalf("GetResults: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].DollarAmount != 0 {
		t.Fatalf("DollarAmount = %v, want zero", got[0].DollarAmount)
	}
	if math.Signbit(got[0].DollarAmount) {
		t.Fatal("negative zero kept its sign; the doc says NUMERIC drops it, so one of the two is wrong")
	}
}

// TestSaveResultsRejectsNonFiniteAmounts confirms the copy source refuses NaN
// and infinities before they reach the database, so the caller gets a named
// earner rather than a constraint violation.
func TestSaveResultsRejectsNonFiniteAmounts(t *testing.T) {
	if pgContainer == nil {
		t.Skip("postgres container unavailable")
	}
	store := NewPostgresCommissionRunStore(pgContainer.NewPool(t))
	ctx := context.Background()

	runID, err := store.CreateRun(ctx, "2026-01", validHash)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// A slice, not a map: map iteration order is randomized, and subtest
	// order should be stable.
	cases := []struct {
		name   string
		amount float64
	}{
		{"NaN", math.NaN()},
		{"+Inf", math.Inf(1)},
		{"-Inf", math.Inf(-1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := store.SaveResults(ctx, runID, "primary", []CommissionResultInput{
				{EarnerID: uuid.New(), DollarAmount: tc.amount, Detail: []byte(`{"v":1}`)},
			})
			if err == nil {
				t.Fatalf("expected SaveResults to reject %s", tc.name)
			}
		})
	}

	// A rejected batch must leave nothing behind, or a retry after one bad
	// row would double the good ones.
	got, err := store.GetResults(ctx, runID)
	if err != nil {
		t.Fatalf("GetResults: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("rejected batches left %d rows behind", len(got))
	}
}
