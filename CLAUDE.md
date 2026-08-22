<CRITICAL-PREREQUISITE>
Before your FIRST response in ANY conversation — regardless of what the user's
message contains (slash commands, questions, anything) — you MUST invoke the
`sop:using-sop` skill via the Skill tool. No exceptions. Do this BEFORE
processing any other skill, command, or request.
</CRITICAL-PREREQUISITE>

# MLMForge

## Philosophy

### Core Beliefs

We build software through small, verified steps:

- **Incremental progress over big bangs** — Small changes that compile and pass tests
- **Learn before implementing** — Study existing code and plan before writing
- **Clear intent over clever code** — Be boring and obvious
- **Single responsibility** — Per function, per class, per module
- **Avoid premature abstractions** — Don't generalize until you have three concrete cases
- **Consistency over preference** — Match existing patterns even if you'd do it differently

### Working Principles

- **Verify before claiming** — Run tests and confirm output before asserting success
- **Minimize blast radius** — Make the smallest change that solves the problem
- **Ask when uncertain** — Stop and ask rather than guessing or assuming
- **Challenge, don't comply** — When the user suggests an approach, evaluate it critically. If you see problems, risks, or better alternatives, say so. Agreeing to avoid friction wastes time and produces worse outcomes.
- **Finish what you start** — Complete the current task fully before moving to the next
- **One problem at a time** — Don't solve multiple issues in a single change
- **Leave the codebase better than you found it** — Within files you're already touching, fix cosmetic issues (typos, whitespace, formatting). Do not remove code or refactor working logic.

### Architecture & Code Style

**Architecture:**
- Composition over inheritance — use dependency injection
- Interfaces over singletons — enable testing and flexibility
- Explicit over implicit — clear data flow and dependencies
- Fail fast — descriptive error messages with context
- Handle errors at the appropriate level — never silently swallow exceptions

