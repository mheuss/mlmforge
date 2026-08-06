# Config Types: Go and Rust Alignment

The compensation plan config rides on two type systems. Plans are authored in YAML, validated against a JSON schema, decoded into Go structs (`internal/config/`), and round-tripped to Rust as JSON for the commission engine (`engine/network-engine/src/config/`).

For every config field, three layers must agree on what's valid:

1. **JSON schema** (`schemas/compensation-plan.schema.json`) — the contract for plan authors. First validation gate.
2. **Go field type** (`internal/config/types.go`) — second gate, runs at YAML unmarshal.
3. **Rust field type** (`engine/network-engine/src/config/`) — final gate, runs at JSON deserialize via serde.

Drift between layers causes silent failures. The patterns below avoid them.

## Field types must match byte-widths

A Go field that mirrors a Rust `u8` should be `uint8`, not `int`. A Go `int` will accept `300` at YAML unmarshal, then either truncate or fail when round-tripped to Rust's `u8`. Catch the error at the Go layer where the user gets a clear unmarshal error, not at the Rust FFI boundary where the error is opaque.

The same rule applies to `u16` ↔ `uint16`, `u32` ↔ `uint32`, etc. If you find yourself typing `int` to mirror an unsigned Rust type, stop.

Recent precedents:
- `GenerationCommissionConfig.MaxGenerations`: `int` → `uint8` (HEU-425)
- `BreakawayGenerationConfig.MaxGenerations`: `int` → `uint8` (HEU-425)

## Map types: declare the value type explicitly

For maps that round-trip to Rust `BTreeMap<K, V>`:
- Go: `map[string]uint8` (or matching value type), not `map[string]int` or `map[string]any`
- JSON schema: declare `additionalProperties: {type: integer, minimum: ..., maximum: ...}` on the value type
- Use `omitempty` on the JSON/YAML tags **when the Rust mirror has `#[serde(default)]`** so an empty map round-trips cleanly. When it does not (e.g. `additional_per_rank` / Rust `additional_streams`), keep the field required and present instead — see "Fail-loud validation on bypass paths" below.

Empty maps and absent fields must produce identical results at every layer. Add a regression test for this when the field is added — three configurations (explicit empty, default-constructed, populated-with-no-op-values) should produce identical engine output. A test that compares only empty maps against each other proves nothing.

## Mirroring a tagged Rust enum as a Go flat struct

When a Rust config type is an internally-tagged enum (`#[serde(tag = "type")]`), the Go mirror is a flat struct. It has a `Type` discriminator field plus every variant's fields side by side.

Do not put `omitempty` on the variant fields. Go marshals the non-selected variant's field as a zero value. Rust serde ignores that extra field when it deserializes the tagged enum. `omitempty` would instead drop a legitimate zero value, such as a real `min_personal_volume: 0`. The Rust field has no `serde(default)` to recover it.

The JSON schema discriminates the variants with `oneOf`. Each branch pins the discriminator with `const`. Each variant's own field carries a constraint strict enough that a zero-valued non-selected field cannot satisfy the wrong branch. For example, `min_rank` carries `minLength: 1`. The empty-string `min_rank` that Go emits on a `contains_personal_volume` predicate then fails the `contains_rank` branch, so `oneOf` stays unambiguous.

Test the wire shape from both sides. A Go test should assert the marshaled JSON carries the zero-valued non-selected field. A Rust test should deserialize that exact Go-emitted payload. Neither half can drift silently.

The Go-side assertion must pair `assert.Contains(t, m, key)` with `assert.Nil(t, m[key])`. `assert.Nil(t, m[missing])` on a `map[string]any` passes whether the key is JSON `null` OR absent, so `Nil` alone does not catch a future `omitempty` regression that drops the key. The two assertions together pin both "key present" and "value is null."

```go
// Catches both shape drift modes:
//   - value changes from null to a real value: Nil fails
//   - omitempty regression drops the key:      Contains fails
assert.Contains(t, m, "differential")
assert.Nil(t, m["differential"])
```

Reference: `LegPredicate` (HEU-444), `OverrideStrategy` (HEU-428).

## Schema additions are mandatory for new fields

When you add a field to a Go config struct that gets serialized:

1. Add the field to `schemas/compensation-plan.schema.json` under the appropriate `$defs` entry
2. Add a fixture exercising it to `internal/config/testdata/valid/`
3. Register the fixture in the hardcoded lists. `TestSchemaValidatesAllValidFixtures` (`schema_test.go`) and `TestPipelineAllValidFixtures` (`pipeline_test.go`) both iterate explicit slices. Fixtures are not auto-discovered.

The schema is what tells plan authors when they typo a field name. Missing the schema declaration means typos produce empty/default values silently. The Go and Rust layers won't catch them either — neither uses `additionalProperties: false` today.

### Invalid fixtures: mutate a valid one

The steps above cover valid fixtures. Invalid ones work differently. They are registered in no list, and each gets its own named test.

Build them by mutating a valid fixture rather than hand-writing a standalone file:

