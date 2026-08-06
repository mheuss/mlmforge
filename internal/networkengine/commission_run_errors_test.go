package networkengine

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestLiveRunExistsErrorCarriesTheWinner(t *testing.T) {
	winner := uuid.New()
	var err error = &LiveRunExistsError{PeriodID: "2026-01", ExistingRunID: winner}

	var target *LiveRunExistsError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *LiveRunExistsError")
	}
	if target.ExistingRunID != winner {
		t.Fatalf("ExistingRunID = %s, want %s", target.ExistingRunID, winner)
	}
	if !strings.Contains(err.Error(), "2026-01") {
		t.Fatalf("message should name the period, got %q", err.Error())
	}
}

func TestLiveRunExistsErrorSurvivesWrapping(t *testing.T) {
	inner := &LiveRunExistsError{PeriodID: "2026-01", ExistingRunID: uuid.New()}
	wrapped := fmt.Errorf("create run: %w", inner)

	var target *LiveRunExistsError
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As should unwrap through fmt.Errorf")
	}
}

func TestRunNotFoundErrorMatches(t *testing.T) {
	id := uuid.New()
	var err error = &RunNotFoundError{RunID: id}

	var target *RunNotFoundError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *RunNotFoundError")
	}
	if !strings.Contains(err.Error(), id.String()) {
		t.Fatalf("message should name the run, got %q", err.Error())
	}
}

func TestRunNotRunningErrorReportsActualStatus(t *testing.T) {
	var err error = &RunNotRunningError{RunID: uuid.New(), Status: RunStatusVoided}

	var target *RunNotRunningError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *RunNotRunningError")
	}
	if target.Status != RunStatusVoided {
		t.Fatalf("Status = %q, want %q", target.Status, RunStatusVoided)
	}
	if !strings.Contains(err.Error(), string(RunStatusVoided)) {
		t.Fatalf("message should name the status, got %q", err.Error())
	}
}

// The validate* helpers mirror migration 000005's CHECK constraints. These
// tests pin that mirroring, because the whole point is that the memory store
// refuses exactly what Postgres refuses.

func TestValidateRunInput(t *testing.T) {
	const goodHash = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	cases := []struct {
		name     string
		periodID string
		planHash string
		wantErr  bool
	}{
		{"valid", "2026-01", goodHash, false},
		{"empty period", "", goodHash, true},
		{"hash without prefix", "2026-01", "0123456789abcdef", true},
		{"prefix with no digest", "2026-01", "sha256:", true},
		{"prefix with junk", "2026-01", "sha256:anything", true},
		{"digest too short", "2026-01", "sha256:abc", true},
		{"uppercase hex", "2026-01", "sha256:" + strings.ToUpper("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRunInput(tc.periodID, tc.planHash)
			if tc.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateCarryForward(t *testing.T) {
	if err := validateCarryForward(nil); err != nil {
		t.Fatalf("nil carry-forward is allowed, got: %v", err)
	}
	if err := validateCarryForward(json.RawMessage(`{"v":1}`)); err != nil {
		t.Fatalf("an object is allowed, got: %v", err)
	}
	if err := validateCarryForward(json.RawMessage(`[1,2]`)); err == nil {
		t.Fatal("an array should be rejected")
	}
	if err := validateCarryForward(json.RawMessage(`{oops`)); err == nil {
		t.Fatal("malformed JSON should be rejected")
	}
}

// The JSON literal null is the case a map[string]any probe misses:
// json.Unmarshal succeeds and leaves the map nil. Postgres rejects it,
// because jsonb_typeof('null'::jsonb) is 'null', not 'object'. Letting it
// through would be a memory-vs-Postgres divergence, which is exactly what
// the shared suite exists to prevent.
func TestValidateCarryForwardRejectsJSONNull(t *testing.T) {
	if err := validateCarryForward(json.RawMessage(`null`)); err == nil {
		t.Fatal("the JSON literal null should be rejected; Postgres refuses it")
	}
}

func TestValidateResultInputs(t *testing.T) {
	ok := []CommissionResultInput{
		{EarnerID: uuid.New(), DollarAmount: 1.5, Detail: json.RawMessage(`{"v":1}`)},
	}
	if err := validateResultInputs("primary", ok); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	if err := validateResultInputs("", ok); err == nil {
		t.Fatal("an empty structure should be rejected")
	}
	if err := validateResultInputs("primary", []CommissionResultInput{
		{EarnerID: uuid.Nil, DollarAmount: 1, Detail: json.RawMessage(`{"v":1}`)},
	}); err == nil {
		t.Fatal("a nil earner id should be rejected")
	}
	if err := validateResultInputs("primary", []CommissionResultInput{
		{EarnerID: uuid.New(), DollarAmount: math.NaN(), Detail: json.RawMessage(`{"v":1}`)},
	}); err == nil {
		t.Fatal("NaN should be rejected")
	}
	if err := validateResultInputs("primary", []CommissionResultInput{
		{EarnerID: uuid.New(), DollarAmount: math.Inf(1), Detail: json.RawMessage(`{"v":1}`)},
	}); err == nil {
		t.Fatal("infinity should be rejected")
	}
	if err := validateResultInputs("primary", []CommissionResultInput{
		{EarnerID: uuid.New(), DollarAmount: 1, Detail: json.RawMessage(`[1,2]`)},
	}); err == nil {
		t.Fatal("a non-object detail should be rejected")
	}
}

// Same reason as TestValidateCarryForwardRejectsJSONNull: the detail column
// carries CHECK (jsonb_typeof(detail) = 'object'), so a JSON null that
// passes here would fail at the database.
func TestValidateResultInputsRejectsJSONNullDetail(t *testing.T) {
	err := validateResultInputs("primary", []CommissionResultInput{
		{EarnerID: uuid.New(), DollarAmount: 1, Detail: json.RawMessage(`null`)},
	})
	if err == nil {
		t.Fatal("a JSON null detail should be rejected; Postgres refuses it")
	}
}

// Negative amounts are legal — clawbacks are the reason — and the database
// CHECK only excludes non-finite values. A tightening to >= 0 in either
// layer has to fail a test.
func TestValidateResultInputsAllowsNegativeAndZero(t *testing.T) {
	for _, amount := range []float64{-12.34, 0, 12.34} {
		err := validateResultInputs("primary", []CommissionResultInput{
			{EarnerID: uuid.New(), DollarAmount: amount, Detail: json.RawMessage(`{"v":1}`)},
		})
		if err != nil {
			t.Fatalf("dollar amount %v should be allowed, got: %v", amount, err)
		}
	}
}

// TestCloneRawBreaksAliasing pins the property that keeps the memory store
// from sharing a mutable buffer with its caller.
func TestCloneRawBreaksAliasing(t *testing.T) {
	if cloneRaw(nil) != nil {
		t.Fatal("cloning nil should stay nil")
	}
	original := json.RawMessage(`{"v":1}`)
	clone := cloneRaw(original)
	if string(clone) != string(original) {
		t.Fatalf("clone = %s, want %s", clone, original)
	}
	original[2] = 'X'
	if string(clone) == string(original) {
		t.Fatal("mutating the original changed the clone; they share a backing array")
	}
}
