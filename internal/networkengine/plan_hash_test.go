package networkengine

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanHashIsStableForIdenticalBytes(t *testing.T) {
	in := json.RawMessage(`{"name":"demo","version":1}`)

	first, err := PlanHash(in)
	if err != nil {
		t.Fatalf("PlanHash: %v", err)
	}
	second, err := PlanHash(in)
	if err != nil {
		t.Fatalf("PlanHash: %v", err)
	}
	if first != second {
		t.Fatalf("hash is not stable: %q vs %q", first, second)
	}
}

func TestPlanHashChangesWithContent(t *testing.T) {
	a, err := PlanHash(json.RawMessage(`{"name":"demo","version":1}`))
	if err != nil {
		t.Fatalf("PlanHash a: %v", err)
	}
	b, err := PlanHash(json.RawMessage(`{"name":"demo","version":2}`))
	if err != nil {
		t.Fatalf("PlanHash b: %v", err)
	}
	if a == b {
		t.Fatal("a changed plan must produce a different hash")
	}
}

func TestPlanHashFormat(t *testing.T) {
	got, err := PlanHash(json.RawMessage(`{"name":"demo"}`))
	if err != nil {
		t.Fatalf("PlanHash: %v", err)
	}
	if !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("hash = %q, want a sha256: prefix", got)
	}
	// "sha256:" plus 64 hex characters.
	if len(got) != 7+64 {
		t.Fatalf("len = %d, want 71", len(got))
	}
}

// The format is defined twice more — by planHashPattern, which both stores
// validate against, and by the plan_hash CHECK in migration 000005. A hash
// this function produces has to satisfy them, or CreateRun rejects its own
// caller's input.
func TestPlanHashOutputPassesValidation(t *testing.T) {
	got, err := PlanHash(json.RawMessage(`{"name":"demo"}`))
	if err != nil {
		t.Fatalf("PlanHash: %v", err)
	}
	if err := validatePlanHashOnly(got); err != nil {
		t.Fatalf("PlanHash output was rejected by the store's own validator: %v", err)
	}
}

// TestPlanHashGolden pins the exact digest for a fixed input. It fails if the
// algorithm, the prefix, or the encoding ever changes, which would silently
// re-identify every future run.
func TestPlanHashGolden(t *testing.T) {
	// printf '%s' '{"name":"demo"}' | sha256sum
	const want = "sha256:d7d234f759ec34fd6298b7e32318614760070aaef9f4e92ced928324b49a0602"

	got, err := PlanHash(json.RawMessage(`{"name":"demo"}`))
	if err != nil {
		t.Fatalf("PlanHash: %v", err)
	}
	if got != want {
		t.Fatalf("PlanHash = %q, want %q", got, want)
	}
}

// Identity is over the exact bytes, deliberately — there is no
// canonicalization step. Two payloads that a JSON parser would call equal
// hash differently if their bytes differ. This looks like a bug from the
// outside, so it is pinned as intent: the pipeline emits deterministically,
// and a binary that formats differently may also calculate differently.
func TestPlanHashIsOverExactBytesNotSemantics(t *testing.T) {
	compact, err := PlanHash(json.RawMessage(`{"a":1,"b":2}`))
	if err != nil {
		t.Fatalf("PlanHash compact: %v", err)
	}
	spaced, err := PlanHash(json.RawMessage(`{"a": 1, "b": 2}`))
	if err != nil {
		t.Fatalf("PlanHash spaced: %v", err)
	}
	if compact == spaced {
		t.Fatal("whitespace-different payloads hashed alike; a canonicalization step crept in")
	}

	reordered, err := PlanHash(json.RawMessage(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatalf("PlanHash reordered: %v", err)
	}
	if compact == reordered {
		t.Fatal("key-reordered payloads hashed alike; a canonicalization step crept in")
	}
}

func TestPlanHashRejectsEmptyInput(t *testing.T) {
	if _, err := PlanHash(nil); err == nil {
		t.Fatal("expected an error for empty input")
	}
	if _, err := PlanHash(json.RawMessage{}); err == nil {
		t.Fatal("expected an error for an empty non-nil slice")
	}
}

func TestPlanHashRejectsInvalidJSON(t *testing.T) {
	cases := []string{
		`{not json`,
		`{"a":1}{"b":2}`, // two values, not one payload
		`{"a":1} trailing`,
		`"a string"`, // valid JSON, but a plan is an object
		`[1,2]`,      // valid JSON, but a plan is an object
	}
	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			if _, err := PlanHash(json.RawMessage(tc)); err == nil {
				t.Fatalf("expected an error for %q", tc)
			}
		})
	}
}
