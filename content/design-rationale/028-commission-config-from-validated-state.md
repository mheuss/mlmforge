# 028: Commission Config Comes From Validated State

## The Problem

The engine is the system of record for money. Decision 018 gives Go the referential and business-rule checks. HEU-517 added a second gate in Rust, because the engine does not trust that the Go pipeline ran. That gate lives in `handle_load_plan`. It validates a `CompensationPlan` and stores it in `WorkerState`.

A gate only works if every money path goes through it. Commission handlers need two things to calculate: the plan, for the rank ladder, and one structure's config, for the rates. Where should those come from?

When this decision was made, five of the seven handlers read both from `WorkerState` and two took them from the request. That difference was not a decision. It was an accident, and it produced HEU-583, where a `calculate_streamline` request carrying `percent: 5.0` computed a 500% rate and the engine paid it. Both have since moved: streamline under HEU-583, board plan under HEU-603.

## The Decision

Commission handlers read the plan and the structure config from `WorkerState`. Never from request params.

A handler resolves its structure by name from the loaded plan. Request params carry only per-calculation data, such as the structure name, the distributor snapshots, and the volume. Binary pairing also takes `carry_forward` and `ownership`, neither of which is policy config.

## The Reasoning

**Validation happens once, at the boundary.** `handle_load_plan` is the only door the *compensation plan* walks through, so `WorkerState.plan` is valid by construction. A commission handler does not re-validate it and does not need to know how validation works.

"Valid" means whatever the validators check, no more. Making them the sole point of trust raises the cost of a hole in one. HEU-612 was the worked example. `StreamlineCommissionConfig::validate` left `commissionable_depth` unbounded above and did not check `dynamic_compression` for ordering, gaps, or duplicates. A hand-rolled plan could defeat the depth limit and pay one level's rate against another level's rank threshold. Both holes are closed, but not both at this boundary: the table checks live in the validator, while the depth limit is enforced one layer in, by the walk counting levels in `u16`. Concentrating trust at the boundary is still the right shape — it just means the boundary has to be worth trusting.

Tree-level engine config is a separate door and is not yet closed. `handle_create_board_plan` takes `config: BoardPlanConfig` from request params, and `BoardPlanEngine::new` checks only the width and height bounds before storing it verbatim — `BoardPlanConfig::validate` never runs on that path. That is HEU-607. `handle_create_streamline` likewise builds its `StreamlineConfig` from request params, never checked against the plan's `stream_config` (the `streams` block on the wire — `#[serde(rename = "streams")]`); that is HEU-598, open question 1. Both land in `WorkerState.trees`.

So "valid by construction" is a claim about the plan, not about all of `WorkerState`. Note that HEU-603 is a different gap — the board *calculate* path, not the create path — and closing it leaves this door open.

**Config on the request creates a second door.** Adding `plan.validate()` to the streamline handler would not have fixed HEU-583. `validate` walks `plan.structures`. The request carried a separate `structure_config` field that fed the calculator directly. Two sources of the same config can disagree, and the one that drove the math was the one nothing checked.

**The type system cannot help while config rides the request.** `StreamlineCommissionConfig::validate` is private to `network-engine`. The worker is a different crate. Validating a bare structure config from a handler would have required widening that API, which adds a second way to do the same thing.

**Named lookup removes a whole error class.** When a handler resolves its structure by name from the plan, a mismatch between the requested structure and the supplied config cannot be expressed. The streamline handler previously carried an explicit equality check for exactly that case. Named lookup made the check unnecessary.

The class it removes is config-versus-structure mismatch. It does not cover data-versus-structure mismatch. Board cycle events carry no structure or plan identity, so a caller can still submit events produced under one board structure while naming another, and get the second one's rates applied to the first one's events. HEU-614 tracks that.

**The guard the gate restores is narrower than it sounds.** `BoardPlanConfig::validate` checks one thing: `cycle_commission` is finite and non-negative. Only the negative half is reachable over the wire, because JSON has no NaN literal and serde_json rejects float overflow. The non-finite half covers configs built in Rust, not requests.

**Per-calculation data is different.** Snapshots and volume change every call and describe a period, not a policy. They belong on the request. Config describes policy and belongs in state.

This rule is about config. It is not a claim that request params cannot move money — they still can, and today nothing validates them. An asserted `snapshot.rank` clears dynamic-compression thresholds (HEU-608); omitting an upline's snapshot promotes every ancestor above it a level (HEU-609); a repeated volume `source_id` pays twice (HEU-610). Only CV *values* are checked, by `validate_cv`. Sourcing config from state closes one door, not the room.

## Revisit Trigger

This rule is a call-site convention. Nothing in the type system enforces it. A new handler can still take config from the request, and nothing will fail until someone sends a bad value.

HEU-597 makes the rule structural by pushing a `ValidatedPlan` newtype through all seven calculators. When that lands, "validated" stops being something a reviewer has to notice and starts being something the compiler checks. Revisit this document then and record that the convention became a type.

A second trigger: if a genuine what-if or preview use case appears, where a caller wants to calculate against a hypothetical plan without changing worker state. That is a real need this rule does not serve. It should be met with an explicit preview operation that validates its input, not by relaxing the rule for one handler.

## What This Means

- All seven commission handlers call `require_plan` before reading params.
- Each has a `find_*_structure` helper that resolves its structure by name from the loaded plan.
- Missing plan returns `NO_PLAN`. Missing structure returns `STRUCTURE_NOT_FOUND`. Both predate this decision and are already in decision 019.
- Callers call `load_plan` before any `calculate_*` operation.
- No `calculate_*` request carries a plan or a structure config.
- Go has no per-structure config DTOs. The plan travels once, as raw JSON, through `LoadPlan`.

`docs/development/config-types.md` lists three bypass paths where a money-seam invariant must be guarded. This rule closes the third one, where a worker handler deserializes config from request params. That file tracks the seam. Both calculate handlers that were on the wrong side of it have moved; the one create door it names is still open.
