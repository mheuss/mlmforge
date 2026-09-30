# Database connection strings a driver refuses

What each database driver does with a connection string it cannot use, and where that text can carry the password.

`mlmforge` hands one connection string to three drivers. `migrate` uses golang-migrate, which hands it to lib/pq. `tree` uses pgx. Each one refuses a bad string in its own way, and each has printed part of the password while doing it (HEU-867).

Every fact below names the library version it was read at, and the test that pins it. A fact no test pins says so. After a driver upgrade, run the pinning tests first. A fact marked "not pinned" has to be re-read in the new source.

## What `mlmforge` does about it

`internal/platform` defines `ConnStringError`. It names the driver and a stage, holds nothing from the string, and has no `Unwrap`. Every refused string at the three call sites comes back as one.

- pgx, at `openTreeDeps` and `PostgresTreeLocker.Lock`: `PgxConnStringError` replaces a `*pgconn.ParseConfigError`.
- golang-migrate, at `openMigration`, in this order:
  1. A string without an exact `postgres://` or `postgresql://` prefix is refused as stage `scheme`.
  2. lib/pq's own parse runs on the string golang-migrate would hand it. A refusal becomes stage `refused`. If lib/pq also refuses a neutral probe string, the environment is at fault, and lib/pq's own error is returned instead.
  3. A `*url.Error` from `database.Open` becomes stage `parse`.

The open gap is HEU-875. A password with an unencoded `/`, `#`, `?` or `@` can split a string that a driver still accepts. A later dial, DNS, connect or option error then prints the split-off part.

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

## golang-migrate v4.19.1

- An unparseable URL comes back as a `*url.Error` with `Op` `parse`. Its text quotes the URL. Go cuts the URL at `#`, and the part before the cut still holds the password.
  Pinned by the `slash`, `bad-escape` and `fragment` rows of `TestMigrateVersion_ARefusedConnStringHoldsNoPassword`.
- The driver name is the text before the first `:`, matched exactly. For an unknown name, the error quotes that text. A keyword-form string with a `:` after the password printed the password.
  Read in source, not pinned. The scheme check refuses these strings first.
- It hands lib/pq `migrate.FilterCustomQuery(purl).String()`.
  Read in source, not pinned. `migrateDriverParseError` builds the same string.

## lib/pq v1.10.9

- `ParseURL` writes query keys raw and quotes only values. A key holding `='` or whitespace shifts the keyword text. `parseOpts` then fails with a plain error that quotes it, for example `missing "=" after "s3cretPWxyz'"`.
  Pinned by `TestMigrateDriverParseError_RefusesWhatLibPQCannotParse`.
- A string without `//` is parsed as keyword text. An opaque `postgres:user:pw@…` printed whole.
  Read in source, not pinned. The scheme check refuses it first. A live-server check against Postgres 16 on 2026-09-30 found the server rejects such a string's first key as an unknown or invalid configuration parameter.
- `NewConnector` parses the string before it checks `client_encoding` and `datestyle`, and the string's own values override the environment. So `PGCLIENTENCODING=LATIN1` with `?client_encoding=UTF8` still connects.
  Pinned by the override rows of `TestMigrateVersion_AStringThatReachedTheDriverStillDoes`, and by `TestMigrateVersion_ARefusedEnvironmentWithARefusedStringHoldsNoPassword`.
- An authority port and a query `port` both reach the keyword text. The entries are sorted, and the later one wins. So the port lib/pq dials depends on how the two values sort.
  Pinned by the query-port rows of `TestMigrateVersion_AStringThatReachedTheDriverStillDoes`.
- It drops a key with an empty value. `connect_timeout=` never reaches the parser.
  Pinned by the `empty-timeout` row of `TestMigrateVersion_AStringMigrateAcceptsReachesTheDialWithNoPassword`.
- It panics when `PGSERVICE`, `PGSERVICEFILE`, `PGREALM` or `PGHOSTADDR` is set. That is HEU-862.
  Read in source and observed on the CLI. Not pinned.
- It returns `ErrCouldNotDetectUsername` when no user is given and none can be found. That error holds no part of the string, so `migrateDriverParseError` passes it through.
  Read in source, not pinned. No test can make the OS user lookup fail.
