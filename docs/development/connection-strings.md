# Database connection strings a driver refuses

What each database driver does with a connection string it cannot use, and where that text can carry the password.

`mlmforge` hands one connection string to three drivers. `migrate` uses golang-migrate, which hands it to lib/pq. `tree` uses pgx. Each one refuses a bad string in its own way, and each has printed part of the password while doing it (HEU-867).

Every fact below names the library version it was read at, and the test that pins it. A fact no test pins says so. After a driver upgrade, run the pinning tests first. A fact marked "not pinned" has to be re-read in the new source.

## What `mlmforge` does about it

`internal/platform` defines `ConnStringError`. It names the driver and a stage. For stage `raw-at` it also names a part. It holds nothing from the string and has no `Unwrap`. Every refused string at the three call sites comes back as one.

Before either command opens a driver, `resolveDBURL` runs `PreDriverError` on the raw string. It runs these checks in this order and returns the first refusal, with driver `mlmforge`. The protection stops at that CLI entry. Callers of the lower layers are not covered.

- Stage `scheme-case`: the string starts with `postgres://` or `postgresql://` only when case is ignored. pgx reads such a string as keyword/value text.
- Stage `keyword-key`: the string has no exact lowercase `postgres://` or `postgresql://` prefix, and its first keyword key holds an ASCII character outside `A-Z a-z 0-9 _ . $`. pgx would send that key to the server as a parameter name, and the server would print it. Bytes at or above `0x80` pass, because Postgres 16 accepts them in a dotted parameter name. A string with no `=` passes this check, and pgx refuses it at parse instead. Read in pgx source, not pinned. A first key starting with exactly `_pq_.` also passes, because the server takes such a name as a protocol option and connects.
- Stage `raw-at`, with a part: a lowercase `postgres://` or `postgresql://` string has a raw `@` in its path, query or fragment. A literal `@` there has to be written `%40`.
- Stage `after-password`: a lowercase URL's query has any `&` after the start of its `password` segment, including an empty segment. A key that reads `password` in any letter case once surrounding whitespace is trimmed counts too. lib/pq trims a lowercase one and reads it as the password. A case variant is not the password to either driver, but the pieces after it still reach the server. Put `password` last, or move the password into the userinfo. A literal `&` in the password has to be written `%26`.
- Stage `raw-plus`: a lowercase URL's query `password` value holds a raw `+`, which Go's query decoding reads as a space. The key is matched the same way, after trimming whitespace and in any letter case. A literal `+` has to be written `%2B`, and a space `%20`.

The `keyword-key` class was measured against a Postgres 16 server on 2026-10-04, and no test pins the server's side. After a Postgres upgrade, re-run that probe on `tree load` against a scratch server. Use keyword strings whose first key is `my-key`, `a.b-c`, `a.b:c`, `_PQ_.a-b`, `é.x`, `a.é`, `a.b$c` and `_pq_.a-b`. The first four must get a FATAL parameter error. The last four must connect. If any result differs, the class is wrong for that edge.

Then each call site does its own mapping.

- pgx, at `openTreeDeps` and `PostgresTreeLocker.Lock`: `PgxConnStringError` replaces a `*pgconn.ParseConfigError`.
- golang-migrate, at `openMigration`, in this order:
  1. The environment is checked before the string. If any of the eleven variables lib/pq panics on is set, even to an empty value, `openMigration` returns `UnsupportedEnvError`. It names each variable that is set and never its value (HEU-862). Pinned by `TestMigrateCommands_RefuseAnUnsupportedVariableBeforeOpeningAnything` and `TestMigrateVersion_RefusesTheEnvironmentBeforeOpeningTheSource`. The empty value is pinned by `TestRefuseUnsupportedEnv_AnEmptyValueCounts`, `TestRefuseUnsupportedEnv_NamesEveryVariableSetInListOrder` and `TestMigrateVersion_NamesEveryUnsupportedVariableAndNoValue`.
  2. A string without an exact `postgres://` or `postgresql://` prefix is refused as stage `scheme`.
  3. lib/pq's own parse runs on the URL with golang-migrate's `x-` settings removed. A refusal becomes stage `refused`. If lib/pq also refuses a neutral probe string, the environment is at fault, and lib/pq's own error is returned instead.
  4. A `*url.Error` from parsing the URL in `dialSession` becomes stage `parse`.

A raw `@` alone in a password does not split the string. A raw `/`, `?` or `#` in a password does. It puts the userinfo's closing `@` after the host part. The `raw-at` stage refuses that shape (HEU-875).

A raw `&` in a `?password=` value splits it. Each piece after the `&` becomes its own query key, and the server prints the key. A raw `+` in it is read as a space. The `after-password` and `raw-plus` stages refuse those shapes (HEU-877, HEU-878). A URL can follow a stray space, quote or BOM. When the string also holds an `=`, `keyword-key` refuses it (HEU-880). Without an `=`, pgx refuses it at parse.

## net/url, Go 1.27

- It cuts at the first `#`, then at the first `?`. It ends the host part at the first `/`. Inside the host part it splits the userinfo at the last `@`. It accepts a raw `@` there.
  Pinned by `TestDriverReadings_SplitWhereRecorded`, which reads the split through pgx and lib/pq.
