# 015: Compensation Plan Schema and Wire Format

## The Problem

Decision 008 through 014 define what compensation plan configuration looks like. The config pipeline from DEVELOPMENT.md (ADR-012) establishes three stages: YAML input, Go validation and translation, typed Rust structs for the engine.

That pipeline left open the specifics. What technology validates the YAML? How do field names stay coordinated between YAML and Rust when the Rust types use different names internally? Where does the boundary sit between structural validation and business-rule validation? How much translation work does Go actually do?

These details matter because they determine how much code the Go layer contains, how IDE tooling works for plan authors, and how validation errors surface.

## Decisions

### One Wire Format

The YAML field names from the compensation plan design documents (008-014) are the canonical wire format. Every system component uses these names.

YAML files use the canonical names directly. Rust config types use `#[serde(rename = "wire_name")]` so deserialization accepts the canonical names. Internal Rust field names may differ for readability. Go passes parsed YAML through to Rust with zero field name translation.

Example: the Rust field `minimum_pv` accepts the wire name `min_personal_volume`. The serde rename is invisible to engine logic. Plan authors write `min_personal_volume` in YAML. Go passes it through. Rust reads it as `minimum_pv`.

This eliminates 28 field name mappings that would otherwise live in the Go translation layer. The full list of renames is tracked in the Rust config types via serde attributes.

### JSON Schema (Draft 2020-12)

A single JSON Schema file (`schemas/compensation-plan.schema.json`) validates compensation plan YAML. The schema uses Draft 2020-12 for `if/then/else` support and broad tooling compatibility.

**Monolithic file with `$defs`.** All 63 types live in one file. No multi-file `$ref`. Simpler to distribute, load, and version.

**Structure type discriminator.** `StructureConfig` uses `allOf` with `if/then` blocks keyed on the `type` field. Each structure type selects its required fields and commission shape. The same pattern applies to `BinaryCommission` (keyed on `mode`: pairing vs cycle/step).

**Descriptions on every property.** Each field has a 1-3 sentence description so plan authors get inline help without opening documentation.

**`additionalProperties: false` off by default.** Extra fields are silently accepted almost everywhere. This favors forward compatibility over typo detection. Three board-plan definitions opt in and are closed: `BoardCyclingConfig`, `BoardPlanStructureParams`, and `BoardPlanCommission`. They are the "selectively later" case this decision left room for.

**Nullable fields use `oneOf`.** Fields that accept null in YAML use `"oneOf": [{"$ref": "..."}, {"type": "null"}]`.

The schema lives in a top-level `schemas/` directory. It is a shared artifact consumed by Go, the admin UI, and IDE tooling. It does not belong in the Go source tree or the Rust crate.

YAML files reference the schema for IDE autocomplete:

```yaml
# yaml-language-server: $schema=../../schemas/compensation-plan.schema.json
```

### Validation Boundary

The schema and Go have distinct responsibilities. The schema validates structure. Go validates business rules.

**Schema validates:**

| Check | Example |
|-------|---------|
| Required fields | `name`, `version`, `period` must exist. `start_date` required within `period`. |
| Types | `payout_lag_days` is integer, `broad_commission_percent` is number |
| Enums | `length` must be one of `week`, `semi_month`, `month`, `quarter` |
| Numeric ranges | `payout_lag_days`: 0-60, percentages: 0.0-1.0 |
| String patterns | `base_currency`: 3 uppercase letters |
| Array constraints | `ranks` minItems 1, `structures` minItems 1 |
| Conditional required fields | `type: matrix` requires `structure` block, `mode: pairing` requires `pairing` block |
| Const values | `version: 1` |

**Go validates:**

| Check | Example |
|-------|---------|
| Cross-field dependencies | `max_group_volume_per_leg <= group_volume` |
| Cross-section references | `qualified_structures` references defined structure names |
| Cross-section dependencies | `pay_once_only` requires `track_achieved_rank: true` |
| Referential integrity | `min_rank` references a rank with lower ordinal |
| Ordering constraints | Ranks sorted by ordinal, tiers sorted by `min_active_legs` |
| External lookups | Product IDs reference valid catalog products |
| Semantic warnings | `payout_lag_days > 30`, `matrix width^height > 1M` |
| Rate table completeness | Every defined rank should have a rate table entry |

