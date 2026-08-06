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
	if !strings.Contains(err.Error(), "not running") {
		t.Fatalf("with no Allowed set the message should say not running, got %q", err.Error())
	}
}

// ReplaceRun accepts a complete run as well as a running one. Reporting "not
// running" against a voided run would tell an operator the wrong requirement.
func TestRunNotRunningErrorNamesEveryAllowedState(t *testing.T) {
	var err error = &RunNotRunningError{
		RunID:   uuid.New(),
		Status:  RunStatusVoided,
		Allowed: []CommissionRunStatus{RunStatusRunning, RunStatusComplete},
	}

	msg := err.Error()
	if !strings.Contains(msg, "must be running or complete") {
		t.Fatalf("message should name both allowed states, got %q", msg)
	}
	if strings.Contains(msg, "not running") {
		t.Fatalf("message should not claim running is the only option, got %q", msg)
	}

	var target *RunNotRunningError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should still match with Allowed set")
	}
}

// Unwrapping is a property of fmt.Errorf rather than of each type, but the
// store wraps these on the way out, so it is pinned for each. LiveRunExists
// has its own test above; these are the other two.
func TestTypedErrorsSurviveWrapping(t *testing.T) {
	notFound := &RunNotFoundError{RunID: uuid.New()}
	if wrapped := fmt.Errorf("get run: %w", notFound); !errors.As(wrapped, new(*RunNotFoundError)) {
		t.Fatal("*RunNotFoundError should unwrap through fmt.Errorf")
	}
	notRunning := &RunNotRunningError{RunID: uuid.New(), Status: RunStatusComplete}
	if wrapped := fmt.Errorf("save results: %w", notRunning); !errors.As(wrapped, new(*RunNotRunningError)) {
		t.Fatal("*RunNotRunningError should unwrap through fmt.Errorf")
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
		// TEXT cannot hold either; Postgres rejects both with 22021. The
		// shared suite covers them through both stores, but without a case
		// here, deleting the guard leaves this file green and breaks parity
		// only against a live database.
		{"period with a NUL byte", "2026\x00-01", goodHash, true},
		{"period that is not valid UTF-8", "2026-\xff", goodHash, true},
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

// validatePlanHashOnly exists to be callable without a period, because
// ReplaceRun inherits period_id from the run it replaces and must reject a
// bad hash before opening its transaction. Reaching it only through
// validateRunInput would leave that entry point unpinned.
func TestValidatePlanHashOnly(t *testing.T) {
	good := "sha256:" + strings.Repeat("a", 64)
	if err := validatePlanHashOnly(good); err != nil {
		t.Fatalf("a valid digest was rejected: %v", err)
	}
	for _, bad := range []string{
		"",
		"sha256:",
		"sha256:anything",
		"sha256:" + strings.Repeat("a", 63),
		"sha256:" + strings.Repeat("a", 65),
		good + "\n",
		"\n" + good,
	} {
		if err := validatePlanHashOnly(bad); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
}

func TestValidateCarryForward(t *testing.T) {
	cases := []struct {
		name    string
		input   json.RawMessage
		wantErr bool
	}{
		{"nil means no carry-over state", nil, false},
		{"empty means the same as nil", json.RawMessage(``), false},
		{"an object is allowed", json.RawMessage(`{"v":1}`), false},
		{"an array is rejected", json.RawMessage(`[1,2]`), true},
		{"malformed JSON is rejected", json.RawMessage(`{oops`), true},
		// The JSON literal null is the case a map[string]any probe misses:
		// json.Unmarshal succeeds and leaves the map nil. Postgres rejects
		// it, because jsonb_typeof('null'::jsonb) is 'null', not 'object'.
		{"the JSON literal null is rejected", json.RawMessage(`null`), true},
		{"a bare number is rejected", json.RawMessage(`42`), true},
		{"a bare string is rejected", json.RawMessage(`"hi"`), true},
		{"trailing data after an object is rejected", json.RawMessage(`{} nope`), true},
		{"a second value after an object is rejected", json.RawMessage(`{}{}`), true},
		// A stray closing brace or bracket is the case json.Decoder.More
		// misses: it exists for streaming container elements and treats both
		// as terminators, not as data. Postgres rejects them.
		{"a trailing brace is rejected", json.RawMessage(`{}}`), true},
		{"a trailing bracket is rejected", json.RawMessage(`{}]`), true},
		{"a trailing brace past whitespace is rejected", json.RawMessage(`{"a":1}   }`), true},
		{"whitespace only is rejected", json.RawMessage("  \t\r\n "), true},
		{"whitespace around an object is allowed", json.RawMessage(" \n{\"v\":1}\n "), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCarryForward(tc.input)
			if tc.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateResultInputs(t *testing.T) {
	cases := []struct {
		name      string
		structure string
		input     CommissionResultInput
		wantErr   bool
	}{
		{"valid", "primary",
			CommissionResultInput{EarnerID: uuid.New(), DollarAmount: 1.5, Detail: json.RawMessage(`{"v":1}`)}, false},
		{"empty structure", "",
			CommissionResultInput{EarnerID: uuid.New(), DollarAmount: 1.5, Detail: json.RawMessage(`{"v":1}`)}, true},
		{"structure with a NUL byte", "pri\x00mary",
			CommissionResultInput{EarnerID: uuid.New(), DollarAmount: 1.5, Detail: json.RawMessage(`{"v":1}`)}, true},
		{"structure that is not valid UTF-8", "prim\xffary",
			CommissionResultInput{EarnerID: uuid.New(), DollarAmount: 1.5, Detail: json.RawMessage(`{"v":1}`)}, true},
		{"nil earner id", "primary",
			CommissionResultInput{EarnerID: uuid.Nil, DollarAmount: 1, Detail: json.RawMessage(`{"v":1}`)}, true},
		{"NaN amount", "primary",
			CommissionResultInput{EarnerID: uuid.New(), DollarAmount: math.NaN(), Detail: json.RawMessage(`{"v":1}`)}, true},
		{"positive infinity", "primary",
			CommissionResultInput{EarnerID: uuid.New(), DollarAmount: math.Inf(1), Detail: json.RawMessage(`{"v":1}`)}, true},
		// The migration's lower bound exists for this case specifically.
		{"negative infinity", "primary",
			CommissionResultInput{EarnerID: uuid.New(), DollarAmount: math.Inf(-1), Detail: json.RawMessage(`{"v":1}`)}, true},
		{"array detail", "primary",
			CommissionResultInput{EarnerID: uuid.New(), DollarAmount: 1, Detail: json.RawMessage(`[1,2]`)}, true},
		// detail is JSONB NOT NULL, so unlike carry_forward there is no
		// absent-value case to allow.
		{"nil detail", "primary",
			CommissionResultInput{EarnerID: uuid.New(), DollarAmount: 1, Detail: nil}, true},
		{"empty detail", "primary",
			CommissionResultInput{EarnerID: uuid.New(), DollarAmount: 1, Detail: json.RawMessage(``)}, true},
		{"JSON null detail", "primary",
			CommissionResultInput{EarnerID: uuid.New(), DollarAmount: 1, Detail: json.RawMessage(`null`)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateResultInputs(tc.structure, []CommissionResultInput{tc.input})
			if tc.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// Two places Go's decoder disagrees with Postgres's jsonb parser, both
// closed in checkJSONObject. Without these the helpers would diverge from the
// CHECK constraints they claim to mirror, which is the whole reason they
// exist.
func TestCheckJSONObjectMatchesPostgresOnKnownDivergences(t *testing.T) {
	// jsonb numbers are NUMERIC, so Postgres accepts a value far outside
	// float64. Decoding into float64 would reject it — a rejection Postgres
	// never makes.
	t.Run("a number beyond float64 range is accepted", func(t *testing.T) {
		if err := validateCarryForward(json.RawMessage(`{"a":1e400}`)); err != nil {
			t.Fatalf("Postgres accepts 1e400 in jsonb; this must too, got: %v", err)
		}
	})

	// Go silently substitutes U+FFFD for an invalid byte and accepts.
	// Postgres rejects at the encoding layer.
	t.Run("raw invalid UTF-8 is rejected", func(t *testing.T) {
		bad := append([]byte(`{"a":"`), 0xff, '"', '}')
		if err := validateCarryForward(bad); err == nil {
			t.Fatal("Postgres rejects invalid UTF-8; this must too")
		}
	})
}

// An empty batch is legal: SaveResults with no rows deletes the structure's
// rows and writes none, which is how a structure that earned nothing is
// recorded.
func TestValidateResultInputsAllowsAnEmptyBatch(t *testing.T) {
	if err := validateResultInputs("primary", nil); err != nil {
		t.Fatalf("a nil batch should be allowed, got: %v", err)
	}
	if err := validateResultInputs("primary", []CommissionResultInput{}); err != nil {
		t.Fatalf("an empty batch should be allowed, got: %v", err)
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

// Empty must collapse to nil, not round-trip as an empty slice. Postgres
// writes a zero-length carry-forward as NULL and reads it back as nil, so
// preserving the distinction would make the two stores disagree on a value
// the shared suite compares directly.
func TestCloneRawCollapsesEmptyToNil(t *testing.T) {
	if got := cloneRaw(json.RawMessage{}); got != nil {
		t.Fatalf("cloneRaw(empty) = %#v, want nil", got)
	}
}
