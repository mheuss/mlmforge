# Platform Use-Cases

Use-cases for the Platform bounded context.

## Table of Contents

- [UC-PLATFORM-001: Withhold a refused database connection string](#uc-platform-001-withhold-a-refused-database-connection-string)
- [UC-PLATFORM-002: Guard a package against float types](#uc-platform-002-guard-a-package-against-float-types)
- [UC-PLATFORM-003: Read a UUID the way Postgres does](#uc-platform-003-read-a-uuid-the-way-postgres-does)

---

### UC-PLATFORM-001: Withhold a refused database connection string

**Added:** Unreleased (HEU-867)
**Files:** `internal/platform/connstring.go` (`ConnStringError`, `PreDriverError`, `keywordKeyError`, `afterPasswordError`, `rawPlusError`, `PgxConnStringError`, `migrateSchemeError`, `migrateDriverParseError`, `migrateConnStringError`), `internal/platform/migrate.go` (`openMigration`), `internal/platform/migrate_session.go` (`dialSession`), `cmd/mlmforge/treedeps.go` (`openTreeDeps`), `internal/networkengine/tree_lock_postgres.go` (`PostgresTreeLocker.Lock`), `cmd/mlmforge/resolve.go` (`resolveDBURL`), `internal/testutil/connstring.go` (`ConnStringCases`, `ResolveRefusedCases`, `ReachesDriverCases`, `RequireNoPasswordWindow`, `RequireNoDriverParseError`)

**Problem:** A driver that refuses a connection string often quotes it, or part of it, in its error. The password goes with it, to the terminal, to job logs, and to anything that walks the error chain.

**Solution:** Replace the driver's error at the call site with `ConnStringError`. It names the driver and the stage. For stage `raw-at` it also names the part. It holds nothing from the string. It has no `Unwrap`, so the raw string in the driver's error cannot be reached through the chain. pgx refusals go through `PgxConnStringError`. The migrate path checks the environment in `openMigration` and the string in `dialSession`.

**Usage:**
```go
// A new call site that parses a connection string with pgx
cfg, err := pgxpool.ParseConfig(connString)
if err != nil {
    return fmt.Errorf("open the report pool: %w", platform.PgxConnStringError(err))
}

// A caller deciding what to log or tag
var cse *platform.ConnStringError
if errors.As(err, &cse) {
    // cse.Driver(), cse.Stage() and cse.Part() are safe to log or tag. err.Error() holds no part of the string.
}
```

A test for a new call site runs the shared cases and checks the text, the chain and the full message:

```go
for _, tc := range testutil.ConnStringCases() {
    if tc.PgxStage == "" {
        continue
    }
    // call the site with tc.ConnString, then:
    testutil.RequireNoPasswordWindow(t, fmt.Sprintf("%v\n%+v", err, err), tc.Password, want, tc.WithoutPassword())
    testutil.RequireNoDriverParseError(t, err)
    require.EqualError(t, err, want)
}
```

**Notes:** Run the password-window and chain checks before the full-text check, so a mutation shows each one failing on its own. What each driver does with a refused string, and which test pins it, is in `docs/development/connection-strings.md`. Before `mlmforge migrate` or `mlmforge tree` opens a driver, `resolveDBURL` runs `PreDriverError`. A call site that does not go through `resolveDBURL` is not covered. `PreDriverError` refuses a `postgres://` or `postgresql://` scheme that is not all lowercase. It also refuses a raw `@` after the host part. A raw `/`, `?` or `#` in a password puts the userinfo's closing `@` there. It also refuses a non-URL string whose first keyword key no parameter name can hold, outside a `_pq_.` protocol option. It refuses any query key or `&` after `password`. It refuses a raw `+` in a query password.

---

### UC-PLATFORM-002: Guard a package against float types

**Added:** Unreleased (HEU-981)
**Files:** `internal/testutil/floatidents.go` (`FindFloatIdents`), `internal/commerce/float_guard_test.go` (`TestNoFloatInCommerce`), `internal/financial/float_guard_test.go` (`TestNoFloatInFinancial`), `internal/networkengine/float_guard_test.go` (`TestNoFloatOnTheDecimalBoundary`)

**Problem:** A `float64` money or volume field rounds values that must stay exact, and the next author copies it.

**Solution:** Two guards exist, and they make different trade-offs. `FindFloatIdents` parses every non-test Go file in one directory and reports each `float32` or `float64` identifier with its owner. It sees nothing that has no float identifier, such as `NullFloat64` or an untyped literal. `TestNoFloatOnTheDecimalBoundary` scans a listed set of files with a regex. It catches `NullFloat64`, `NewFromFloat` and `ParseFloat`, but only in the files it lists. HEU-1006 asks whether to converge them.

**Usage:**
```go
func TestNoFloatInOrders(t *testing.T) {
	found, err := testutil.FindFloatIdents(".", nil)

	require.NoError(t, err)
	assert.Empty(t, found, "float32 or float64 in the non-test Go files of this package")
}
```

**Notes:** An owner is `Type.Field` for a struct field, `Type.Method` for a method, and the function name for a plain function. A legitimate float is excused by naming its owner in the exception set, for example `map[string]bool{"Rate.Ratio": true}`. See the guard limit noted on HEU-531.

### UC-PLATFORM-003: Read a UUID the way Postgres does

**Added:** Unreleased (HEU-944)
**Files:** `internal/platform/uuid.go` (`CanonicalUUID`)

**Problem:** A memory twin that parses ids with `uuid.Parse` matches spellings Postgres refuses, and misses spellings Postgres accepts.

**Solution:** `CanonicalUUID` returns the lowercase hyphenated form of a string Postgres's uuid input accepts. It reports false for anything Postgres would refuse. It accepts braces around the id and a hyphen after any group of four hex digits. It refuses a `urn:uuid:` prefix and a value wrapped in characters that are not braces.

**Usage:**
```go
key, ok := platform.CanonicalUUID(id)
if !ok {
    return fmt.Errorf("save widget: id %q is not a UUID; nothing was written", id)
}
store[key] = widget
```

**Notes:** The memory event store and the memory tree store both use it. The memory tree store refuses an id it rejects on its four write methods and passes it through unchanged on reads. HEU-1018 asks whether reads should refuse too. HEU-1019 covers the tree consumer's gate, which does not check `tree_id`.

---
