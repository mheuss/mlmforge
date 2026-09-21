# Postgres Stores

How to write a Go store against Postgres in this codebase. Every entry below
cost real debugging on HEU-555 and is invisible until it bites.

For the store *shape* — one interface, two implementations, a shared suite —
see UC-NET-008 in [`../use-cases/network-engine.md`](../use-cases/network-engine.md).

## pgx sends only a nil slice as SQL NULL

An empty non-nil `[]byte` or `json.RawMessage` is sent as an empty payload, and
a `jsonb` column rejects it with `22P02 invalid input syntax for type json`.

```go
// Fails against Postgres, passes against an in-memory store.
pool.Exec(ctx, sql, id, json.RawMessage{})
```

Normalize `len(x) == 0` to nil before the write. Do it in one helper both
implementations call, or the two disagree on a value the shared suite compares.

## jsonb does not preserve key order

Postgres parses `jsonb` to a binary form and re-emits keys sorted by length,
then bytewise. Whitespace goes too.

```
wrote: {"v":1,"source_id":"...","level":3}
read:  {"v": 1, "level": 3, "source_id": "..."}
```

So golden fixtures pin the bytes your code hands the driver, not the bytes in
the table. **Never byte-compare a fixture against a `SELECT`.** Parse and
compare values. This also means an in-memory store returns different bytes than
Postgres for the same write — assert on parsed values in any shared suite.

Numbers survive exactly: `jsonb` stores them as `numeric`, so a float64 written
into a JSON field reads back bit-identical even though the text may be
reformatted (`1e21` becomes `1000000000000000000000`).

## Go's json decoder is not Postgres's jsonb parser

Four known divergences. None is theoretical; all were reproduced.

| Input | Go | Postgres |
|---|---|---|
| `\u0000` escape in a string | accepts | rejects — text cannot hold NUL |
| unpaired surrogate (`\ud800`) | accepts, substitutes U+FFFD | rejects |
| raw invalid UTF-8 byte | accepts, substitutes U+FFFD | rejects at the encoding layer |
| number beyond float64 (`1e400`) | **rejects** | accepts as `numeric` |

The last one reverses: Go is stricter. Fix it with `json.Decoder` +
`UseNumber()`, which keeps the literal text instead of parsing to float64.
Catch raw invalid UTF-8 with `utf8.Valid`. The two escape-sequence cases are
not worth reimplementing jsonb's parser for — document them instead.

## `json.Decoder.More()` is not a trailing-data check

`More` exists for streaming the elements of a container, so it returns false on
`]` and `}`. At top level that means `{}}` and `{}]` read as clean
end-of-input, while `json.Unmarshal` rejects both.

```go
// Wrong — accepts `{}}`.
if d.More() { return errTrailing }

// Right.
if _, err := d.Token(); err != io.EOF { return errTrailing }
```

## NUMERIC accepts Infinity since Postgres 14

`CHECK (col <> 'NaN'::numeric)` does not make a column finite. `Infinity`,
`-Infinity`, and `+Inf` all insert — and `+Inf` is exactly what
`strconv.FormatFloat(math.Inf(1), 'f', -1, 64)` emits, so a Go float64 reaches
it through the ordinary text path.

```sql
CHECK (col > '-Infinity'::numeric AND col < 'Infinity'::numeric)
```

That covers NaN too, since NaN sorts above Infinity in `numeric` ordering. Note
the `'Infinity'::numeric` literal requires Postgres 14 or newer.

`strconv.ParseFloat` accepts `"NaN"` and `"Infinity"` on the way back, so guard
the read as well as the write.

## A constraint violation inside a transaction aborts it

After a `23505`, the transaction is in an aborted state and the next statement
fails with `25P02 current transaction is aborted`. So this does not work:

```go
tx.Begin()
tx.Exec(insert)          // 23505
tx.QueryRow(findWinner)  // 25P02 — cannot answer
```

Run a conflict-detecting insert on the pool, where each statement is its own
transaction and the follow-up lookup runs clean. Inside a transaction you need
`ON CONFLICT DO NOTHING` or a SAVEPOINT instead.

Detect the specific conflict on `pgconn.PgError.ConstraintName`, not on
SQLSTATE — `23505` covers every unique violation on the table, including the
primary key.

## Typed-error contracts can depend on the isolation level

Under READ COMMITTED, a `SELECT ... FOR UPDATE` that loses a race re-reads the
row after the winner commits (EvalPlanQual) and sees the new state. Under
REPEATABLE READ the same statement raises `40001 could not serialize access`.

