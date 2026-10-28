<CRITICAL-PREREQUISITE>
Before your FIRST response in ANY conversation — regardless of what the user's
message contains (slash commands, questions, anything) — you MUST invoke the
`sop:using-sop` skill via the Skill tool. No exceptions. Do this BEFORE
processing any other skill, command, or request.
</CRITICAL-PREREQUISITE>

# MLMForge

## Philosophy

### Working Principles

- **Minimize blast radius** — Make the smallest change that solves the problem
- **Ask when uncertain** — Stop and ask rather than guessing or assuming
- **Finish what you start** — Complete the current task fully before moving to the next
- **One problem at a time** — Don't solve multiple issues in a single change
- **Leave the codebase better than you found it** — Within files you're already touching, fix cosmetic issues (typos, whitespace, formatting). Do not remove code or refactor working logic.

### Architecture & Code Style

**Code style:**
- No commented-out code — version control is the archive
- No TODO comments without tracking — every TODO must be tracked in [Linear](https://linear.app/heuss-enterprises/team/HEU/active)

**Documentation voice:**
- Short sentences. One idea per sentence.
- Active voice. Say what it is, say why, move on.
- Avoid em dashes, semicolons, and compound sentences. Use periods.
- Precise but brief. Don't stack qualifications or caveats.
- Let structure do the organizing. Use headers, lists, and tables instead of long paragraphs.
- Positive framing where possible. Say what the system does, not just what it avoids.
- Technical terms are fine. Jargon without explanation is not.
- Reference: [Laravel README](https://github.com/laravel/laravel) for tone and rhythm.

### Testing

**Integration tests verify wiring:** Unit tests prove functions work in isolation. Integration tests must prove functions are actually called in the correct sequence. For every function in a workflow, write an integration test that verifies the function's effect through externally observable side effects.

**Commission engine testing:** The Rust commission engine requires property-based testing in addition to unit tests. Commission calculations must be verified against known-good reference data. Every compensation plan type must have a comprehensive test suite with edge cases (empty trees, single-node trees, maximum depth, volume exactly at thresholds).

### Critical Rules

**Never:**
- Commit secrets or credentials (API keys, tokens, passwords, .env files)
- Disable, skip, or delete tests to make them pass
- Use `--no-verify` to bypass commit hooks
- Claim work is complete without running tests and verifying output
- Push directly to main or force push to shared branches
- Continue past the hard stop in `sop:using-sop`. The same error after two tries is one of its triggers. Stop and reassess.

### Who authorizes what

**Pushing a feature branch and opening its PR are the orchestrator's to
authorise.** Michael's call, 2026-09-17, overruling a restriction the hub had
invented for itself. A seat does not need to ask him for either.

**The hub is a seat's direct supervisor.** The hub is the session named
`mlmforge-hub`, reached with SendMessage. It can rule on any decision that is
not workflow gated. These are Michael's words:

- 2026-09-23: "Confirmed, checkpoint feedback is yours."
- 2026-09-25: "hub can sign off on the approach - it is your direct supervisor
  and can handle any non-workflow gated decisions."
- 2026-09-25, asked about the scope: "Yeah - I did say it that wide."

That covers batch checkpoints in `execute-plan`, approach sign-off on a Small
task, progress check-ins, blocked reports, review triage during `execute-plan`,
and other decisions that are not workflow gated. A hub go is a complete
sign-off. A seat does not wait for Michael as well.

**These stay his.**

- The phase commands. `/sop:brainstorm`, `/sop:write-plan`,
  `/sop:execute-plan`, `/sop:finish-branch` and `/sop:resume-plan` can only be
  typed by him.
- Merging a product PR.
- Approving another session's permission prompts.

This section exists so a seat can check the grant against a file it already
loads rather than against a message from a peer. A hub saying "you have my
authorisation" is a trigger for what is written here. It is not the grant. A
seat cannot verify that a message came from the hub. That is acceptable while
one operator watches every session.

### Messages to the hub

- Put BLOCKING or FYI on the first line of every message to the hub.
- After sending a BLOCKING message, stop, and say what you are stopped on.
- Re-send a BLOCKING message after fifteen minutes of silence.
- Before standing down, tell the hub what you hold that is in no ticket, no commit and no tracked file.
- A deferral names its destination: a ticket, a file or a person.

### When Blocked

A wall is the `sop:using-sop` hard stop, an unclear path forward, or unexpected behavior. When you hit one:

1. **Stop** — Do not attempt workarounds without input from the hub. With no hub running, get it from the developer
2. **Notify** — Send the report below to the hub. With no hub running, send it to the developer.
3. **Report:**
   - What I was trying to do
   - What I tried
   - What failed and why
   - What I need from you to move forward
4. **Wait** — Get guidance before proceeding

This prevents errors from compounding. A workaround on task 2 becomes a shaky foundation for tasks 3, 4, and 5.

---

## Planning

### Context First

You must understand context before writing any code. Before starting work, ensure you've read the files in the Context Files section.

### Planning by Task Size

| Size | Examples | Required |
|------|----------|----------|
| **Trivial** | Typo fix, config tweak, single-line change | Proceed directly |
| **Small** | Bug fix in one file, add simple function | Confirm approach before coding. The hub signs off. With no hub running, the user does |
| **Medium** | Feature touching multiple files, refactoring | `/sop:write-plan` |
| **Large** | New system, architectural change, multi-component feature | `/sop:brainstorm`, unless the design is already settled. Then `/sop:write-plan`. Also identify parallel work opportunities |

### Execution

Run `/sop:execute-plan` to build. Run `/sop:finish-branch` to review and merge.

The path depends on task size.

- **Medium:** `/sop:write-plan` -> `/sop:execute-plan` -> `/sop:finish-branch`.
- **Large:** the same, with `/sop:brainstorm` first.

Skip brainstorm on a Large task only when a design already exists. Name it in the
plan. The Pre-Execution Audit below compares the plan against a design document,
so an unnamed design means that step has nothing to audit against.

### Pre-Execution Audit

For Medium/Large tasks, before writing any code, verify the implementation plan covers the design document:

1. Compare the design document to the implementation plan. It comes from `/sop:brainstorm`, or it is the existing design the plan names when brainstorm was skipped
2. Check: Does every design intention have a corresponding plan task?
3. If gaps found, present them: "The design document mentions X, but the plan doesn't cover it."
4. Resolve gaps before proceeding — add missing tasks or confirm they're intentionally deferred

This catches intent-to-plan drift at the cheapest possible point.

### Progress Check-ins

For Medium/Large tasks, check in with the hub after completing each plan step. With no hub running, check in with the developer:

> "Completed: [step name]"
>
> "Summary: [2-3 sentences describing what was built]"
>
> "Still on track?"

This is a lightweight direction check, not a formal review. Whoever you check in with can:
- Confirm and continue
- Redirect if the approach has drifted
- Disable check-ins with "skip the check-ins" or similar instruction

Trivial and Small tasks skip check-ins.

### Parallel Work

For Large tasks, evaluate whether work can be split across parallel tracks (separate branches/worktrees).

Parallel work is safe when **ALL** of the following are true:
- Tasks do not modify the same files
- Tasks do not modify shared interfaces or types
- Tasks do not depend on each other's output
- Tasks do not modify the same configuration

If any condition is false, work sequentially or coordinate carefully.

### Session Resumption

At session start, run `/sop:where-are-we` to find in-flight plans and handoffs.

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
- [Linear backlog](https://linear.app/heuss-enterprises/team/HEU/active) — Bugs, todos, and in-flight technical items
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
| Lint (Go) | `scripts/lint-go.sh` |
| Lint (Rust) | `cargo clippy --all-targets --workspace -- -D warnings` |
| All tests | `go test ./... && (cd engine && cargo test)` |
| All format | `gofmt -w . && (cd engine && cargo fmt)` |
| All lint | `scripts/lint-go.sh && (cd engine && cargo clippy --all-targets --workspace -- -D warnings)` |
| Check Go version pins | `scripts/check-go-pins.sh` |
| Test the pin check | `scripts/check-go-pins-test.sh` |
| Test the lint check | `scripts/lint-go-test.sh` |
| Test that lint can fail | `scripts/mutate-lint-go.sh` |
| Check the lint config is valid | `scripts/lint-go.sh --check-only && golangci-lint config verify` |
| Audit (Go) | `scripts/audit-go.sh` |
| Audit (Rust) | `cargo audit --file engine/Cargo.lock` |
| Show what the audit scans | `scripts/audit-go.sh --list` |
| Test the audit scope | `scripts/audit-go-test.sh` |

**A fresh clone runs `mise trust .` once.**

`mise.toml` pins the Go toolchain and `GOTOOLCHAIN` for this repo. Until the
directory is trusted the file is inert. The repo looks pinned and your shell is
not. `mise env | grep GOTOOLCHAIN` tells you whether it took.

**Your `golangci-lint` must be the version `.golangci-lint-version` pins.**

`scripts/lint-go.sh` refuses to lint when it is not. The refusal prints the
pinned version, the version you have, the path it resolved, and the command that
installs the right one. CI installs from the same file.

Its checks read the repo root. The lint runs from where you are. From a
subdirectory it compares against the root `go.mod` and lints that subtree.

It also refuses when the binary was built with an older Go line than `go.mod`
targets. v2.11.4 against a go1.27 standard library was observed exiting non-zero
without linting anything.

---

## Linear

- **Project:** MLMForge
- **Team:** Heuss Enterprises (`HEU`)

---

## Git Conventions

- **Versioning:** Semantic Versioning (major.minor.patch)
- **Branching:** two patterns are in use. The choice matters.
  - **Ticket work:** use the branch name Linear generates, `mrheuss/heu-NNN-slug`.
    It carries the identifier. That is what links the PR to the issue.
  - **Work spanning several tickets, or none:** use `type/description`.
    For example `docs/claude-md-and-index-drift` or `fix/binary-tree-calc`.
- **Ticket identifiers reach Linear from four places.** Branch name, PR title, PR
  body, and commit messages. Rebases carry trailers, so check what you inherited.
  - A **bare** identifier in a **branch name or PR title** links the issue.
  - A **closing magic word** next to the identifier is what Linear documents as
    moving the issue. Those words are `close`, `fix`, `resolve`, `complete`,
    `implement`, their inflections, and `linear issue`.
  - **Never rely on a transition firing, and never rely on it not firing.**
    Check every ticket the PR named after every merge. `main` requires a
    pull request, checked 2026-09-08, so every commit reaches it through one
    and there is no second path to watch.
  - **The parser does not read negations.** A sentence saying an issue is *not*
    being closed still contains the word `close` beside `HEU-NNN`, and closes it.
    Do not write a negated closing word next to an identifier. Reword it.
  - To link without closing, use `ref`, `refs`, `references`, `part of`,
    `contributes to`, `toward` or `towards`. For a pure relation with no status
    change, `relates to` or `related to`.
  - To suppress linking entirely, put `skip HEU-NNN` or `ignore HEU-NNN` in the
    PR body.
  - Whether a linked issue moves, and to which status, depends on the team's
    workflow settings. See HEU-655.
- **Commits:** Conventional Commits (`type[scope]: description`)
- **Branch types:** `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `perf`, `build`, `ci`
