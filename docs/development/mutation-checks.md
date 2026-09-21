# Checking that a test can fail

A test suite that passes tells you nothing on its own. It passes against
correct code and against code whose guard you deleted, unless something proves
otherwise. The cheap proof is to break the code on purpose and watch a test go
red.

The technique is not the hard part. Running it honestly is.

## Prove the unmutated copy passes first

The four cases below ask whether the mutated run was sound. Did it compile? Did
the filter match a test? Did the anchor land? Did a checkout eat uncommitted
work? None of them asks whether the *unmutated* run is sound.

If the harness cannot reproduce a clean pass on an untouched copy, every row
after that is measuring the harness. Run the suite against an unmodified copy
before any mutation. Refuse to report anything unless it comes back green with
a count.

```bash
baseline=$(run_suite_against "$unmutated_copy")
case "$baseline" in *", 0 failed"*) ;; *) echo "baseline not green"; exit 1 ;; esac
```

Keep the baseline's own denominator. Hold every later row to it. A mutation that
truncates the run and goes red otherwise reads exactly like one the whole suite
caught.

This is not hypothetical. A harness ran the copy from a directory where the code
under test could not resolve its own default paths. It reported every row as
caught. It scored a perfect run. It was testing nothing. The failing case was
the same one every time. It had nothing to do with any mutation.

## The harness lies in four ways, and all four look like a clean pass

Every one of these produces zero failing tests. So does a suite that genuinely
catches nothing. Reading "0 failures" and concluding "the mutation survived" is
wrong in four of the five cases, and right only in the last.

**Run this checklist against the harness, not from memory.** Three of the four
were introduced while fixing the previous one, which is why this is a procedure
rather than four things to watch out for.

### 1. The mutation did not compile

Deleting a branch often leaves a variable unused, which Go rejects. `go test`
then emits no test lines at all, and a grep for `--- FAIL` returns zero.

Gate every run on the build, inside the harness function:

```bash
mise exec -- go build ./... || { echo BUILD_FAILED; return; }
```

Neuter a branch with `&& false` rather than deleting it, so the variables stay
used.

### 2. The filter matched no test

Rename a function and forget the test, and `-run TestTheNewName` selects
nothing. No tests run, no failures print, every mutation reads as surviving.

Count what ran, in the same function, and refuse a result without it:

```bash
ran=$(echo "$out" | grep -cE '^=== RUN')
[ "$ran" -lt 2 ] && { echo "NO TESTS RAN"; return; }
```

Report the count beside the failures, as `2 of 27`. A baseline of `0 of 27` is
meaningful; `0 of 0` is not.

### 3. The anchor matched twice, or not at all

A replacement keyed on text can land somewhere unintended or nowhere. A
one-tab pattern also matches inside a two-tab one:

```
'\t}, reconcile)'  also matches within  '\t\t}, reconcile)'
```

Assert the match count before replacing:

```python
assert s.count(old) == 1, "count %d" % s.count(old)
```

When a fragment appears in two functions, slice to the function first and
assert within the slice.

### 4. `git checkout` reverted work that was never committed

Reverting a mutation with `git checkout -- <file>` restores the file to `HEAD`.
If that file also holds uncommitted work, the work is gone, and every later
measurement describes a tree you did not intend.

**Commit the change under test before mutating it.** Then `checkout` restores
the thing being measured rather than deleting it.

Knowing the rule is not enough. This one was written from four occurrences and
then happened a fifth time, in the session that wrote it, because an anchor
failed partway through a loop and the loop's `checkout` ran anyway. Make the
harness refuse instead:

```bash
[ -n "$(/usr/bin/git status --short -- "$FILE")" ] && { echo "UNCOMMITTED WORK IN $FILE"; return; }
```

Run it before the first mutation, not before each one: by the time a mutation
is applied the file is dirty by design.

## After a fix, re-run the whole set

A fix can make a mutation survive in code it never touched. Re-running the rows
the fix was aimed at will not find it.

The mechanism is that a repair and the mutation that proved it live at different
points on the same path. Add a guard upstream. It absorbs the input that made
the downstream repair observable. The repaired line still works. No test fails.
Nothing now distinguishes it from the broken version.

