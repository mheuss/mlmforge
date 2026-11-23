# Test doubles

A double inherits the gaps of whatever it embeds, and a double that cannot
observe a thing cannot fail a test named for that thing.

Two doubles reached review on one branch unable to see their own subject.
Neither was caught by reading. Both were caught by mutating the code they were
supposed to be checking and watching nothing go red.

## Answer only what you were asked

A double that returns the same answer regardless of its arguments cannot catch
the caller asking the wrong question.

A transport returned the configured position for any `user_id`. Pointing the
consumer's inspection at the parent instead of the placed node passed every
test. In production that compares one node's position against another node's
row.

Parse the request. Answer for a match, and fail for anything else:

```go
if r.position == nil || r.position.UserID != q.UserID {
    return nil, &EngineError{Code: engineCodeUserNotFound, Message: q.UserID}
}
```

Record what was asked, and assert on it, so the test says which question was
put rather than only what came back.

## Embedding carries the gaps with it

`MemoryTreeStore` ignored `context.Context` entirely until HEU-798.
`PostgresTreeStore` honours it, because a pool does. A double wrapping the
memory store therefore could not see a cancellation, whatever the test was
called.

A test asserting that a compensating write survives a cancelled context passed
against an implementation with no shield at all, because the double it embedded
could not tell the two apart.

When a double stands in for the production implementation, it has to model the
behaviour under test even where the embedded type omits it. The example below
was written while the gap existed. It is kept for the order of its two records.
That order still matters now that the embedded store refuses too:

```go
func (c *deleteRecordingStore) UndoRootProjection(ctx context.Context, treeID, userID string, eventVersion int64) error {
    c.attempts = append(c.attempts, userID)
    if err := ctx.Err(); err != nil {
        return err
    }
    if err := c.MemoryTreeStore.UndoRootProjection(ctx, treeID, userID, eventVersion); err != nil {
        return err
    }
    c.deleted = append(c.deleted, userID)
    return nil
}
```

Record the attempt above the shield. Record the result only after the call it
describes returns without an error. That order is load-bearing. A double that
records only after the shield reports a refused call and a call that never
happened as the same empty slice, and telling those two apart is usually the
reason the double exists.

Before embedding, ask what the embedded type does not do that production does.
Where the answer is filed as a ticket rather than fixed, the double is where it
bites.

## Prefer a failure to a zero value

A double that returns a zero value for an unconfigured case lets a test pass
while comparing against state nothing ever held. Return an error instead, so an
unconfigured case fails loudly rather than agreeing with whatever was expected.

## A list of every method covers only the methods it names

A suite case that loops over "every method" of an interface checks the methods
written in its list. A method added to the interface later is not in that list.
Nothing fails, and a double can skip the contract without anyone seeing it.

This is most likely at a merge. One branch adds methods while another adds the
list. Both merge cleanly, and neither side knew about the other.

```go
calls := []struct {
    name string
    call func() error
}{
    {"Get", func() error { _, err := s.Get(ctx, id); return err }},
    {"Put", func() error { return s.Put(ctx, row) }},
}
```

A `Delete` added to the interface after this was written is not checked here.
After you add a method to an interface, or merge a branch that did, search the
shared suites for cases like this one and add the new methods.

## A double's request log is not a record of what it changed

A double can log some of its calls and not others. An assertion on that log
says nothing about a call the log never records. It passes whether or not the
call happened.

A test that asserts "nothing was mutated" on a log that holds no removals
cannot see a removal. The removal changes the double's state and leaves no
entry.

Wrap the double and record each mutating call before delegating it. Assert on
that record. The wrapper has the same gap as a list of every method. Name every
mutating method of the interface.

HEU-1024 hit this. The first version of its pin asserted on a log that held no
removals, so a removal reaching the engine went unseen.

## Check the double the same way as the code

A double is code under test. Mutate the thing it is meant to detect and confirm
a test goes red. See [mutation-checks.md](mutation-checks.md).

## A fixture that writes through the double changes when the double does