If your typed error comes from inspecting that re-read, the contract silently
depends on the session default. Pin it:

```go
tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
```

Otherwise someone hardening the production pool to REPEATABLE READ breaks the
error contract with every test still green.

## `now()` is the transaction timestamp

`now()` is `transaction_timestamp()`, captured at `BEGIN` — before the
statement can block on a row lock. Behind a held lock it can be seconds early,
and a column default of `now()` has the same problem.

For an audit column, use `clock_timestamp()`. Measured on HEU-555: a
replacement run's `started_at` landed 1.95s *before* the run it superseded was
voided, making the lifecycle read backwards.

## A same-period foreign key is expressible

"This column must reference a row sharing my `period_id`" looks like it needs a
trigger. It does not:

```sql
UNIQUE (id, period_id),
FOREIGN KEY (other_id, period_id) REFERENCES t (id, period_id)
```

The extra `UNIQUE` is redundant with the primary key on its own — that is the
point, it gives the pair something to reference. Since `id` is the PK, exactly
one row has that id, so requiring the pair to exist forces its `period_id` to
match the referencing row's.

## Sampling live-heap peak measures GC pacing, not your code

Polling `/memory/classes/heap/objects:bytes` during an operation, even against
a post-fixture baseline, reports roughly `liveHeap × GOGC/100`. The collector
lets the heap grow that far before running, so the number tracks the *caller's*
data and barely moves when the operation's allocation changes.

Measured: varying allocation volume 15x moved the figure under 3%; varying
`GOGC` moved it proportionally (50 → 48 MB, 100 → 94 MB, 400 → 366 MB).

To measure what an operation actually holds, suppress pacing for the duration
(`debug.SetGCPercent(1)`). Wall clock is then meaningless — near-continuous GC
inflates it several-fold — so time and live heap cannot be measured in one
pass. For allocation *volume*, `runtime.MemStats.TotalAlloc` is deterministic
and needs no sampling.

## TEXT cannot store a NUL byte

Postgres rejects it with `invalid byte sequence for encoding "UTF8": 0x00`, as
it does any invalid UTF-8. If a string reaching a `TEXT` column comes from
config or user input, validate it in Go — otherwise an in-memory store accepts
a value the real one cannot write.

```go
if !utf8.ValidString(s) || strings.ContainsRune(s, 0) { ... }
```

## A NOT NULL column does not make a branch unreachable

Every store interface here has two implementations. Postgres carries the
constraint. The in-memory one does not.

HEU-562 deleted a guard on an empty `user_id` in the tree loader, reasoning that
the column is `UUID NOT NULL` so no row can hold one. That reasoning checked one
implementation. The in-memory store has no such constraint, `validateNodes`
never checks for it, and HEU-565 already records an empty user ID reaching the
engine on the success path. The guard went back in, with a test that failed
before the fix.

**A column definition is evidence about one implementation, not about the
program.**

Before removing a guard on the strength of a constraint, check the other store,
the validation layer, and any path that builds the struct directly.

This is the same seam as "TEXT cannot store a NUL byte" above, approached from
the other side. That entry is about writing a value Postgres will reject. This
one is about trusting Postgres to have rejected it already.

## The test count in networkengine depends on the machine

Sites in `internal/networkengine` skip when no Postgres container is running.
They cover the store pairs, commission schema and amounts, qualification
history, and tree persistence. Several sit in shared helpers rather than in the
tests themselves, so one site can skip many tests.

Without the container the package passes with all of them skipped. It prints
`ok` either way.

The container is not the only gate. The Rust worker binary at
`engine/target/debug/network-engine-worker` is a second one. The tree
persistence integration tests are among the sites that gate on it. A run with
the container up and the worker unbuilt still skips and still prints `ok`. Build
it with `cargo build --workspace` in `engine/`.

A missing binary skips only outside CI. With `CI` set it fails instead
(HEU-660). A green CI run says nothing about whether your own run covered those
tests. A binary older than its sources fails either way (HEU-615). The same is
true for any stat error other than "not found".

**Report the package as ok with zero failures, how many skipped, and which
gates were open.**

A bare pass count is true on every machine and means something different on
each. A bare skip count is now ambiguous too. The two gates cover overlapping sets.
The same total can come from either. Naming the gates is
what tells a reader which run they are looking at.

Say which unit the number is in. Verbose output prints a line per level. A
parent and its subtests each contribute. A count of skip lines is not a count of
tests.

```
go test ./internal/networkengine/ -v -count=1 2>&1 | grep -cE '^ *--- SKIP'
```