Worked case. A comparison truncated a prerelease version. `1.26rc1` compared as
`26`. Deleting that truncation failed a test. That failure got it pinned. A
later fix added a check that every component parses as a number. The untruncated
value fails that check. So it skips the comparison instead of reaching it. Both
versions of the line now produce the same silent result. The truncation went
from caught to surviving without being edited. Only a full re-run showed it.

The same trigger has a second effect, on the harness rather than the code.
Reformatting the code under mutation leaves expressions matching text that has
moved. Those rows report as not applied. That is the right answer only if the harness
separates a stale match from a mutation that ran. Re-read the not-applied
rows after every edit to the code under mutation: a stale expression and a
genuinely unreachable one look identical.

**Re-run every row after every fix.** The cost is one suite run per mutation.
The alternative is a guard that reads as pinned and is not.

## A filtered reading discards the reason the count moved

A pipeline that counts result lines throws away everything else the run said.
When the count is the thing being reported, that is the one place the
explanation was.

The same suite on the same commit reported 1537 passing and 1358 passing, both
at exit 0. The difference was a container that did not start. The run said so:
it printed a line naming the missing container to stderr. An awk counting
`RUN`, `PASS`, `SKIP` and `FAIL` lines matched none of them and dropped it. The
number was published as verification. The explanation had already been
given.

**Keep the raw output. Read it whenever a figure moves.** A summary is fine
when the figure is stable and useless the moment it is not.

This is worse than a tool that says nothing. The evidence existed. The
instrument removed it. A gap in coverage leaves the diagnostic waiting to be
found. A filter destroys it in transit.

## A suite cannot tell you it is testing the wrong requirement

Tests check the code against what their author believed the requirement was.
When that belief is wrong, they pass. They keep passing. Every gate that
reads them agrees.

Nine cases on one branch asserted that a lint proceeds when an input file
cannot be read. The design said the opposite in two places: that the script
exits non-zero without linting when an input cannot be read, and that it fails
closed on any condition it cannot evaluate. The nine passed a claim check, a
per-task review, a full code review and an external reviewer.

Only an adversarial read caught it. The reason is worth keeping: **the diff
was internally consistent.** Code and tests agreed with each other. A reviewer
comparing them finds nothing. The disagreement was between the tests and a
requirement document neither of them cites.

**Re-read the requirement, not the diff, when a test encodes a refusal or a
skip.** A test that says a check is skipped is asserting that skipping is
correct. That is a claim about the requirement, not about the code.

## A surviving mutation is not always a defect

Sometimes the code is equivalent under the mutation and no test can tell them
apart. That is a finding about the code, not the tests. Say which it is rather
than adding a test that pins an accident.

## The fixture is part of the code under test

A mutation pass aimed only at the implementation misses assertions that cannot
fail because of the data they run on:

- An assertion that an error contains `"t"` passes on any message containing
  "the" or "event".
- `strings.ToUpper` on a uuid of digits and hyphens is a no-op, so a
  case-difference case compares a value with itself.

Mutate the fixture too: make the value a row varies identical to the baseline
and check that the row goes red.

## A red says the test failed, not which line failed it

`require` and `assert` differ in one way that decides what a mutation pass
established. In testify v1.11.1 a `require` helper calls its `assert` twin and
then calls `t.FailNow()` when that returns false, which stops the test
function. An `assert` helper returns a bool and execution continues.

So a failing `require` above the assertion you are probing means your
assertion never ran. The suite still goes red, and the red is easy to credit to
the wrong line.

Two findings look identical from the outside, and they need different fixes:

- The assertion cannot fail. It ran, and it passes whatever the code does.
  Delete it or make it specific.
- The assertion was never reached. A `require` above it aborted first. The
  assertion may be fine. The test is shorter than it looks.

**Say which of the two a mutation pass established, never just "cannot fail."**

The cheap way to tell them apart is to make the assertion fail on purpose and
check that its own message is the one that prints. If a different message
prints, you have found the second case.

This is not in the numbered list above, because it does not look like a clean
pass. It looks like a catch, which is worse: a clean pass invites suspicion and
a red does not.

## A double inherits the gaps of what it embeds

See [test-doubles.md](test-doubles.md).