The schema catches structural mistakes immediately. Wrong type, missing field, bad enum value. Go catches logical mistakes that require understanding relationships between sections.

### Structural Translations (Go)

Five structural differences between the YAML wire format and the Rust types require Go translation. These are shape changes, not field renames.

| Difference | YAML format | Rust format | Go action |
|------------|-------------|-------------|-----------|
| Structure tagging | Flat: `type` field is a sibling of other fields | Adjacent tagged: `type` + `config` wrapper | Wrap sibling fields into a `config` object |
| Donated placement | Two fields: `donated_placement_enabled` + `donated_placement_restriction` | One field: `Option<DonatedPlacementRestriction>` | Collapse boolean + enum to single optional |
| Streamline levels | Map: `1: {min_rank, percent}` | `Vec<StreamlineLevel>` | Convert map entries to ordered vector |
| Binary mode | Flat: `mode` string + sibling config object (`pairing`, `cycle`, `step`) | Externally tagged: `mode: {pairing: {...}}` | Nest the active config under the mode key |
| Binary placement key | `placement.binary` | `placement.binary_placement` | Rename key to avoid collision with structure type name |

### start_date Handling

The JSON Schema requires `start_date` in `PeriodConfig`. The Rust `PeriodConfig` deserializes it as a required `NaiveDate`. The Go `PeriodConfig` stores it as `*string` (nullable pointer) because Go handles schema validation before the value reaches Rust. If the schema passes, `start_date` is always present by the time translation runs. The nullable Go type is a concession to Go's lack of a non-zero-value string type, not a signal that the field is optional.

The YAML format favors human readability. Flat structures, maps with numeric keys. The Rust format favors type safety. Tagged enums, Option types, vectors. Go bridges the gap.

## What We Considered

**Rust-native wire format.** Let serde's default serialization define the YAML shape. Simpler for developers but produces awkward YAML for plan authors. Adjacent-tagged enums, nested wrappers, and vector syntax are not intuitive for non-technical people configuring compensation plans. The plan author experience is more important than developer convenience.

**Per-field Go mapping.** Go translates every field name between YAML and Rust. This was the default assumption before we counted the renames. With 28 fields that differ, the translation layer would be a significant source of bugs and maintenance burden. Serde rename attributes on the Rust side are simpler, verified by compilation, and require zero Go code.

**Multi-file schema with `$ref`.** Split the schema into one file per config area. Better organization on paper but adds complexity to schema loading, distribution, and versioning. A single file with internal `$defs` is simpler. At roughly 2,500 lines the file is large but manageable.

**`additionalProperties: false`.** Catches typos immediately. But it also breaks forward compatibility. Adding a new field to the schema would invalidate existing YAML files until they upgrade. The open-by-default approach lets old schemas validate new files with extra fields. Go-level validation can add specific typo detection later.

**OpenAPI or TypeSpec.** Both can generate JSON Schema but add a generation step and constrain the schema to what the generator supports. Direct JSON Schema gives full control over Draft 2020-12 features like `if/then/else` for the structure discriminator.

## What This Enables

- Plan authors get IDE autocomplete and inline validation. The YAML language server reads the schema and provides immediate feedback without running the application.
- Adding a new config field requires updating the Rust struct (with serde rename if names differ) and the JSON Schema. Go passes it through with no mapping code unless the field has a structural difference.
- The Go validation pipeline has a clean first stage. Schema validation catches structural errors. Go validation focuses on business rules.
- Admin UI form generation can consume the same schema. Field types, descriptions, enums, and ranges are all present.
- The engine stays pure. It receives a typed Rust struct and produces commission results. It never parses YAML, reads from the database, or handles raw JSON.
