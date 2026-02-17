---
description: Project-wide code review for systemic issues
---

# Project Review

This command performs a whole-codebase review looking for systemic issues that per-commit reviews miss. It dispatches parallel agents scoped to independent areas, then compiles findings into a single report.

This is report-only by default — it does not make changes unless you ask.

## Step 1: Load Project Context

Read `CLAUDE.md` to understand conventions, architecture, and project structure.

If `DEVELOPMENT.md` exists, read it too for architectural decisions and patterns.

These documents define the standard. Findings in Step 3 are measured against what's documented here.

## Step 2: Scope the Review

Determine the codebase areas to review. Use the project structure (top-level directories, modules, or domains) to divide the work into independent areas.

Dispatch a parallel agent for each area. Each agent receives:
- The area it's responsible for (directory path or module scope)
- The project conventions loaded in Step 1
- The review categories from Step 3

> "Reviewing [N] areas in parallel:"
> - [area 1 — e.g., `src/api/`]
> - [area 2 — e.g., `src/components/`]
> - [area 3 — e.g., `lib/`]

## Step 3: Review Categories

Each agent reviews its area for the following:

1. **Dead code** — Unused functions, unreachable branches, orphaned files
2. **Simplification opportunities** — Overengineered abstractions, unnecessary indirection
3. **Pattern violations** — Inconsistency with project conventions, coupling issues, duplicated logic
4. **Cross-file consistency** — Stale references, terminology drift, format mismatches between files that should agree
5. **Test gaps** — Untested happy paths, missing error/edge case coverage (skip if no test infrastructure exists)
6. **Unhandled edge cases** — Boundary conditions, null/empty inputs, concurrent access
7. **Dense or unreadable code** — High cyclomatic complexity, long functions, unclear naming
8. **Security** — Injection risks, hardcoded secrets, overly permissive defaults

## Step 4: Compile Findings

Collect results from all agents. For each finding, report:

- **File and line** — e.g., `src/api/auth.ts:42`
- **Category** — From the list in Step 3
- **Severity** — High, medium, or low
- **What's wrong** — Brief description
- **Suggested fix** — What to do about it

Deduplicate findings that multiple agents flagged (e.g., a pattern violation visible from two modules).

## Step 5: Present Report

Present findings grouped by severity (high first), then by category within each severity level.

> "## Review Findings"
>
> "### High Severity"
> - [finding 1]
> - [finding 2]
>
> "### Medium Severity"
> - [finding 3]
>
> "### Low Severity"
> - [finding 4]
>
> "Found [N] issues: [H] high, [M] medium, [L] low."

If high-severity issues exist:

> "Want me to address the high-severity issues now?"

## Step 6: Notify

If `~/.claude/hooks/notify.sh` exists, fire a notification:

```bash
echo '{"hook_event_name":"Notification","message":"Project review complete","cwd":"'"$(pwd)"'"}' | ~/.claude/hooks/notify.sh
```

#### Key Behaviors

This command is designed to **find systemic issues**, not review individual changes:

1. **Report-only by default** — Does not modify code unless the user asks
2. **Parallel dispatch** — Each area gets its own agent for speed
3. **Adapts to project type** — Skips test gap checks if no test infrastructure exists
4. **Conventions are the standard** — Uses CLAUDE.md as the baseline for what "correct" looks like
5. **Complements per-commit review** — The code-reviewer skill catches change-level issues; this catches codebase-level drift
