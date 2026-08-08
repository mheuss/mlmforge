# 028: Commission Config Comes From Validated State

## The Problem

The engine is the system of record for money. Decision 018 gives Go the referential and business-rule checks. HEU-517 added a second gate in Rust, because the engine does not trust that the Go pipeline ran. That gate lives in `handle_load_plan`. It validates a `CompensationPlan` and stores it in `WorkerState`.

A gate only works if every money path goes through it. Commission handlers need two things to calculate: the plan, for the rank ladder, and one structure's config, for the rates. Where should those come from?

When this decision was made, five of the seven handlers read both from `WorkerState` and two took them from the request. That difference was not a decision. It was an accident, and it produced HEU-583, where a `calculate_streamline` request carrying `percent: 5.0` computed a 500% rate and the engine paid it. Streamline has since moved; board plan is the one that has not.

## The Decision

Commission handlers read the plan and the structure config from `WorkerState`. Never from request params.

A handler resolves its structure by name from the loaded plan. Request params carry only per-calculation data, such as the structure name, the distributor snapshots, and the volume. Binary pairing also takes `carry_forward` and `ownership`, neither of which is policy config.

### Current exception: board plan

`handle_board_calculate_commissions` does not follow this rule yet. It takes `_state`, calls `require_plan` zero times, and reads `config: BoardPlanConfig` straight from params. `BoardPlanConfig::validate` guards `cycle_commission` against negative and non-finite values under the board dollar law, and it never runs on that path. Non-finite is the more dangerous of the two: NaN propagates silently through sums and makes every comparison false, so it corrupts a total without an obvious sign.

This is the same defect as HEU-583, not a deliberate carve-out. HEU-603 tracks it. This document states the target, and board plan is the one handler that has not reached it.

## The Reasoning

**Validation happens once, at the boundary.** `handle_load_plan` is the only door the *compensation plan* walks through, so `WorkerState.plan` is valid by construction. A commission handler does not re-validate it and does not need to know how validation works.

Tree-level engine config is a separate door and is not yet closed. `handle_create_board_plan` takes `config: BoardPlanConfig` from request params, and `BoardPlanEngine::new` checks only the width and height bounds before storing it verbatim — `BoardPlanConfig::validate` never runs on that path. That is HEU-607. `handle_create_streamline` likewise builds its `StreamlineConfig` from request params, never checked against the plan's `stream_config` (the `streams` block on the wire — `#[serde(rename = "streams")]`); that is HEU-598, open question 1. Both land in `WorkerState.trees`.

So "valid by construction" is a claim about the plan, not about all of `WorkerState`. Note that HEU-603 is a different gap — the board *calculate* path, not the create path — and closing it leaves this door open.

**Config on the request creates a second door.** Adding `plan.validate()` to the streamline handler would not have fixed HEU-583. `validate` walks `plan.structures`. The request carried a separate `structure_config` field that fed the calculator directly. Two sources of the same config can disagree, and the one that drove the math was the one nothing checked.

**The type system cannot help while config rides the request.** `StreamlineCommissionConfig::validate` is private to `network-engine`. The worker is a different crate. Validating a bare structure config from a handler would have required widening that API, which adds a second way to do the same thing.

**Named lookup removes a whole error class.** When a handler resolves its structure by name from the plan, a mismatch between the requested structure and the supplied config cannot be expressed. The streamline handler previously carried an explicit equality check for exactly that case. Named lookup made the check unnecessary.

**Per-calculation data is different.** Snapshots and volume change every call and describe a period, not a policy. They belong on the request. Config describes policy and belongs in state.

## Revisit Trigger

This rule is a call-site convention. Nothing in the type system enforces it. A new handler can still take config from the request, and nothing will fail until someone sends a bad value.

HEU-597 makes the rule structural by pushing a `ValidatedPlan` newtype through all seven calculators. When that lands, "validated" stops being something a reviewer has to notice and starts being something the compiler checks. Revisit this document then and record that the convention became a type.

A second trigger: if a genuine what-if or preview use case appears, where a caller wants to calculate against a hypothetical plan without changing worker state. That is a real need this rule does not serve. It should be met with an explicit preview operation that validates its input, not by relaxing the rule for one handler.

## What This Means

- Six of the seven commission handlers call `require_plan` before reading params. Board plan is the outstanding exception above.
- Each of those six has a `find_*_structure` helper that resolves its structure by name from the loaded plan.
- Missing plan returns `NO_PLAN`. Missing structure returns `STRUCTURE_NOT_FOUND`. Both predate this decision and are already in decision 019.
- Callers call `load_plan` before any `calculate_*` operation, board plan excepted until its ticket lands.
- No `calculate_*` request carries a plan or a structure config, board plan excepted until its ticket lands.
- Go has no per-structure config DTOs. The plan travels once, as raw JSON, through `LoadPlan`.

`docs/development/config-types.md` lists three bypass paths where a money-seam invariant must be guarded. This rule closes the third one, where a worker handler deserializes config from request params. That file tracks the seam and names the one handler still on the wrong side of it.