Change what a double's insert keeps, and every test fixture that builds state
through that insert changes too. Nothing fails. The fixture builds a different
row.

HEU-945 made the memory tree store drop a caller's `RemovedAt` on insert, as
Postgres does. Five consumer tests built tombstones by passing
`RemovedAt` to that insert. After the change they would have got active rows.
Their seed helpers now insert the row, then set its removal time.

Before you change what a double's write keeps or checks, find the fixtures that
write through it. The next section is one way to find them.

A search for literal fixtures misses the ones that start from the double's own
output. A test can take a snapshot from the fake, decode it, edit it, and hand
it back. That fixture was never written as a literal, so a grep for literals
does not find it. Search for the decode of the double's type as well.

Found in plan review for HEU-1068 on 2026-11-18. A count of hand-built fake
snapshots covered the literals only. Two load tests decoded a taken snapshot and
edited it. One of them would have been refused by the new restore check.

A fixture that writes below the store's write method can leave out state a
later check reads. The check then refuses on its own. A guard in the code under
test looks pinned when it is not. Seed through the same write the code under
test uses. Each check then sees what production gives it.

Found in final review for HEU-1081 on 2026-11-18.

## Measure a new refusal with a probe that refuses nothing

Before you make a double refuse an input, count who sends it. Copy the tree to
a scratch directory. Make the double log each call it would refuse, with the
test name. Let every call through. Then run the suite.

A probe that refuses undercounts. Each test stops at its first refused call, so
the sites behind it never run.

Measured for HEU-844 on 2026-11-06. A probe that refused every non-UUID id
logged 178 refused calls before the tests stopped. All were on the insert path.
A probe that logged and refused nothing found 101 calls through the four write
methods: 80 inserts, 13 removals and 8 root undos. The refusing probe logged no
removal or undo calls at all.

## Script only replies the worker gives for the payload

A scripted engine reply must be one the worker gives for that payload. A reply
it cannot give describes a refusal that never happens. A scripted reply fires
whether or not the payload would provoke it. The test then cannot show that the
payload provokes the refusal.

When a test needs a particular refusal, give the payload what provokes it. Let
the fake refuse. When the fake does not model that refusal, add the check to the
fake first. Put it in the worker's order. When a test needs any engine error for
a valid payload, use `INTERNAL_ERROR` with the message "handler panicked
unexpectedly". Check the worker's panic reply before copying the message.

Read from the worker for HEU-949 on 2026-11-13. Its dispatch loop gives that
reply for any request whose handler panics.

Dated examples, from HEU-948 on 2026-10-19:

- `TestTreeWriterReject_RejectsAPlacementTheEngineRefusesOnItsFirstDelivery`
  scripted `SPONSOR_NOT_FOUND` for a placement whose sponsor was the root. The
  worker accepted that placement. The test's payload was changed to name a
  sponsor the tree does not hold.
- The `stuckPlacement` fixture and
  `TestTreeWriterReject_AFailedAppendLeavesTheOutcomeEmpty` were changed the
  same day to give the reply the worker gives for an absent sponsor.

A test may script a reply after a transport failure. The failure must be one the
real transport can recover from. A failure that ends the connection cannot be
followed by a reply. Use a plain error that models a transient failure.

Found in plan review for HEU-1067 on 2026-11-18.

## A double that ends the path leaves nothing past it to assert

A double that fails the call ends the code path there. An assertion about
anything the code would do after that call cannot fail. It holds with or
without the code under test.

Put the proof on the double being reached, or not, and on the error returned.
To check that a later step did not run, run the same case through a double that
lets the call through.

Found in review for HEU-959 on 2026-11-14. The nil-ID gate test ran each write
with `refusingLocker`, which fails the test when the lock is taken. It also
asserted an empty engine call log and an unchanged stream. Both held without
the gate, because the write stops at the lock before it calls the engine or
appends. Both assertions were dropped.

## Check the fake against the worker, not against your reading of it

