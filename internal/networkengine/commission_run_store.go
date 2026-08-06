package networkengine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// CommissionRunStatus is the lifecycle state of a commission run.
type CommissionRunStatus string

const (
	RunStatusRunning  CommissionRunStatus = "running"
	RunStatusComplete CommissionRunStatus = "complete"
	RunStatusVoided   CommissionRunStatus = "voided"
)

// CommissionRun is the read shape for one run.
type CommissionRun struct {
	ID           uuid.UUID
	PeriodID     string
	PlanHash     string
	Status       CommissionRunStatus
	CarryForward json.RawMessage
	StartedAt    time.Time
	CompletedAt  *time.Time
	VoidedAt     *time.Time
	SupersededBy *uuid.UUID
}

// CommissionResultInput is the write shape for one earning. RunID and
// Structure come from the SaveResults call rather than from each element, so
// there is no mismatch case to define. ID is omitted because the database
// assigns it and pgx.CopyFrom cannot return generated values.
type CommissionResultInput struct {
	EarnerID     uuid.UUID
	DollarAmount float64
	Detail       json.RawMessage
}

// CommissionResult is the read shape for one persisted earning.
//
// DollarAmount is float64 because that is what the engine emits and what
// CommissionEarningDTO carries. The Postgres store converts to NUMERIC on
// write via strconv.FormatFloat(v, 'f', -1, 64), the shortest representation
// that round-trips, so the Go boundary is lossless both ways. NUMERIC still
// buys exact, order-independent SUM in SQL, which is why it was chosen.
//
// Caveat for future readers: a SQL-side SUM over a million rows may not be
// exactly representable as float64. A payout reader should pull that total as
// NUMERIC text, not as a float.
type CommissionResult struct {
	ID           int64
	RunID        uuid.UUID
	Structure    string
	EarnerID     uuid.UUID
	DollarAmount float64
	Detail       json.RawMessage
}

// CommissionRunStore persists commission runs and their results.
//
// It is the system of record, not a projection. Design-rationale 027
// establishes that commission data must never depend on EventStore retention.
//
// Completion is the visibility flip. A running run's partial results are
// invisible to GetLiveResults, which is what lets a bulk write happen outside
// the replacement transaction without ever exposing a half-written run.
//
// Input validation is part of the contract, not the database's job alone.
// Both implementations reject the same malformed input through the shared
// validate* helpers below, so a test that passes against the memory store
// also passes against Postgres. MemoryQualificationHistoryStore.SaveResult
// takes the same approach and says so for the same reason.
//
// Context cancellation is honored by the Postgres implementation, because pgx
// honors it. The memory implementation does no I/O and does not check ctx.
// Callers must not rely on a memory-store call observing cancellation.
type CommissionRunStore interface {
	// CreateRun opens a new run for periodID. Returns *LiveRunExistsError,
	// carrying the existing run's ID, when a non-voided run already exists
	// for that period.
	CreateRun(ctx context.Context, periodID, planHash string) (uuid.UUID, error)

	// SaveResults replaces every row for (runID, structure) with results.
	// Calling it twice for the same pair leaves one copy, which is what makes
	// a retry after an uncertain commit safe. Different structures accumulate
	// independently under one run.
	//
	// Passing an empty results slice deletes the structure's rows and writes
	// none, which is how a structure that earned nothing is recorded.
	//
	// Returns *RunNotFoundError when the run does not exist, and
	// *RunNotRunningError when it exists but is not running.
	SaveResults(ctx context.Context, runID uuid.UUID, structure string,
		results []CommissionResultInput) error

	// CompleteRun marks the run complete and records the carry-forward it
	// produced. This is the point at which the run's results become visible
	// to GetLiveResults.
	//
	// carryForward may be nil, meaning the run produced no carry-over state.
	// An empty non-nil slice means the same thing: implementations must
	// normalize len == 0 to nil on write. This is not something the driver
	// does for you — pgx encodes only a nil slice as SQL NULL, and an empty
	// non-nil one is sent as an empty payload that fails the jsonb insert.
	//
	// Returns *RunNotFoundError when the run does not exist, and
	// *RunNotRunningError when it exists but is not running.
	CompleteRun(ctx context.Context, runID uuid.UUID, carryForward json.RawMessage) error

	// VoidRun voids a run without a replacement. Valid from running or
	// complete. This is how a run left running by a crash is cleared.
	//
	// It does not set superseded_by. Only ReplaceRun does, inside the
	// transaction where it already holds the old run's row lock and has read
	// its period_id. That is the one place the same-period rule can be
	// enforced without taking a second lock, so exposing the link here would
	// be a path around the check with no caller needing it.
	//
	// Returns *RunNotFoundError when the run does not exist. Voiding an
	// already-voided run is a no-op returning nil, so a retry is safe.
	VoidRun(ctx context.Context, runID uuid.UUID) error

	// ReplaceRun voids oldRunID and opens its replacement for the same period
	// in one transaction. This is ADR-013 scenario 2. Valid when oldRunID is
	// running or complete.
	//
	// Returns *RunNotFoundError when oldRunID does not exist, and
	// *RunNotRunningError with Allowed set to {running, complete} when it is
	// already voided.
	ReplaceRun(ctx context.Context, oldRunID uuid.UUID, planHash string) (uuid.UUID, error)

	// GetRun returns one run, or *RunNotFoundError.
	GetRun(ctx context.Context, runID uuid.UUID) (*CommissionRun, error)

	// GetActiveRun returns the period's non-voided run whatever its status,
	// or (nil, nil) when there is none. Operators use it to find a run left
	// running by a crash.
	GetActiveRun(ctx context.Context, periodID string) (*CommissionRun, error)

	// GetResults returns one run's results ordered by id ascending,
	// regardless of the run's status.
	GetResults(ctx context.Context, runID uuid.UUID) ([]CommissionResult, error)

	// GetLiveResults returns the period's current results, ordered by id
	// ascending. Empty when the period has no run, when its run is still
	// running, or when its run is voided. Implementations must resolve the
	// run and read its rows atomically so a replacement landing mid-read
	// cannot produce a mix.
	GetLiveResults(ctx context.Context, periodID string) ([]CommissionResult, error)
}

