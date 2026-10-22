package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mlmforge/mlmforge/internal/dec"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDollarAmountRoundTripsThroughNumeric checks that an exact decimal
// written to NUMERIC reads back with the same value.
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

	amounts := []dec.Amount{
		dec.MustParse("0"), dec.MustParse("1"), dec.MustParse("-1"),
		dec.MustParse("0.1"), dec.MustParse("0.2"), dec.MustParse("0.3"),
		dec.MustParse("0.3333333333333333333333333"),
		dec.MustParse("123456.789"),
		dec.MustParse("1e-60"),
		dec.MustParse("1e60"),
		dec.MustParse("-1e60"),
		// Clawbacks are why negatives must survive, and the finite-range
		// CHECK is the constraint most likely to be tightened by mistake.
		dec.MustParse("-0.01"), dec.MustParse("-123456.789"),
		// More significant digits than a float64 holds.
		dec.MustParse("12345678901234567.890123"),
	}
	for i := 0; i < 200; i++ {
		amounts = append(amounts, dec.Amount{Decimal: decimal.New(rng.Int63n(2_000_000_000_000_000)-1_000_000_000_000_000, -int32(rng.Intn(14)))})
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
		if !r.DollarAmount.Equal(amounts[i].Decimal) {
			t.Errorf("row %d: got %v, want %v", i, r.DollarAmount, amounts[i])
		}
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
		{EarnerID: uuid.New(), DollarAmount: dec.MustParse("10"), Detail: []byte(`{"v":1}`)},
		{EarnerID: uuid.New(), DollarAmount: dec.MustParse("20"), Detail: []byte(`{"v":1}`)},
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
		{EarnerID: uuid.New(), DollarAmount: dec.MustParse("30"), Detail: poison},
	})
	if err == nil {
		t.Fatal("premise broken: Postgres should reject a lone surrogate in jsonb")
	}
	// Pin which rejection. A type-only check would also accept an error from
	// the run lock or anything else in the transaction, and this test's whole
	// premise is that the failure lands inside CopyFrom, after the DELETE.
	//
	// 22P02 is invalid_text_representation. Postgres reports an unpaired
	// surrogate as malformed json input, not as 22P05
	// untranslatable_character — that one is for encoding conversion.
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "22P02" {
		t.Fatalf("want invalid json input (22P02) from the copy, got %v", err)
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
	if !after[0].DollarAmount.Equal(dec.MustParse("10").Decimal) || !after[1].DollarAmount.Equal(dec.MustParse("20").Decimal) {
		t.Fatalf("prior rows changed: %v, %v", after[0].DollarAmount, after[1].DollarAmount)
	}
}

func TestCommissionEarningDTOReadsTheEngineNumberExactly(t *testing.T) {
	var e CommissionEarningDTO
	require.NoError(t, json.Unmarshal([]byte(`{"earner_id":"00000000-0000-0000-0000-000000000001","source_id":"00000000-0000-0000-0000-000000000002","level":1,"rate":0.07,"cv_amount":100,"dollar_amount":7,"walk":0}`), &e))
	assert.Equal(t, "7", e.DollarAmount.String())
	assert.Equal(t, "0.07", e.Rate.String())
}

func TestCommissionEarningDTORefusesAQuotedAmount(t *testing.T) {
	var e CommissionEarningDTO
	err := json.Unmarshal([]byte(`{"dollar_amount":"7"}`), &e)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bare JSON number")
}
