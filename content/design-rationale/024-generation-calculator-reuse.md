# 024: Generation Calculator Reuse

## The Problem

The standalone generation commission calculator needs to walk upward through the tree, counting generation boundaries. The `count_generations_upward()` utility in `commission/generation.rs` already does exactly this. It was built for stairstep Walk 2 (generation overrides after breakaway).

Do we reuse it, or extract a cleaner shared interface?

## The Decision

Reuse `count_generations_upward()` directly. Map boundary-rank nodes to the `breakaway_set` parameter.

## The Reasoning

The utility exists, is tested (6 unit tests), and its interface works for both consumers. The `breakaway_set` parameter carries boundary-rank nodes in generation context. This is a semantic mismatch. The parameter name says "breakaway" but the data is "boundary rank nodes." The behavior is identical.

Extracting a shared interface was considered. It would mean:
- Renaming `breakaway_set` to something neutral (e.g., `boundary_set`)
- Updating stairstep Walk 2 to use the new interface
- Re-running all stairstep tests to verify no regression

This has cost with no functional gain. Both consumers work. The existing tests cover the behavior. Renaming does not change what the code does.

## Revisit Trigger

If a third consumer of the generation counting logic appears, extract a common core with a cleaner interface. HEU-288 (infinity commission mode) is the likely candidate. It would need the same upward walk with different step criteria.

The project's three-case abstraction threshold applies. Two consumers with a semantic mismatch is tolerable. Three means the pattern is established and worth naming properly.

## What This Means

- `calculate_generation()` calls `count_generations_upward()` with boundary-rank nodes in the `breakaway_set` parameter.
- The `boundary_check` closure handles eligibility filtering based on `ineligible_creates_boundary`.
- No changes to stairstep or the shared utility.
- Documented here so future developers understand the semantic mismatch is intentional.
