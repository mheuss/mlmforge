# 018: Config Pipeline

## The Problem

Decision 015 defines what the compensation plan schema looks like and where the validation boundary sits between JSON Schema and Go. It left open the mechanics of the Go-side pipeline. How does YAML become Rust-ready JSON? How do six different commission types get parsed when the YAML structure is flat? How do errors and warnings coexist without blocking valid plans?

These decisions affect how plan authors experience validation errors, how new compensation plan types are added, and how much code changes when the schema evolves.

## Decisions

### Five-Stage Pipeline

The `Pipeline` struct in `internal/config/` runs five stages in order. Each stage has a clear input, output, and failure mode.

| Stage | What it does | Failure behavior |
|-------|-------------|-----------------|
| 1. Schema validation | Validates raw YAML against JSON Schema Draft 2020-12 | Blocks all subsequent stages |
| 2. YAML unmarshal | Parses YAML into Go structs (`CompensationPlan`) | Returns infrastructure error |
| 3. Commission resolution | Second-pass unmarshal of per-structure commission blocks | Returns infrastructure error |
| 4. Business-rule validation | Cross-field, referential integrity, semantic warnings | Errors block stage 5. Warnings do not. |
| 5. Structural translation | Converts YAML-shape Go structs to Rust-shape JSON | Returns infrastructure error |

Schema errors (stage 1) block everything. A malformed document cannot be meaningfully unmarshaled. Business-rule errors (stage 4) block translation but allow warnings through. Warnings like "payout_lag_days > 30" do not prevent a valid plan from reaching the engine.

### Two-Pass Commission Parsing

Compensation plan YAML is flat. A structure's `commission` block sits at the same level as `name` and `type`. The shape of the commission block varies by structure type: unilevel has `rate_table` and `compression`, binary has `mode` and `pairing`, streamline has `dynamic_compression` and `streams`.

Go cannot unmarshal a type-discriminated block in one pass. The initial YAML unmarshal stores the commission block as `any` in `CommissionRaw`. Stage 3 (`resolveCommissions`) reads the structure's `Type` field, re-marshals `CommissionRaw` to YAML bytes, and decodes into the correct typed struct (`UnilevelCommission`, `BinaryCommission`, etc.).

This two-pass approach keeps the YAML format flat for plan authors while giving Go fully typed commission configs for validation and translation. The alternative was a manually written YAML parser or a `map[string]any` that would need type assertions everywhere.

### Commission Marker Interface

The `Commission` interface has a single unexported method: `isCommission()`. All six commission types implement it. `StructureConfig.resolvedCommission` is typed as `Commission` instead of `any`.

This provides compile-time safety. Adding a new commission type requires implementing the marker method. Translation code that type-asserts on `resolvedCommission` gets a meaningful type rather than `any`. The unexported method prevents external packages from satisfying the interface accidentally.

### Validation Severity

`ValidationError` has a `Severity` field: `"error"` or `"warning"`. The `hasErrors()` function checks whether any error in a slice has severity `"error"`. Warnings pass through to the caller but do not block translation.

This matters because some validation checks are advisory. A matrix structure with `width^height > 1,000,000` positions is probably wrong but not definitively invalid. A `search_mode` of `first_levels` without a `search_depth` is suspicious but might be intentional. These produce warnings. Missing required references and type mismatches produce errors.

The caller receives the full list. It can display warnings to plan authors, log them, or ignore them. The pipeline does not make that decision.

### Union Types

`DemotionPolicy` is a YAML union: either the string `"promotion_only"` or an object with a `grace` field. Go handles this with custom `UnmarshalYAML` (checks `yaml.ScalarNode` vs object) and custom `MarshalJSON` (produces the Rust-compatible format). String-value validation is delegated to the JSON Schema enum constraint in stage 1.

This pattern applies wherever the YAML format uses a string-or-object union. The Go type carries both variants. Custom marshal/unmarshal bridges YAML's flexibility with Go's static typing.

## What We Considered

**Single-pass parsing with custom unmarshaler.** Write a custom YAML unmarshaler for `StructureConfig` that inspects the `type` field and decodes the commission block in one pass. This requires hand-written YAML decoding logic that duplicates what `yaml.Unmarshal` already does. The two-pass approach reuses standard unmarshaling.

**`any` for resolved commissions.** Simpler but loses type safety. Translation code would need `map[string]any` type assertions. Adding a new commission type would compile without implementing the required methods.

**Separate error and warning return values.** Two slices instead of a severity field. This forces callers to handle two lists. A single slice with severity is simpler and extensible (adding a third severity level later requires no signature changes).

**Validation in the Rust engine.** Let Go pass through raw JSON and let Rust validate business rules. This pushes Go-domain logic (rank name references, structure name references, cross-section dependencies) into Rust, where the Go types are not available. Go has the full parsed plan in memory. It is the right place for referential integrity checks.

## What This Enables

- **Clean error reporting.** Plan authors see schema errors first. If the document is structurally valid, they see business-rule errors and warnings. No noise from downstream stages when the YAML is malformed.
- **Type-safe translation.** Each structure type's translation function receives a concrete commission type. No runtime type guessing.
- **Incremental schema evolution.** Adding a new field: update the JSON Schema, add to the Go struct, add a serde rename on the Rust side. If the field needs business-rule validation, add a check in `rules.go`. If it needs structural translation, add a case in `translate.go`. Most fields need neither.
- **Testable stages.** Each stage can be tested in isolation. Schema validation tests use raw YAML bytes. Business-rule tests use constructed `CompensationPlan` structs. Translation tests verify JSON output shape.