A fake that models the worker's refusals can drift from the worker without any
test failing. Reading the worker's handlers is not enough. The order of the
checks matters, and so does the message text.

Send the same request to the fake and to the real worker binary, and require the
same answer. Make each case one the worker refuses, so an empty answer on both
sides cannot pass. Build the check request and the apply request from one set of
arguments, and require the worker's check to answer what its apply answers.

Give the fake one function per rule, and call it from both the check path and
the apply path. Two copies of a rule drift apart. The worker runs one rule for
both.

Found in final review for HEU-1058 on 2026-11-17. The fake's matrix removal
check refused nothing once a pruning mode was set. Its own removal refused the
root and an absent user, as the worker's check does. Agreement cases now pin
it.

## Pin a restore refusal against an edited worker snapshot

A fake's snapshot format can differ from the worker's. Then the same bad shape
cannot be sent to both as one payload. Build it twice instead.

Take a real snapshot from the worker. Decode it with unknown fields refused, so
a field the test does not model fails the decode. Edit it into the shape, and
restore it under a fresh name. Restoring under the source's own name can be
refused before the check you meant to reach. Then build the same shape in the
fake's format and restore that.

Require the worker's code and a regex anchored at both ends on its message. An
edit that trips an earlier check then fails the pin rather than passing for the
wrong reason. Require the fake's code and its own message separately.

Before any pin uses the edit helpers, run one test that decodes a real worker
snapshot and restores the re-encoded result unchanged. That catches a layout the
helpers do not model.

Used for HEU-1068 on 2026-11-18, for the writer fake's restore checks.

## Pin a check's order with a shape two checks refuse

A fake that refuses the right shapes can still run its checks in a different
order from the worker. Then a snapshot with two faults gets a different answer
from each engine. No test fails.

To pin that check A runs before check B, build a shape that both refuse. Require
the worker's answer for A from both engines. Edit only what A needs and leave
B's fault in place.

This row passes before and after the change it guards. Its evidence is a
mutation: move A past B and watch the row fail. Record that run in the commit
body. No test-first failure exists for it.

An order row does not show that B exists. Delete B and the row still gets A's
answer from both engines. Pair it with a row that only B refuses. HEU-1086 found
this on 2026-11-23. Its two order rows passed with the root check deleted, and
only the lone-root row failed.

Pin only the boundaries the worker's order makes observable. Do not pin the
fake's own layout. An example is which of two loops runs first inside one worker
check. Such a row breaks on a refactor that changes nothing the worker would
answer.

Used for HEU-1078 on 2026-11-18, for the writer fake's root-sponsor and
sponsor-chain checks.

## Two offenders on one restore check need the setup's placement order

When two nodes fail the same restore check, and that check walks the arena slot
by slot, the worker names the node in the earlier slot. The writer fake has no
slots. It names the one with the
lower user ID. The two agree only while slot order matches user-ID order.

The fake keeps user-ID order. Matching the worker would mean copying its slot
allocation into the fake, including how it reuses a freed slot, on every path
that adds or removes a node. That is a second place for the fake to drift, and
the two messages would still differ in form. Decided for HEU-1087 on
2026-11-23.

So the restore agreement rows rely on a setup that places users in user-ID
order. The refusal test checks this before it edits each worker snapshot. It
fails when a live slot holds a lower user ID than a live slot before it, or when
a freed slot comes before a live one. Its message lists the user ID in each
slot.

A shape whose slot order differs from its user-ID order is not supported.
HEU-1087's shape is one. It places …0003 before …0002 and puts both at a bad
depth. The worker names …0003 and the fake names …0002. Where a row can be
reduced to one offender, reduce it. A sponsor cycle cannot be, because a walk
from any node on it fails. Nor can a parent at the largest depth, because that
parent fails the depth check too.

Order across faults on different nodes matters as well. The worker checks every
edge of one node before it moves to the next. The fake does the same in one
loop. A fake that checked every parent edge before any sponsor edge named a
later node's bad parent ahead of an earlier node's bad sponsor. HEU-1120 found
this on 2026-11-23.

