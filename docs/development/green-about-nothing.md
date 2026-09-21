# A green that covered nothing

A check reports pass. It covered less than you think. Nothing in the output
says so, and a pass over half the material is byte-identical to a pass over all
of it.

This is not the same as a test that cannot fail. That one is empty. These
checks work, on the things they reach.

## From one branch, one day

HEU-788, 2026-09-21.

- **The text pass could not see the plan directory.** That directory is
  gitignored. The skill builds its scope from the diff plus untracked files, so
  it is neither. Run to the letter, the pass would have read none of the plan
  amendments or the handoff, and returned a verdict.
- **The worker staleness check watched only Rust sources.** A manifest or a
  lockfile changes what gets compiled without any source file being touched. A
  dependency bump changed the binary and the check reported fresh.
- **A test asserted a distinction it could not make.** It named two failures
  that had rendered identically and claimed to tell them apart. Its doubles
  were keyed literals with no message, so both rendered the same fallback. It
  would have passed for exactly the pair it existed to separate.

The first two are tools. The third was written deliberately, by someone who had
described the hazard in writing an hour earlier.

## What they share

Not the blind spot. Every check has one.

**The output cannot be told apart from a complete one.** A caller reading
"pass" gets the same bytes either way, so nothing downstream can compensate.

That is why widening the scope is the weaker fix. The next gap is invisible
again. Saying what was covered survives the next gap.

## What to do

**Name the scope beside the verdict.** "Clean over 4 files" is a different
claim from "clean", and the second is the one that misleads.

**Say what a check does not reach, in the same breath as its result.** A
reviewer that reports "verified the diff; the live-database claims are outside
what I can check statically" has handed you the gap. One that reports "verified"
has not.

**Where a scope is set by something invisible to the check, say so at the
call site.** The text pass cannot discover a gitignored directory. Naming the
paths in the dispatch is the only fix available from outside.

## Naming the trap does not disarm it

The third instance above was written by the person who had named it. Having
described the hazard, they felt covered, and wrote the assertion anyway inside
the window where they were most aware of it.

Treat a hazard you have just described as more likely to catch you, not less.
