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
		// The extremes, not a scaled-down stand-in. Under 'f' with -1
		// precision these format to 309 and 326 characters, against NUMERIC
		// limits of 131072 integer and 16383 fractional digits — so no
		// float64 exists that NUMERIC cannot hold, and this pins both ends.
		math.MaxFloat64,
		-math.MaxFloat64,
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
	// got[i] lines up with amounts[i] because BIGSERIAL ids ascend in COPY
	// order, GetResults sorts by id, and NewPool truncates with RESTART
	// IDENTITY. The suite pins the ordering half separately.
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

// SaveResults is DELETE-then-COPY inside one transaction, so a write that
// fails partway must leave the structure's prior rows intact. Nothing pinned
// that: the suite's malformed-input cases all write their bad batch first, so
// they never exercise a rollback over existing rows, and the risk is losing
// the old rows rather than duplicating them.
//
// Reaching the failure requires input the Go validator accepts and Postgres
// refuses, since validateResultInputs runs before the transaction opens and
// pre-empts every other bad value. checkJSONObject documents exactly such a
// case: Go's decoder replaces an unpaired surrogate with U+FFFD and accepts,
// while Postgres rejects it at the jsonb parse. So this doubles as proof that
// the documented divergence is real.
func TestAFailedWriteLeavesPriorRowsIntact(t *testing.T) {
	if pgContainer == nil {
		t.Skip("postgres container unavailable")
	}
	store := NewPostgresCommissionRunStore(pgContainer.NewPool(t))
	ctx := context.Background()

	runID, err := store.CreateRun(ctx, "2026-01", validHash)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	good := []CommissionResultInput{
		{EarnerID: uuid.New(), DollarAmount: 10, Detail: []byte(`{"v":1}`)},
		{EarnerID: uuid.New(), DollarAmount: 20, Detail: []byte(`{"v":1}`)},
	}
	if err := store.SaveResults(ctx, runID, "primary", good); err != nil {
		t.Fatalf("seed SaveResults: %v", err)
	}

	// Six characters — backslash, u, d, 8, 0, 0 — assembled rather than
	// written as one literal so nothing in the toolchain folds it into an
	// actual code point.
	loneSurrogate := `\u` + "d800"
	poison := []byte(`{"v":1,"note":"` + loneSurrogate + `"}`)
	if err := checkJSONObject(poison); err != nil {
		t.Fatalf("premise broken: Go should accept the lone surrogate, got %v", err)
	}

	err = store.SaveResults(ctx, runID, "primary", []CommissionResultInput{
		{EarnerID: uuid.New(), DollarAmount: 30, Detail: poison},
	})
	if err == nil {
		t.Fatal("premise broken: Postgres should reject a lone surrogate in jsonb")
	}

	// The rollback is the point. A DELETE that committed without its COPY
	// would show zero rows here, silently destroying a structure's earnings.
	after, err := store.GetResults(ctx, runID)
	if err != nil {
		t.Fatalf("GetResults: %v", err)
	}
	if len(after) != len(good) {
		t.Fatalf("got %d rows after a failed write, want the original %d; the DELETE was not rolled back",
			len(after), len(good))
	}
	if after[0].DollarAmount != 10 || after[1].DollarAmount != 20 {
		t.Fatalf("prior rows changed: %v, %v", after[0].DollarAmount, after[1].DollarAmount)
	}
}
