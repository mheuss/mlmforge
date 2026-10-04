# Database connection strings a driver refuses

What each database driver does with a connection string it cannot use, and where that text can carry the password.

`mlmforge` hands one connection string to three drivers. `migrate` uses golang-migrate, which hands it to lib/pq. `tree` uses pgx. Each one refuses a bad string in its own way, and each has printed part of the password while doing it (HEU-867).

Every fact below names the library version it was read at, and the test that pins it. A fact no test pins says so. After a driver upgrade, run the pinning tests first. A fact marked "not pinned" has to be re-read in the new source.

## What `mlmforge` does about it

`internal/platform` defines `ConnStringError`. It names the driver and a stage. For stage `raw-at` it also names a part. It holds nothing from the string and has no `Unwrap`. Every refused string at the three call sites comes back as one.

Before either command opens a driver, `resolveDBURL` runs `PreDriverError` on the raw string. It runs five checks in this order and returns the first refusal, with driver `mlmforge`. The protection stops at that CLI entry. Callers of the lower layers are not covered.

- Stage `scheme-case`: the string starts with `postgres://` or `postgresql://` only when case is ignored. pgx reads such a string as keyword/value text.
- Stage `keyword-key`: the string has no exact lowercase `postgres://` or `postgresql://` prefix, and its first keyword key holds an ASCII character outside `A-Z a-z 0-9 _ . $`. pgx would send that key to the server as a parameter name, and the server would print it. Bytes at or above `0x80` pass, because Postgres 16 accepts them in a dotted parameter name. The class was measured against Postgres 16 on 2026-10-04, and no test pins the server's side. After a Postgres upgrade, re-run that probe on `tree load` against a scratch server, with keyword strings whose first key is `my-key`, `a.b-c`, `a.b:c`, `é.x`, `a.é` and `a.b$c`. The first three must get a FATAL parameter error, and the last three must connect. If any result differs, this stage's class is wrong for that edge.
- Stage `raw-at`, with a part: a lowercase `postgres://` or `postgresql://` string has a raw `@` in its path, query or fragment. A literal `@` there has to be written `%40`.
- Stage `after-password`: a lowercase URL's query has any `&` after the start of its `password` segment, including an empty segment. A key that reads `password` once surrounding whitespace is trimmed counts too, because lib/pq trims it and reads it as the password. Put `password` last, or move the password into the userinfo. A literal `&` in the password has to be written `%26`.
- Stage `raw-plus`: a lowercase URL's query `password` value holds a raw `+`, which Go's query decoding reads as a space. The key is matched the same way, after trimming whitespace. A literal `+` has to be written `%2B`, and a space `%20`.

Then each call site does its own mapping.

- pgx, at `openTreeDeps` and `PostgresTreeLocker.Lock`: `PgxConnStringError` replaces a `*pgconn.ParseConfigError`.
- golang-migrate, at `openMigration`, in this order:
  1. A string without an exact `postgres://` or `postgresql://` prefix is refused as stage `scheme`.
  2. lib/pq's own parse runs on the string golang-migrate would hand it. A refusal becomes stage `refused`. If lib/pq also refuses a neutral probe string, the environment is at fault, and lib/pq's own error is returned instead.
  3. A `*url.Error` from `database.Open` becomes stage `parse`.

A raw `@` alone in a password does not split the string. A raw `/`, `?` or `#` in a password does. It puts the userinfo's closing `@` after the host part. The `raw-at` stage refuses that shape (HEU-875).

A raw `&` in a `?password=` value splits it. Each piece after the `&` becomes its own query key, and the server prints the key. A raw `+` in it is read as a space. The `after-password` and `raw-plus` stages refuse those shapes (HEU-877, HEU-878). A string that is not a URL but holds one, after a stray space, quote or BOM, is refused by `keyword-key` (HEU-880).

## net/url, Go 1.27

- It cuts at the first `#`, then at the first `?`. It ends the host part at the first `/`. Inside the host part it splits the userinfo at the last `@`. It accepts a raw `@` there.
  Pinned by `TestDriverReadings_SplitWhereRecorded`, which reads the split through pgx and lib/pq.

## pgx v5.9.2

- Every parse refusal is a `*pgconn.ParseConfigError`. That includes pool options such as `pool_max_conns=abc`.
  Pinned by `TestPgxConnStringError_ReplacesEachRefusal`, which builds each refusal with `pgxpool.ParseConfig`.
- Its `ConnString` field holds the raw string. Anything that walks the error chain can read it back.
  Read in source, not pinned. The fix keeps the type out of the chain, which `RequireNoDriverParseError` pins at every site.