- Its query decoding reads `+` in a value as a space, decodes keys as well as values, and skips empty segments. So `?password=x&` reads the password as `x`.
  Pinned by `TestDriverReadings_ARawAmpersandSplitsAQueryPassword`, `TestDriverReadings_QueryKeysAreDecodedAndTheirOrderIsIgnored` and `TestDriverReadings_APlusIsASpaceInTheQueryAndLiteralInTheUserinfo`.

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
- It reads any string without an exact `postgres://` or `postgresql://` prefix as keyword/value text. So a leading space, tab, quote or BOM makes the whole URL before its first `=` a runtime parameter name.
  Pinned by `TestDriverReadings_PgxSendsANonURLFirstKeyAsAParameterName`. Not pinned, observed on a live Postgres 16 server, 2026-10-04: the server printed that name, once with the whole password in it. The outputs are recorded on HEU-880.
- It sends every query key that is not one of its own settings as a startup parameter, even with an empty value. It does not trim or fold the case of a key, so `+password` and `PASSWORD` are parameters, not the password. Query key order does not change its reading.
  Pinned by `TestDriverReadings_ARawAmpersandSplitsAQueryPassword`, `TestDriverReadings_QueryKeysAreDecodedAndTheirOrderIsIgnored` and `TestDriverReadings_LibPQTrimsSpaceAroundAKeyAndNeitherDriverFoldsCase`. Not pinned, observed on a live Postgres 16 server, 2026-10-04: the server printed such a key in `unrecognized configuration parameter`. The outputs are recorded on HEU-877.

## golang-migrate v4.19.1

- An unparseable URL comes back as a `*url.Error` with `Op` `parse`.
  Pinned by the `slash`, `bad-escape` and `fragment` rows of `TestMigrateVersion_ARefusedConnStringHoldsNoPassword`.
- That error's text quotes the URL. Go cuts the URL at `#`, and the part before the cut still holds the password.
  Observed on the CLI, 2026-09-30. Not pinned, because the fix withholds the text.
- The driver name is the text before the first `:`, matched exactly. For an unknown name, the error quotes that text. A keyword-form string with a `:` after the password printed the password.
  Read in source, not pinned. The scheme check refuses these strings first.
- mlmforge no longer calls golang-migrate's `database.Open`. `dialSession` hands lib/pq `migrate.FilterCustomQuery(purl)` with `fallback_application_name=mlmforge-migrate` added when the URL sets no `fallback_application_name`, or an empty one, and `options` led by `-c client_connection_check_interval=1000`. `migrateDriverParseError` builds the string without those additions.

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
  The same drop applies to a piece split off a query password, which is why a bare piece after a raw `&` leaks on `tree` but not on `migrate`. Pinned by `TestDriverReadings_ARawAmpersandSplitsAQueryPassword`.
- It sends a split-off piece that has a value, such as `cXV4=eHl6`, as a startup parameter.
  Pinned by `TestDriverReadings_ARawAmpersandSplitsAQueryPassword`. Not pinned, observed on a live Postgres 16 server, 2026-10-04: `migrate` printed `pq: unrecognized configuration parameter` with the piece's key. The outputs are recorded on HEU-877.
- Its keyword parser skips `unicode.IsSpace` around a key, in `scanner.SkipSpaces` and `parseOpts`. So `+password`, `%09password` and `%C2%A0password` all become its password. It does not fold case, so `PASSWORD` does not.
  Pinned by `TestDriverReadings_LibPQTrimsSpaceAroundAKeyAndNeitherDriverFoldsCase`, which reads the connector's parsed options.
- It panics when any of `PGHOSTADDR`, `PGSERVICE`, `PGSERVICEFILE`, `PGREALM`, `PGREQUIRESSL`, `PGSSLCRL`, `PGREQUIREPEER`, `PGKRBSRVNAME`, `PGGSSLIB`, `PGSYSCONFDIR` or `PGLOCALEDIR` is set, even to an empty value. `migrate` refuses these before lib/pq reads the environment (HEU-862).
  Pinned by `TestUnsupportedEnvNames_MatchWhatLibPQPanicsOn`, which sets each of the 40 PostgreSQL 17 libpq environment variables, and `PGREALM`, alone and recovers the panic. The test sets each to `x`. The panic on an empty value is read in source, not pinned.
- `testutil.IsolatePgxEnv` sets `PGSERVICEFILE` and `PGSYSCONFDIR`, so a test that calls lib/pq must not run under it. `testutil.ClearTimeoutEnv` unsets `PGSERVICE` and `PGSERVICEFILE`. `testutil.ClearLibPQEnv` unsets the eleven panic variables, and `PGCLIENTENCODING` and `PGDATESTYLE`.
  Pinned by `TestClearLibPQEnv_UnsetsEveryVariableLibPQRefusesOrPanicsOn`.
- It returns `ErrCouldNotDetectUsername` when no user is given and none can be found. That error holds no part of the string, so `migrateDriverParseError` passes it through.
  Read in source, not pinned. No test can make the OS user lookup fail.