```go
base := readFixture(t, "valid/streamline-plan.yaml")
require.Empty(t, p.validateSchema(base), "base fixture should validate cleanly")

over := replaceInYAML(t, base, level1Valid, level1+"5.0")
```

The `require.Empty` line is the point. It re-proves on every CI run that the base is valid apart from the mutation, so the test cannot pass for an unrelated reason. A hand-written invalid fixture can only assert that once, by hand, at authoring time. When the schema later tightens, that file can start failing for a second reason. The test stays green while the constraint it was written to pin is gone.

`replaceInYAML` (`testhelpers_test.go`) replaces the first match and fails the test if the anchor is missing. Anchor on enough lines to be unique. A bare `percent: 0.10` also matches `broad_commission_percent` if that field ever takes the same value.

Worked examples: `TestSchemaRejectsStreamlineLevelOverU8`, `TestSchemaRejectsOverMaxBounds`, `TestSchemaRejectsStreamlinePercentOutOfRange`.

## Boundary tests for narrow types

For any field with a constrained range (`uint8`, `uint16`, etc.), add a table-driven boundary test:

| Case | Value | Expectation |
|------|-------|-------------|
| zero | 0 | Pass or fail (decide the contract — see HEU-442) |
| max | 255 (for u8) | Pass |
| one above max | 256 | Fail at unmarshal |
| large overflow | 300 | Fail at unmarshal |
| negative | -1 | Fail at unmarshal |

The boundary test enforces the type narrowing. A future refactor that "helpfully" wraps the field in a custom unmarshaler that clamps would break the contract; this test catches it.

## Validation lives in `rules.go`

Cross-field invariants (e.g., "every key in `MaxGenerationsPerRank` must reference a defined rank") belong in `internal/config/rules.go`, not in the schema. The schema validates structure and value ranges; `rules.go` validates relationships between fields.

Pattern to follow: `validateStructureRefs` for reference checks. Look at `Breakaway.Overrides.Differential.RankRates` validation as a template — it uses `ValidationError` with code `undefined_reference` and a JSON-pointer-style path like `/structures/{i}/commission/breakaway/overrides/differential/rank_rates`.

## Cross-layer enforcement of byte-width caps

A field whose *value* fits a narrow Rust type (`u8`, `u16`) is handled by the byte-width and boundary-test patterns above. A field whose *collection length* fits a narrow type is different. The schema enforces structure, Go validates relationships, and Rust often does an inner-loop `try_from` that panics if the cap is exceeded. All three layers must agree.

Concrete case from HEU-428: `walk_multi_tier_overrides` derives each tier's 1-based depth floor as a `u8` (`u8::try_from(tier_index + 1)`). A 256th tier panics the calculator. The fix is to enforce the cap consistently at every layer above:

1. **Schema** (`schemas/compensation-plan.schema.json`): add `maxItems: 255` (or `maximum: <upper>` for scalar fields) so plan authors get a clear schema_violation at YAML load.
2. **Go validator** (`internal/config/rules.go`): mirror the cap as a `len(slice) > 255` check, with the same `invalid_value` code as other structural checks. The schema is the first gate but `rules.go` is what runs in tests that bypass schema validation.
3. **Rust engine**: keep the `try_from` as the inner-loop guard. The `.expect(...)` message can now truthfully name the upstream guards.

Pair the cap with two boundary tests at the Go layer:
- The inclusive max (e.g., 255 tiers): must be accepted with zero validation errors.
- One above the max (e.g., 256 tiers): must produce exactly one `invalid_value` error at the slice's path.

Without the matching Go check, the schema alone is the gate. Test fixtures that bypass schema validation (`validateBusinessRules` direct calls) can construct 256+ entries and trip the Rust panic at calculate time. The Go check prevents this.

### Two-sided ranges need four boundary points, at every layer

Two boundary tests are right for a one-sided cap. A two-sided range needs four: over the max, under the min, and both inclusive endpoints.

`StreamlineLevel.percent` is the worked example (HEU-584). It is a fraction in `[0, 1]`, gated by `minimum`/`maximum` in the schema and by `level.Percent < 0 || level.Percent > 1` in `rules.go`. Cover only the out-of-range cases and the comparison stays free to tighten to `<= 0 || >= 1` with the whole suite green. That silently rejects `percent: 0` (no commission at this level) and `percent: 1.0` (full payout), both of which the schema still accepts. Legal config gets refused, and the two Go gates disagree with nothing to catch it.

Pin the endpoints at each layer separately. Endpoint coverage at the schema layer does not constrain `rules.go`, and the reverse holds too. Splitting the out-of-range cases into one test per branch matters for the same reason: a single "out of range" test leaves the other half of the comparison deletable.

## Automated drift guards (HEU-513)

The patterns above are the discipline; four test-time guards enforce them in CI so the contract cannot silently drift again.

- **Width manifest** (`engine/testdata/config_contract/width_manifest.json`): a single source of truth listing each width-constrained field by JSON pointer and its expected `uintN` width. `width_contract_test.go` drives per-field boundary assertions from it, and the manifest doubles as the human-readable index of the whole contract surface.

