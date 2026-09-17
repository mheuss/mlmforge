# Checking that a test can fail

A test suite that passes tells you nothing on its own. It passes against
correct code and against code whose guard you deleted, unless something proves
otherwise. The cheap proof is to break the code on purpose and watch a test go
red.

The technique is not the hard part. Running it honestly is.

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

## A double inherits the gaps of what it embeds

See [test-doubles.md](test-doubles.md).
