package networkengine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// PlanHash returns a stable content hash of the engine-facing plan JSON.
//
// The input is stage-5 output of the config pipeline (ADR-018) — the
// jsonBytes return of config.Pipeline.LoadAndValidate, produced by
// translateToEngine. That is the exact byte sequence handed to the worker's
// load_plan op.
//
// Identity is over those exact bytes. There is no JSON canonicalization step.
// translateToEngine marshals from typed Go values and map[string]any, both of
// which encoding/json emits deterministically (struct fields in declaration
// order, map keys sorted), so the bytes are already stable for a given
// binary. A generic canonicalizer would add a step that cannot be tested
// against real variation, and it still would not make semantically equal
// numbers like 1 and 1.0 hash alike, because the pipeline never emits both
// for the same field.
//
// A consequence worth stating: changing a config struct's field order or a
// serde rename changes the hash for an unchanged YAML plan. That is correct
// for replay integrity. A different binary may compute commissions
// differently, and the pinned identity should say so.
//
// The returned form is "sha256:<hex>" so the algorithm is visible in stored
// data and can be migrated without guessing what old rows used. The
// commission_runs table has a CHECK enforcing that prefix, and
// validatePlanHashOnly enforces the same shape in Go.
func PlanHash(engineJSON json.RawMessage) (string, error) {
	if len(engineJSON) == 0 {
		return "", fmt.Errorf("plan hash: engine JSON is empty")
	}
	// Validate rather than transform. This catches a caller passing YAML, a
	// truncated buffer, or a fragment, without changing the bytes hashed.
	//
	// checkJSONObject rather than json.Valid: a plan is an object, and
	// json.Valid accepts a bare string or array, handing back a
	// legitimate-looking hash for something that is not a plan. (Both reject
	// trailing data, so that is not the difference.)
	if err := checkJSONObject(engineJSON); err != nil {
		return "", fmt.Errorf("plan hash: invalid engine JSON: %w", err)
	}
	sum := sha256.Sum256(engineJSON)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
