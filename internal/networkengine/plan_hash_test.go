package networkengine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlmforge/mlmforge/internal/config"
)

// PlanHash's doc makes two factual claims about a different package: that
// stage-5 pipeline output is always a JSON object, and that it is byte-stable
// for a given plan. Both hold today, and nothing enforced either. A
// translateToEngine that returned a top-level array, or gained a map-to-slice
// conversion without a sort, would break PlanHash at runtime with every test
// still green.
//
// This runs real plan fixtures through the actual pipeline rather than
// asserting on hand-written JSON, because the claim is about the pipeline.
func TestPlanHashOverRealPipelineOutput(t *testing.T) {
	root := filepath.Join("..", "..")
	pipeline, err := config.NewPipeline(filepath.Join(root, "schemas", "compensation-plan.schema.json"))
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}

	dir := filepath.Join(root, "internal", "config", "testdata", "valid")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var ran int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			yamlBytes, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}

			first, hash := hashPipelineOutput(t, pipeline, yamlBytes)
			// Same input, fresh pipeline run: the hash must not move. A map
			// iterated without sorting would show up here.
			second, again := hashPipelineOutput(t, pipeline, yamlBytes)
			if hash != again {
				t.Fatalf("hash moved across runs of identical input:\n%s\n%s", hash, again)
			}
			if string(first) != string(second) {
				t.Fatal("pipeline output bytes are not stable for identical input")
			}
			if err := validatePlanHashOnly(hash); err != nil {
				t.Fatalf("hash of real pipeline output failed the store's validator: %v", err)
			}
		})
		ran++
	}
	if ran == 0 {
		t.Fatal("no plan fixtures found; this test would pass vacuously")
	}
}

// hashPipelineOutput runs one plan through stage 5 and hashes the result,
// failing on any validation error so a fixture that stops being valid cannot
// quietly skip the assertion.
func hashPipelineOutput(t *testing.T, p *config.Pipeline, yamlBytes []byte) ([]byte, string) {
	t.Helper()
	engineJSON, verrs, err := p.LoadAndValidate(yamlBytes)
	if err != nil {
		t.Fatalf("LoadAndValidate: %v", err)
	}
	for _, ve := range verrs {
		if ve.Severity == config.SeverityError {
			t.Fatalf("fixture failed validation: %+v", ve)
		}
	}
	hash, err := PlanHash(engineJSON)
	if err != nil {
		t.Fatalf("PlanHash over real pipeline output: %v", err)
	}
	return engineJSON, hash
}

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

// Changing an entry asserts that plan's hash has changed.
var pinnedPlanHashes = map[string]string{
	"binary-cycle-step-plan.yaml":    "sha256:f1e32c655e1c783449d334963fe16015bf755f94b9b9ab35027bfeb02ea4dea2",
	"binary-plan.yaml":               "sha256:72401cb132657ec3abf0fdadd65b6b4420ff11b78d4d81e9f16ec98889b464bf",
	"board-plan.yaml":                "sha256:8e45ab4db67366496c8cd81c6367530ddb19859f2432389db1ac8245218c91f4",
	"full-unilevel.yaml":             "sha256:d0023bd7b766587821009ba61aa3443bee92492f97ec911340c342561c794965",
	"generation-plan.yaml":           "sha256:beea01a0b316344b0301b1af5556c2fe42192fae1d0a4d48c9db82c754c78d90",
	"hybrid-plan.yaml":               "sha256:e827d7b6edfedefb7e2b22b9b50cfc4a13ca4bbf1b8c821d6b956c9b551b65f5",
	"leg-cap-fraction-plan.yaml":     "sha256:81aa4f63ab25c44dbbff237e9452d1ecb145e64376b8661517267e3b7b3a1b60",
	"leg-quality-plan.yaml":          "sha256:d0195db01bc76b42112cb28449734729c16dca426915fbd0e4c2f76a184d78c6",
	"matrix-plan.yaml":               "sha256:71e7338fbb6664d6cdbc54b8bd3e2504398c4319f72c47bbfb74ace488844bc8",
	"minimal-unilevel.yaml":          "sha256:0f9bfec6de355ee8e0967dab30be9570f24a71343238c3cbaacc37cbaa05a0a5",
	"multi-tier-stairstep-plan.yaml": "sha256:b25c9d6689ac61037a2376fd8801f8c3e924831cefd999150a594644b5e95ca1",
	"stairstep-plan.yaml":            "sha256:eeaa6afda6667a4f59f5f6d493ab070d67ad40d05fa165913723aaf3f3d5bf58",
	"streamline-plan.yaml":           "sha256:14d94a31643fffc604aea5d3f328a55570acb18dde34e465edb177eeb80bf553",
	"tenure-rank-plan.yaml":          "sha256:1f37b28023b9703e164318ddf4547b8ade9ec1fda5b26d5ae27692ca037a8a0d",
	"windowed-rank-plan.yaml":        "sha256:6ecfdcc441ef76521d9887558532fee647b455a13b54e7fa3ccd50e7efc00809",
}

func TestPlanHashOfEachCommittedPlanIsPinned(t *testing.T) {
	root := filepath.Join("..", "..")
	pipeline, err := config.NewPipeline(filepath.Join(root, "schemas", "compensation-plan.schema.json"))
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	dir := filepath.Join(root, "internal", "config", "testdata", "valid")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var seen int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		seen++
		t.Run(e.Name(), func(t *testing.T) {
			yamlBytes, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			_, hash := hashPipelineOutput(t, pipeline, yamlBytes)
			want, ok := pinnedPlanHashes[e.Name()]
			if !ok {
				t.Fatalf("no pinned hash for %s; the pipeline produced %s", e.Name(), hash)
			}
			if hash != want {
				t.Fatalf("plan hash for %s: pinned %s, the pipeline produced %s", e.Name(), want, hash)
			}
		})
	}
	if seen != len(pinnedPlanHashes) {
		t.Fatalf("%d plan fixtures on disk, %d pinned", seen, len(pinnedPlanHashes))
	}
}
