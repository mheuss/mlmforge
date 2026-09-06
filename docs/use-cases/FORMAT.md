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

### The Usage block

A Usage block shows a **caller using the solution**. It does not reproduce the
implementation.

Show the call, the types that cross the boundary, and the decision a caller
makes with the result. Do not paste a function body, and do not copy lines out
of the files the entry names.

This sharpens the template above rather than replacing it. The template already
says "Example showing how to use the solution". Entries drifted from it because
the reason was never written down.

The reason: a block that reproduces implementation asserts a fact about code in
another file, and nothing checks it. Not the build, not the tests, not CI. The
only detector is a reader who happens to know both the entry and the current
source. This workspace already applies that rule to comments, where a comment
may not assert a fact about code outside the lines it sits on. A pasted block is
the same assertion at a longer distance, with no compiler in between.

A call-site example ages differently. If the implementation changes underneath
it, the example stays true, because it never claimed to be the implementation.
If the signature changes, the example is wrong in one visible line, and the
entry's **Files:** line says where to look.

Where an implementation detail is the point of the entry, put it in
**Solution:** as prose. Prose that ages reads as dated. A code block that ages
reads as current, which is what makes it dangerous.

An entry may have no Usage block at all. That is better than a block that
restates what **Solution:** already says.

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
