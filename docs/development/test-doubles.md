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

`MemoryTreeStore` ignores `context.Context` entirely. `PostgresTreeStore`
honours it, because a pool does. A double wrapping the memory store therefore
cannot see a cancellation, whatever the test is called.

A test asserting that a compensating write survives a cancelled context passed
against an implementation with no shield at all, because the double it embedded
could not tell the two apart.

When a double stands in for the production implementation, it has to model the
behaviour under test even where the embedded type omits it:

```go
func (c *deleteRecordingStore) DeleteNode(ctx context.Context, treeID, userID string) error {
    if err := ctx.Err(); err != nil {
        return err
    }
    ...
}
```

Before embedding, ask what the embedded type does not do that production does.
Where the answer is filed as a ticket rather than fixed, the double is where it
bites.

## Prefer a failure to a zero value

A double that returns a zero value for an unconfigured case lets a test pass
while comparing against state nothing ever held. Return an error instead, so an
unconfigured case fails loudly rather than agreeing with whatever was expected.

## Check the double the same way as the code

A double is code under test. Mutate the thing it is meant to detect and confirm
a test goes red. See [mutation-checks.md](mutation-checks.md).
