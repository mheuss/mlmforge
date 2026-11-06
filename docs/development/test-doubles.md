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

## Measure a new refusal with a probe that refuses nothing

Before you make a double refuse an input, count who sends it. Copy the tree to
a scratch directory, make the double log each call it would refuse along with
the test name, and let every call through. Then run the suite.

A probe that refuses undercounts. Each test stops at its first refused call, so
the sites behind it never run.

Measured for HEU-844 on 2026-11-06. A probe that refused every non-UUID id
logged 178 refused calls, all on the insert path, before the tests stopped. A
probe that logged and refused nothing found 101 calls through the four write
methods: 80 inserts, 13 removals and 8 root undos. The refusing probe logged no
removal or undo calls at all.