## Pin a rule with shapes that separate the wrong rules

One shape pins one answer. Several wrong rules can give that same answer, so the
shape passes for all of them.

Write down the wrong rules a rewrite could plausibly use. Check each shape
against each of them. Add shapes until every wrong rule fails at least one.

A tank that lists two users twice shows it. The worker names the user whose
second entry comes first. On A, B, B, A it names B. A scan from the end also
names B. On A, B, A, B it names A. A count-first scan also names A. That scan
names the first user in tank order with more than one entry. Each shape alone
let one wrong rule through. The pair caught both.

A count works the same way. A shape where two rules give the same count does not
pin the count. Add a shape where they differ.

A mutation that survives and changes an answer means a shape is missing. It is
not a reason to drop the mutation. A mutation that changes no answer for any
input needs no shape.

Found in final review for HEU-1076 on 2026-11-18.

Used for HEU-1083 on 2026-11-19, for the writer fake's subtree removals in both
pruning modes.

## Take an agreement row's expected value from the worker

An agreement row holds a literal the worker must return. Write that literal from
your reading of the worker and the row can pin a wrong answer. The fake then
copies the same mistake and both sides pass.

Read the value from the worker first. Add a throwaway probe test through
`go test -overlay` that builds the row's layout on the worker and prints the
result. Hard-code what it printed. The row's worker-side assertion then checks
the literal on every run.

Found in review for HEU-1083 on 2026-11-20. Each row added during review took
its value from the worker.

## Check a generated sweep against the mutants it is for

A sweep that passes on the real fake says nothing about its bounds. Its draws
can miss the shapes a wrong rule needs. Then the sweep passes on that wrong rule
too.

Keep the mutants that got past the hand-written rows. Run each one against the
sweep through `go test -overlay`. Count the seeds each one fails, not only
whether any did. Require every mutant to fail at least 5 seeds of the fixed
range CI runs. One failing seed is a margin a small change to the generator can
lose.

Bias the draws toward the shapes those mutants need. Keep a share of the seeds
unbiased. A generator tuned only to the mutants you know cannot find the next
one.

Guard each bias with a floor. Count the cases the bias exists to produce. Fail
the run when that count falls below the floor. A drifted bias can leave a mutant
just at the bar and every other floor passing. On HEU-1090, one run turned the
most-children bias off. In that run, the mutant whose free-slot search skipped
repositioned siblings' subtrees failed exactly 5 seeds. The non-leaf floor still
passed.

Choose each step from the real engine's state, never the fake's. A mutated fake
then gets the same cases as the real one. Its counts can be compared.

Used for HEU-1090 on 2026-11-23. The HEU-1083 sweep caught its weakest mutant on
2 seeds in 300. Aiming removals at the nodes with the most children raised that
to 12 in 200. One seed in four stays unbiased.

## A lenient decode in a probe hides a shape difference

A probe can decode a snapshot into a struct and compare by length or printed
value. That comparison cannot tell a missing key from an empty list. The two
engines can write different shapes and the probe still reports agreement.

When the shape matters, compare the raw keys as well as the decoded values.

Found in final review for HEU-1083 on 2026-11-20. A generated sweep compared the
fake's holding tank with the worker's this way. It missed that the fake leaves
the key out where the worker writes an empty list. Filed as HEU-1092.

## Mirror checked arithmetic with an explicit guard

The worker can add with an overflow check that refuses at the top of the range.
Go's fixed-width addition wraps to 0 instead. A fake that copies the comparison
without the check accepts a value the worker refuses.

Guard the maximum before adding. Then pin the guard with a row where the wrap
changes the fake's answer. The value at the maximum is often refused by another
rule anyway. Then the guard only decides which node gets named. Build the row so
that the node checked first sits below the maximum. Require the worker's named
node from both engines.

Found for HEU-1082 on 2026-11-19. The fake's depth check let a depth-0 child
pass under a parent at the largest depth. It then named the parent instead of
the child.