// LiveRunExistsError reports that a period already has a non-voided run.
// ExistingRunID makes the condition recoverable: a caller that loses the race
// can read the winner instead of only failing.
type LiveRunExistsError struct {
	PeriodID      string
	ExistingRunID uuid.UUID
}

func (e *LiveRunExistsError) Error() string {
	return fmt.Sprintf("period %q already has an active commission run %s",
		e.PeriodID, e.ExistingRunID)
}

// RunNotFoundError reports that no run exists with the given ID.
type RunNotFoundError struct {
	RunID uuid.UUID
}

func (e *RunNotFoundError) Error() string {
	return fmt.Sprintf("commission run %s not found", e.RunID)
}

// RunNotRunningError reports an operation being attempted against a run in a
// state that operation does not accept.
type RunNotRunningError struct {
	RunID  uuid.UUID
	Status CommissionRunStatus

	// Allowed names the states the attempted operation does accept. Empty
	// means running only, which covers SaveResults and CompleteRun.
	//
	// ReplaceRun sets it, because it accepts running or complete. Without
	// that, replacing a voided run would report "is voided, not running",
	// telling an operator the run had to be running when a complete one
	// would have worked too.
	Allowed []CommissionRunStatus
}

func (e *RunNotRunningError) Error() string {
	if len(e.Allowed) == 0 {
		return fmt.Sprintf("commission run %s is %s, not running", e.RunID, e.Status)
	}
	names := make([]string, len(e.Allowed))
	for i, s := range e.Allowed {
		names[i] = string(s)
	}
	return fmt.Sprintf("commission run %s is %s, must be %s",
		e.RunID, e.Status, strings.Join(names, " or "))
}

// planHashPattern mirrors the plan_hash CHECK in migration 000005.
var planHashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// The validate* helpers below mirror the table CHECK constraints so both
// implementations reject the same input. Without them the memory store
// accepts values Postgres refuses, and a test that passes in memory fails in
// production — which is the exact trap the shared suite exists to prevent.
//
// These return plain errors, not typed ones, matching
// MemoryQualificationHistoryStore.SaveResult. They report a caller bug, not a
// condition a caller recovers from, so there is nothing to branch on.

func validateRunInput(periodID, planHash string) error {
	if periodID == "" {
		return fmt.Errorf("commission run: period_id must be non-empty")
	}
	return validatePlanHashOnly(planHash)
}

// validatePlanHashOnly is the hash half, for ReplaceRun. That path inherits
// period_id from the run it is replacing, so there is nothing to check there,
// and the hash must be validated before the transaction opens rather than
// after the old run is already voided.
func validatePlanHashOnly(planHash string) error {
	if !planHashPattern.MatchString(planHash) {
		return fmt.Errorf("commission run: plan_hash %q must match sha256:<64 hex>", planHash)
	}
	return nil
}