- It masks a userinfo password in its message, but its reason text can quote part of it. A `/` in the password gave `invalid port ":Zm9vQmFy" after host`. A `?password=` value was printed whole.
  Observed on the CLI, 2026-09-30. Not pinned, because the fix withholds the text.
- When `url.Parse` fails, it keeps only the inner error and drops the `*url.Error`. So a URL parse failure cannot be recognised by type.
  Read in source, not pinned. `pgxStage` re-parses the rejected string instead, and the `parse` rows of `TestPgxConnStringError_ReplacesEachRefusal` pin that.
- It accepts `password=… host=::1 dbname=app` and the two lib/pq query-key strings.
  Pinned by the no-pgx-stage rows of `TestPgxConnStringError_ReplacesEachRefusal`.
- It reads a string whose scheme is not lowercase `postgres://` or `postgresql://` as keyword/value text. With an `=` in it, the text before the first `=` becomes a runtime parameter name.
  Pinned by `TestDriverReadings_PgxReadsAMisCasedSchemeAsKeywordValueText`, which checks the parsed runtime parameters. Observed against Postgres 16 on 2026-10-01: pgx sent the name to the server. The server echoed it in `unrecognized configuration parameter`. That part is not pinned.

## golang-migrate v4.19.1

- An unparseable URL comes back as a `*url.Error` with `Op` `parse`.
  Pinned by the `slash`, `bad-escape` and `fragment` rows of `TestMigrateVersion_ARefusedConnStringHoldsNoPassword`.
- That error's text quotes the URL. Go cuts the URL at `#`, and the part before the cut still holds the password.
  Observed on the CLI, 2026-09-30. Not pinned, because the fix withholds the text.
- The driver name is the text before the first `:`, matched exactly. For an unknown name, the error quotes that text. A keyword-form string with a `:` after the password printed the password.
  Read in source, not pinned. The scheme check refuses these strings first.
- It hands lib/pq `migrate.FilterCustomQuery(purl).String()`.
  Read in source, not pinned. `migrateDriverParseError` builds the same string.

## lib/pq v1.10.9

- `ParseURL` writes query keys raw and quotes only values. A key holding `='` or whitespace shifts the keyword text. `parseOpts` then fails with a plain error that quotes it. The `pq-quoted-key` case gave `missing "=" after "pq3cretpwXYZ'" in connection info string"`.
  Pinned by `TestMigrateDriverParseError_RefusesWhatLibPQCannotParse`.
- A string without `//` is parsed as keyword text. An opaque `postgres:user:pw@…` printed whole.
  Read in source, not pinned. The scheme check refuses it first. A live-server check against Postgres 16 on 2026-09-30 found the server rejects such a string's first key as an unknown or invalid configuration parameter.
- `NewConnector` parses the string before it checks `client_encoding` and `datestyle`, and the string's own values override the environment. So `PGCLIENTENCODING=LATIN1` with `?client_encoding=UTF8` still connects.
  Pinned by the override rows of `TestMigrateVersion_AStringThatReachedTheDriverStillDoes`, and by `TestMigrateVersion_ARefusedEnvironmentWithARefusedStringHoldsNoPassword`.
- An authority port and a query `port` both reach the keyword text. The entries are sorted, and the later one wins. So the port lib/pq dials depends on how the two values sort.
  Pinned by the query-port rows of `TestMigrateVersion_AStringThatReachedTheDriverStillDoes`.
- It drops a key with an empty value. `connect_timeout=` never reaches the parser.
  Pinned by the `empty-timeout` row of `TestMigrateVersion_AStringMigrateAcceptsReachesTheDialWithNoPassword`.
- It panics when any of `PGHOSTADDR`, `PGSERVICE`, `PGSERVICEFILE`, `PGREALM`, `PGREQUIRESSL`, `PGSSLCRL`, `PGREQUIREPEER`, `PGKRBSRVNAME`, `PGGSSLIB`, `PGSYSCONFDIR` or `PGLOCALEDIR` is set. HEU-862 covers the service variables.
  Read in source, not pinned. A probe hit the `PGSYSCONFDIR` panic on 2026-10-01.
- `testutil.IsolatePgxEnv` sets `PGSERVICEFILE` and `PGSYSCONFDIR`, so a test that calls lib/pq must not run under it. `testutil.ClearTimeoutEnv` unsets `PGSERVICE` and `PGSERVICEFILE`. No helper unsets the other nine panic variables.
- It returns `ErrCouldNotDetectUsername` when no user is given and none can be found. That error holds no part of the string, so `migrateDriverParseError` passes it through.
  Read in source, not pinned. No test can make the OS user lookup fail.
