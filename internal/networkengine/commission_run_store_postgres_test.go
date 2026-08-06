package networkengine

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/google/uuid"
)

// The copy source is the bypass path named by docs/development/config-types.md:
// bytes reach the money table through Values, and SaveResults' own validation
// is not on that path if a future caller builds the source directly. These
// exercise it head-on, since going through SaveResults never reaches these
// branches.
func TestCommissionResultCopySourceRejectsBadRows(t *testing.T) {
	cases := []struct {
		name string
		in   CommissionResultInput
	}{
		{"NaN", CommissionResultInput{EarnerID: uuid.New(), DollarAmount: math.NaN(), Detail: json.RawMessage(`{"v":1}`)}},
		{"positive infinity", CommissionResultInput{EarnerID: uuid.New(), DollarAmount: math.Inf(1), Detail: json.RawMessage(`{"v":1}`)}},
		{"negative infinity", CommissionResultInput{EarnerID: uuid.New(), DollarAmount: math.Inf(-1), Detail: json.RawMessage(`{"v":1}`)}},
		// Must not become {}. detail is NOT NULL with a jsonb_typeof CHECK,
		// and defaulting would persist a caller bug as if it were deliberate.
		{"nil detail", CommissionResultInput{EarnerID: uuid.New(), DollarAmount: 1, Detail: nil}},
		{"empty detail", CommissionResultInput{EarnerID: uuid.New(), DollarAmount: 1, Detail: json.RawMessage{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := newCommissionResultCopySource(uuid.New(), "primary", []CommissionResultInput{tc.in})
			if !src.Next() {
				t.Fatal("Next should yield the one row")
			}
			if _, err := src.Values(); err == nil {
				t.Fatal("expected Values to reject the row")
			}
		})
	}
}

// The amount crosses as a decimal string, not as a float, so NUMERIC stores
// exactly what Go had rather than whatever textual form the driver picks.
func TestCommissionResultCopySourceFormatsAmounts(t *testing.T) {
	cases := []struct {
		amount float64
		want   string
	}{
		{10.5, "10.5"},
		{-12.34, "-12.34"},
		{0, "0"},
		{0.1, "0.1"},
		// Would render as 1e-07 under %v, which NUMERIC also accepts but which
		// hides the exact value from anyone reading the table.
		{0.0000001, "0.0000001"},
		{1234567890.123456, "1234567890.123456"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			src := newCommissionResultCopySource(uuid.New(), "primary", []CommissionResultInput{
				{EarnerID: uuid.New(), DollarAmount: tc.amount, Detail: json.RawMessage(`{"v":1}`)},
			})
			if !src.Next() {
				t.Fatal("Next should yield the one row")
			}
			vals, err := src.Values()
			if err != nil {
				t.Fatalf("Values: %v", err)
			}
			if got := vals[3]; got != tc.want {
				t.Fatalf("dollar_amount = %v, want %q", got, tc.want)
			}
		})
	}
}

// Next must report exhaustion rather than running past the slice, since
// CopyFrom drives it until it returns false.
func TestCommissionResultCopySourceStopsAtTheEnd(t *testing.T) {
	src := newCommissionResultCopySource(uuid.New(), "primary", []CommissionResultInput{
		{EarnerID: uuid.New(), DollarAmount: 1, Detail: json.RawMessage(`{"v":1}`)},
	})
	if !src.Next() {
		t.Fatal("first Next should yield the row")
	}
	if src.Next() {
		t.Fatal("second Next should report exhaustion")
	}
	if err := src.Err(); err != nil {
		t.Fatalf("Err = %v, want nil", err)
	}
}

// An empty batch yields nothing at all — SaveResults skips CopyFrom entirely
// in that case, which is what makes "clear this structure" work.
func TestCommissionResultCopySourceHandlesAnEmptyBatch(t *testing.T) {
	src := newCommissionResultCopySource(uuid.New(), "primary", nil)
	if src.Next() {
		t.Fatal("an empty batch should yield no rows")
	}
}

func TestPostgresCommissionRunStore(t *testing.T) {
	if pgContainer == nil {
		t.Skip("postgres container unavailable")
	}
	runCommissionRunStoreSuite(t, func(t *testing.T) CommissionRunStore {
		return NewPostgresCommissionRunStore(pgContainer.NewPool(t))
	})
}
