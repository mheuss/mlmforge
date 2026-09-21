# Guards in shell scripts

A guard that cannot evaluate its condition must refuse. The failure mode worth
watching for is the opposite: a guard that returns the same status for "no" and
for "could not tell". The script then proceeds as though the answer were no.

## `grep -q` behind a pipe under `pipefail` reads a match as no match

`grep -q` exits at its first match without draining its input. The writer is
then killed by SIGPIPE. `pipefail` makes the whole pipeline return 141. An
`if` reads 141 as false.

The guard reports the opposite of the truth, and only on inputs large enough
that the writer is still writing when grep exits. Below that size it is
correct.

```bash
# wrong under `set -o pipefail`
if printf '%s\n' "$body" | grep -qE 'pattern'; then

# right
if grep -qE 'pattern' <<< "$body"; then
```

**There is no threshold to test against.** Measured on one machine, the piped
form matched 40 of 40 times at 19KB, 32 of 40 at 39KB, and 0 of 40 at 79KB. It
is a race across a band. A fixture has to sit well past the band rather than
just past a number.

`grep -q` also cannot distinguish no-match from its own error: both leave the
`if` false. Where that matters, capture the status and test it.

## A condition you cannot evaluate is not a condition that passed

Reading an input with `2>/dev/null` and then treating an empty result as
"nothing to check" turns every read failure into a silent pass. A missing file,
a directory, and a permission error all arrive as the same empty string.

Decide which of the two the guard means. Say so where it is written. If it
refuses, the message should name the operation that failed rather than a cause
the run did not observe.

## `exec` does not run an `EXIT` trap

`exec` replaces the process image. Cleanup registered with `trap ... EXIT`
never fires. A temp file created earlier outlives every run that reaches the
`exec`. Remove it before the `exec` rather than relying on the trap.

The trap still earns its place on every path that does not `exec`.

## A digit test is not a number test

`[[ $x =~ ^[0-9]+$ ]]` accepts strings the shell's arithmetic then refuses. A
twenty-digit value passes the pattern and fails `[ "$x" -lt "$y" ]`. That
prints a diagnostic naming a line number and leaves the comparison unmade.
Bound the length: `^[0-9]{1,9}$`.

## A missing separator turns one value into two copies of itself

`${v#*.}` returns `v` unchanged when `v` holds no dot. The same goes for every
`#`, `##`, `%` and `%%` expansion whose pattern is absent.

So splitting a value into major and minor gives the same string twice. Both
halves then pass a digit test, and the comparison between them reports that
they agree.

```bash
v=127
major=${v%%.*}     # 127
rest=${v#*.}       # 127, because there is no dot to cut at
minor=${rest%%.*}  # 127
```

A built-with line of `go127` reached the lint this way. Two components that are
one component wrote themselves past a guard that refuses an older Go line,
because 127 is not older than 127.

**Compare the remainder to the value before trusting the split.** Equal means
the separator was absent, not that the tail was empty.

```bash
if [ "$rest" = "$v" ]; then minor=; fi
```

Clearing the component is enough. The digit test below it already refuses an
empty string, so the refusal keeps its existing message and its existing test.