// checkJSONObject reports whether raw is a JSON object, approximating what
// Postgres accepts into a JSONB column carrying
// CHECK (jsonb_typeof(col) = 'object').
//
// It decodes into any and type-asserts rather than unmarshaling into
// map[string]any directly. The direct form looks equivalent but silently
// accepts the JSON literal null: json.Unmarshal([]byte("null"), &m) returns a
// nil error and leaves the map nil. Postgres rejects that value, because
// jsonb_typeof('null'::jsonb) is 'null', not 'object'.
//
// UseNumber keeps big numbers out of float64. Without it, {"a":1e400} fails
// here while Postgres accepts it, since jsonb numbers are NUMERIC — a
// rejection Postgres would not have made.
//
// Known limits, because Go's decoder is not Postgres's jsonb parser and this
// does not reimplement one. Two cases still pass here that Postgres rejects,
// both confined to escape sequences inside strings:
//
//   - a \u0000 escape — Postgres cannot represent NUL in text.
//   - an unpaired surrogate such as "\ud800" — Go replaces it with U+FFFD
//     during decoding, so it is indistinguishable afterward from a legitimate
//     U+FFFD in the input.
//
// Neither is reachable from the values this package writes today, which are
// built from UUIDs, numbers, and fixed enum strings. A caller putting
// free-form text into Detail or CarryForward would meet them as a
// Postgres-only insert failure.
func checkJSONObject(raw json.RawMessage) error {
	// Explicit, so the wrapped message says what happened. Falling through to
	// the decoder works but surfaces a bare "EOF". TrimSpace so whitespace-only
	// input takes this branch too, rather than the EOF path this exists to
	// avoid.
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("no value")
	}
	// Raw invalid UTF-8 bytes: Go substitutes U+FFFD and accepts, Postgres
	// rejects at the encoding layer. Cheap to catch here, unlike the escape
	// cases above.
	if !utf8.Valid(raw) {
		return fmt.Errorf("not valid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var probe any
	if err := d.Decode(&probe); err != nil {
		return err
	}
	// Decode stops after one value, so trailing junk needs its own check or
	// `{} garbage` would pass where json.Unmarshal rejected it.
	//
	// Not Decoder.More: that exists for streaming the elements of a container
	// and returns false on ']' or '}', so it reads a stray closing brace as
	// end-of-input rather than as data. `{}}` would slip through. Draining to
	// io.EOF has no such blind spot.
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("unexpected data after the JSON value")
	}
	if _, ok := probe.(map[string]any); !ok {
		return fmt.Errorf("want a JSON object, got %s", jsonKind(probe))
	}
	return nil
}

// jsonKind names the JSON type of a decoded value, for error messages. The
// decoder produces only these kinds plus map[string]any, which the caller
// handles before calling.
func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	default:
		return "an unexpected type"
	}
}

// validateCarryForward allows nil, meaning the run produced no carry-over
// state. A present value must be a JSON object, matching the table CHECK.
func validateCarryForward(cf json.RawMessage) error {
	if len(cf) == 0 {
		return nil
	}
	if err := checkJSONObject(cf); err != nil {
		return fmt.Errorf("commission run: carry_forward must be a JSON object: %w", err)
	}
	return nil
}

func validateResultInputs(structure string, results []CommissionResultInput) error {
	if structure == "" {
		return fmt.Errorf("commission results: structure must be non-empty")
	}
	for i, r := range results {
		if r.EarnerID == uuid.Nil {
			return fmt.Errorf("commission results: row %d has a nil earner id", i)
		}
		if math.IsNaN(r.DollarAmount) || math.IsInf(r.DollarAmount, 0) {
			return fmt.Errorf("commission results: row %d (earner %s): dollar_amount must be finite, got %v",
				i, r.EarnerID, r.DollarAmount)
		}
		// A nil or empty Detail is rejected by checkJSONObject. That mirrors
		// detail JSONB NOT NULL. Unlike carry_forward there is deliberately
		// no shortcut here letting it pass: an earning with no detail is a
		// caller bug, not an absent-value case.
		if err := checkJSONObject(r.Detail); err != nil {
			return fmt.Errorf("commission results: row %d (earner %s): detail must be a JSON object: %w",
				i, r.EarnerID, err)
		}
	}
	return nil
}

// cloneRaw copies a json.RawMessage so the store and its caller never share a
// backing array. json.RawMessage is a []byte, so a shallow struct copy leaves
// them aliased: a caller mutating its input after a write would silently
// change memory-store state, and a caller mutating a returned value would
// corrupt the store. Postgres hands back independent bytes, so cloning is
// also what keeps the two implementations behaving alike.
//
// Empty collapses to nil rather than round-tripping as an empty slice. pgx
// encodes only a nil slice as SQL NULL; an empty non-nil one is sent as an
// empty payload and fails the jsonb insert. Preserving the distinction here
// would make the two stores disagree on a value the shared suite compares.
func cloneRaw(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	out := make(json.RawMessage, len(b))
	copy(out, b)
	return out
}