- **AST completeness scan** (`TestConfigContract_NoUntypedIntFields`, `width_contract_test.go`): parses the `internal/config` package AST and fails if any struct field is, or wraps, a signed integer (`int`, `int8`…`int64`) — including behind a pointer, slice, map key, map value, or inline struct. A small allow-list names the three genuinely-signed fields. This is what turns "a new field typed `int`" into a red build instead of latent drift. Being AST-only, it does not see a signed int behind a named type (`type Depth int32`) or an embedded field; config wire types use neither today (a `go/types`-based scan would be needed if that changes).

- **Golden fixture guard** (`TestConfigContractFixturesMatchPipeline`, `genfixtures_test.go`): byte-compares each committed `engine/testdata/config_contract/fixtures/*.json` against live `translateToEngine` output, locking the wire shape and key order (`taggedStructure` emits `type` before `config` in each structure — a `map[string]any` would emit them alphabetically, which is the ordering that struct exists to avoid; see the comment on it in `translate.go`). The Rust side (`config_width_contract.rs`) loads those same fixtures and asserts they deserialize and reject over-max values, so a `translate.go` change that desyncs the fixtures fails on the Go side before the Rust side reads a stale file. Regenerate intentionally with `REGEN_FIXTURES=1 go test ./internal/config/ -run TestGenerateConfigContractFixtures` (the guard requires the value to be exactly `1`).

- **Rate-map key-width guard**: level-keyed rate maps are Go `map[string]float64` but Rust `BTreeMap<u8, f64>`, so the *key* carries a byte-width too. `validateRateKeyWidths` / `validateU8MapKeys` (`rules.go`) reject a key outside `[0, 255]` or non-numeric, and the schema mirrors the bound with a `propertyNames` pattern. Streamline level keys are additionally 1-based (level 0 rejected), narrower than the full `0–255` range the width-only rate maps allow. The AST scan above also recurses into map *keys*, so a narrow-key map typed with a signed key is caught too.

## Fail-loud validation on bypass paths (HEU-513)

The three-layer model has three bypass paths, and a money-seam invariant must be guarded on all of them:

- A caller that builds a plan programmatically or hand-writes a fixture reaches `validateBusinessRules` **without the schema** — so `rules.go` must own the invariant, not just the schema.
- A caller that feeds JSON straight to the engine bypasses Go entirely — so the Rust `*::validate` impls (`engine/network-engine/src/config/validate.rs`) must own it too.
- A request-scoped worker handler that deserializes its own plan or structure config from request params bypasses even `CompensationPlan::validate`. The HEU-517 gate runs only in `handle_load_plan`. Example: `handle_calculate_streamline` (`engine/network-engine-worker/src/handlers/streamline.rs`) takes `plan` and `structure_config` per request with no validation. HEU-583 tracks the fix. Until then, treat request-scoped params as unvalidated input when adding or changing handlers.

Guard both. Examples on this seam:

- `max_generations >= 1` (a 0 excludes every earner): guarded in `rules.go` **and** Rust `GenerationCommissionConfig::validate` for the main generation config, and again for the stairstep `BreakawayGenerationConfig` single-walk override — the sibling is easy to miss.
- Matrix `spillover_direction`: the schema and Rust `MatrixStructureParams::validate` accept only `breadth_first`; `rules.go` rejects any other non-empty value so the Go bypass fails loud at load rather than opaquely at the engine.
- `start_date` required on the period (Go `validatePeriod`), because Rust's `NaiveDate` has no default.

The guiding rule is *fail loud*: a malformed value should earn a clear, coded rejection, not a silent default. `StreamConfig.AdditionalPerRank` is the deliberate counter-example to the general "use `omitempty` + `serde(default)`" map rule — its Rust mirror `additional_streams` intentionally has NO `serde(default)`, so the Go field keeps NO `omitempty` and the schema *requires* it. A missing value then earns a loud Rust rejection instead of a silent empty default. `TestAdditionalPerRank_EmptyStaysPresent` pins this against a future `omitempty` "fix" (HEU-513 Task 8A).

## Testing an optional `oneOf [$ref, null]` object

An optional config object declared as `"oneOf": [{ "$ref": "#/$defs/X" }, { "type": "null" }]` — the shape used for `window` and `tenure` on `RankQualification` — emits TWO `schema_violation` leaves when a required sub-field is missing:

1. The missing-property error on the `$ref` branch.
2. An "expected null, got object" error from the failed `null` branch.

Both leaves carry the same `Code` (`schema_violation`) and the same `Path` (the gate object). Only the `Message` distinguishes them — the real one names the missing field.

So a missing-required-field test must assert on the message, not the count or the path:

```go
// Wrong: len(errs) == 1 fails — the oneOf emits two leaves.
// Wrong: asserting Code or Path alone matches both leaves.
// Right: pin the real leaf by the field name in its message.
require.Contains(t, err.Error(), "threshold_rank")
```

Discovered in HEU-446 (windowed/tenure gates) and applied to the gate rejection tests.
