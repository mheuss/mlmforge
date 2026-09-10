# `config_contract`

Fixtures for the integer-width contract. Both languages read them.

`width_manifest.json` lists every field the Go config layer tightens below its
wire type's natural range. For each one it carries an `engine_pointer` into a
fixture under `fixtures/`, and the Rust test sets that field to `over_max` and
asserts serde rejects it.

## The contract is deserializability, not validity

A fixture here promises it will **deserialize** into the engine
`CompensationPlan`. It does **not** promise that `load_plan` accepts it.

Those are different gates. Deserializing checks the shape and the types.
`load_plan` also runs semantic validation, and a plan can be structurally
perfect and semantically rejected.

**So do not reach for one of these as a ready-made plan without checking it
loads.** That has already cost one session its time.

## What loads today

Driven through the worker on 2026-09-10, one `load_plan` per fixture:

| Fixture | `load_plan` |
| --- | --- |
| `board.json` | loads |
| `generation.json` | loads |
| `matrix.json` | loads |
| `streamline.json` | loads |
| `unilevel.json` | loads |
| `stairstep.json` | loads |

A date, not a standing property. Re-run it rather than trusting this table.

`stairstep.json` used to be rejected here. HEU-699 gave `min_override` an
explicit unit, so the 10 it carries is now tagged as currency and the fixture
loads. The fixture is also still a valid width target: the manifest points
`StairstepCommission.CommissionableDepth` at it and that pointer resolves.

## These files are generated

`internal/config/genfixtures_test.go` writes this directory from the authoring
plans in `internal/config/testdata/valid/`, through the real Go pipeline, so a
fixture matches what the Rust worker actually receives.

**Edit the authoring YAML, never the JSON.**

```
REGEN_FIXTURES=1 go test ./internal/config/ -run TestGenerateConfigContractFixtures
```

`TestConfigContractFixturesMatchPipeline` byte-compares every committed fixture
against live pipeline output on an ordinary `go test`, so a hand edit fails
there rather than drifting quietly.

## Why this is a README and not a comment in the JSON

JSON has no comments, and the byte-comparison above would reject a field added
to carry one. The directory is the only place a note can live where someone
reaching for a fixture will see it before they use it.
