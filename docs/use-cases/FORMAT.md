# Use-Case Documentation Format

## When to Document

Document a use-case when:
- A new feature is implemented that solves a business problem
- A pattern emerges that could be reused across contexts
- A non-obvious solution is found that future developers should know about

## Entry Format

Each use-case entry in a domain file follows this structure:

````markdown
### UC-{domain}-{number}: {Title}

**Added:** {version}
**Files:** `path/to/main/file.go`, `path/to/other/file.go`

**Problem:** One sentence describing the business need.

**Solution:** 2-3 sentences describing how it's solved, including key functions/types involved.

**Usage:**
```go
// Example showing how to use the solution
```

**Notes:** Any caveats, edge cases, or related use-cases.
````

## Domain File Structure

Each domain file (`{domain}.md`) starts with:

```markdown
# {Domain Name} Use-Cases

Use-cases for the {domain} bounded context.

## Table of Contents

- [UC-{domain}-001: Title](#uc-{domain}-001-title)
```

## Numbering

- Use sequential numbers per domain: `UC-NET-001`, `UC-COM-001`, `UC-FIN-001`, etc.
- Domain prefixes: NET (Network Engine), COM (Commerce), FIN (Financial), IDN (Identity), ENG (Engagement), OPS (Operations), PTL (Portals), PLT (Platform)

## Maintenance

- Update use-cases when the underlying implementation changes
- Mark deprecated use-cases with `**Status:** Deprecated — replaced by UC-{domain}-{number}`
- Remove deprecated entries at the next major version