**Code style:**
- Match, don't invent — find similar code and follow its pattern exactly
- Follow existing conventions — match the project's style, not your preferences
- Use project tooling — the project's formatter, linter, and build system
- No commented-out code — version control is the archive
- No TODO comments without tracking — every TODO must be tracked in [Linear](https://linear.app/heuss-enterprises/project/mlmforge-aa8bdeecd6ac)

**Documentation voice:**
- Short sentences. One idea per sentence.
- Active voice. Say what it is, say why, move on.
- Avoid em dashes, semicolons, and compound sentences. Use periods.
- Precise but brief. Don't stack qualifications or caveats.
- Let structure do the organizing. Use headers, lists, and tables instead of long paragraphs.
- Positive framing where possible. Say what the system does, not just what it avoids.
- Technical terms are fine. Jargon without explanation is not.
- Reference: [Laravel README](https://github.com/laravel/laravel) for tone and rhythm.

**Go-specific:**
- Follow Effective Go and Go Code Review Comments conventions
- Use `gofmt` formatting (non-negotiable)
- Prefer returning errors over panicking
- Use context.Context for cancellation and timeouts
- Keep interfaces small — one or two methods

**Rust-specific:**
- Follow Rust API Guidelines
- Use `cargo fmt` formatting (non-negotiable)
- Prefer `Result<T, E>` over panicking
- Use `clippy` warnings as errors
- Leverage the type system for correctness — encode invariants in types

### Testing

**Test-Driven Development is mandatory:**
1. **Red** — Write a failing test first
2. **Green** — Write minimal code to pass
3. **Refactor** — Improve while keeping tests green

**Test quality:**
- Test behavior, not implementation — verify what the code does, not how
- One behavior per test — multiple assertions are fine if they all verify the same action. If a test fails, you should know which behavior broke without reading the test body.
- Tests must be deterministic — no flakiness
- Avoid over-mocking — mock external dependencies, not internal code

**Integration tests verify wiring:** Unit tests prove functions work in isolation. Integration tests must prove functions are actually called in the correct sequence. For every function in a workflow, write an integration test that verifies the function's effect through externally observable side effects.

**Commission engine testing:** The Rust commission engine requires property-based testing in addition to unit tests. Commission calculations must be verified against known-good reference data. Every compensation plan type must have a comprehensive test suite with edge cases (empty trees, single-node trees, maximum depth, volume exactly at thresholds).

### Critical Rules

**Never:**
- Commit secrets or credentials (API keys, tokens, passwords, .env files)
- Disable, skip, or delete tests to make them pass
- Use `--no-verify` to bypass commit hooks
- Claim work is complete without running tests and verifying output
- Push directly to main or force push to shared branches
- Continue after 3 failed attempts — stop and reassess

### When Blocked

When you hit a wall (3 failed attempts, unclear path forward, unexpected behavior):

1. **Stop** — Do not attempt workarounds without developer input
2. **Notify** — If `~/.claude/hooks/notify.sh` exists, fire a notification so the developer knows you need attention:
   ```bash
   echo '{"hook_event_name":"Notification","message":"Blocked — need your input","cwd":"'"$(pwd)"'"}' | ~/.claude/hooks/notify.sh
   ```
3. **Report:**
   - What I was trying to do
   - What I tried
   - What failed and why
   - What I need from you to move forward
4. **Wait** — Get developer guidance before proceeding

This prevents errors from compounding. A workaround on task 2 becomes a shaky foundation for tasks 3, 4, and 5.

---

## Planning

### Context First

You must understand context before writing any code. Before starting work, ensure you've read the files in the Context Files section.

### Planning by Task Size

| Size | Examples | Required |
|------|----------|----------|
| **Trivial** | Typo fix, config tweak, single-line change | Proceed directly |
| **Small** | Bug fix in one file, add simple function | Confirm approach with user before coding |
| **Medium** | Feature touching multiple files, refactoring | `/groom` |
| **Large** | New system, architectural change, multi-component feature | `/groom`, plus identify parallel work opportunities |

### Execution

After planning is complete, `/groom` hands off to `/preflight` which offers execution options (subagent-driven, parallel session, manual, or save for later).

### Pre-Execution Audit

For Medium/Large tasks, before writing any code, verify the implementation plan covers the design document:

1. Compare the design document (from brainstorming) to the implementation plan
2. Check: Does every design intention have a corresponding plan task?
3. If gaps found, present them: "The design document mentions X, but the plan doesn't cover it."
4. Resolve gaps before proceeding — add missing tasks or confirm they're intentionally deferred

This catches intent-to-plan drift at the cheapest possible point.

### Progress Check-ins

For Medium/Large tasks, check in with the developer after completing each plan step:

> "Completed: [step name]"
>
> "Summary: [2-3 sentences describing what was built]"
>
> "Still on track?"

If `~/.claude/hooks/notify.sh` exists, fire a notification before the check-in so the developer knows a response is needed:
```bash
echo '{"hook_event_name":"Notification","message":"Step complete — check-in ready","cwd":"'"$(pwd)"'"}' | ~/.claude/hooks/notify.sh
```

This is a lightweight direction check, not a formal review. The developer can:
- Confirm and continue
- Redirect if the approach has drifted
- Disable check-ins with "skip the check-ins" or similar instruction

Trivial and Small tasks skip check-ins.

### Step Reviews

For Medium/Large tasks, invoke the code-reviewer agent after completing major implementation steps — not every small change, but logical chunks like "commission engine binary tree walker complete" or "Commerce context API endpoints implemented."

Step reviews provide actual code analysis, not just self-reported summaries. They catch drift from the implementation plan while there's still time to course-correct.

The final review in pre-commit still applies — step reviews are additive, not a replacement.

### Parallel Work

For Large tasks, evaluate whether work can be split across parallel tracks (separate branches/worktrees).

Parallel work is safe when **ALL** of the following are true:
- Tasks do not modify the same files
- Tasks do not modify shared interfaces or types
- Tasks do not depend on each other's output
- Tasks do not modify the same configuration

If any condition is false, work sequentially or coordinate carefully.

### Update Tracking

After planning, create Linear issues for any new tasks identified.

### Session Resumption

At session start, scan `docs/plans/` for plans marked `**Status:** In Progress` (Markdown bold format). Skip `handoff-*.md`. Handoff notes describe status in prose and quote the marker, so scanning them produces false matches.

```bash
grep -rlE '\*\*Status:\*\* In Progress' docs/plans/ --exclude='handoff-*.md'
```

If found, prompt:

> "Found in-progress work: `{filename}` ({progress summary}). Resume this work?"

If yes, run `/resume-plan`. This ensures work isn't forgotten across session boundaries.

---

## Use-Case Catalog

### Purpose

Prevent code duplication by documenting existing solutions organized by business domain. When implementing new features, check the catalog first to discover if the problem has already been solved.

### Location

`docs/use-cases/`

### Structure

- **INDEX.md** — Lists all domains with descriptions
- **FORMAT.md** — Defines how to document use-cases (entry format, when to document, maintenance guidelines)
- **{domain}.md** — One file per domain containing all use-cases for that area

See `docs/use-cases/FORMAT.md` for entry format and documentation guidelines.

---

## Context Files

Read these at the start of every session:

- `VERSION_HISTORY.md` — Current version and recent changes
- [Linear backlog](https://linear.app/heuss-enterprises/project/mlmforge-aa8bdeecd6ac) — Bugs, todos, and in-flight technical items
- `DEVELOPMENT.md` — Architectural decisions and patterns
- `docs/standards/accessibility.md` — Accessibility and localization requirements

If any of these files are missing, incomplete, or don't answer your questions about the current task, stop and ask before proceeding.

---

## Commands

Run `go` commands from the repo root and `cargo` commands from `engine/`.
There is no `Cargo.toml` at the root, so a bare cargo command run from
there fails outright. The combined rows carry their own `cd`, so they copy
and paste from the repo root as written.

| Task | Command |
|------|---------|
| Build (Go) | `go build ./...` |
| Build (Rust) | `cargo build` |
| Run | Not yet implemented (no HTTP server) |
| Test (Go) | `go test ./...` |
| Test (Rust) | `cargo test` |
| Format (Go) | `gofmt -w .` |
| Format (Rust) | `cargo fmt` |
| Lint (Go) | `golangci-lint run` |
| Lint (Rust) | `cargo clippy --all-targets --workspace -- -D warnings` |
| All tests | `go test ./... && (cd engine && cargo test)` |
| All format | `gofmt -w . && (cd engine && cargo fmt)` |
| All lint | `golangci-lint run && (cd engine && cargo clippy --all-targets --workspace -- -D warnings)` |

---

## Linear

- **Project:** MLMForge
- **Team:** Heuss Enterprises (`HEU`)

---

## Git Conventions

- **Versioning:** Semantic Versioning (major.minor.patch)
- **Branching:** `type/description` (e.g., `feat/commission-engine`, `fix/binary-tree-calc`)
- **Commits:** Conventional Commits (`type[scope]: description`)
- **Branch types:** `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `perf`, `build`, `ci`
