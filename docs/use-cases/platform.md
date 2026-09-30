# Platform Use-Cases

Use-cases for the Platform bounded context.

## Table of Contents

- [UC-PLATFORM-001: Withhold a refused database connection string](#uc-platform-001-withhold-a-refused-database-connection-string)

---

### UC-PLATFORM-001: Withhold a refused database connection string

**Added:** Unreleased (HEU-867)
**Files:** `internal/platform/connstring.go` (`ConnStringError`, `PgxConnStringError`, `migrateSchemeError`, `migrateDriverParseError`, `migrateConnStringError`), `internal/platform/migrate.go` (`openMigration`), `cmd/mlmforge/treedeps.go` (`openTreeDeps`), `internal/networkengine/tree_lock_postgres.go` (`PostgresTreeLocker.Lock`), `internal/testutil/connstring.go` (`ConnStringCases`, `RequireNoPasswordWindow`, `RequireNoDriverParseError`)

**Problem:** A driver that refuses a connection string often quotes it, or part of it, in its error. The password goes with it, to the terminal, to job logs, and to anything that walks the error chain.

**Solution:** Replace the driver's error at the call site with `ConnStringError`. It names the driver and a stage and holds nothing from the string. It has no `Unwrap`, so the raw string in the driver's error cannot be reached through the chain. pgx refusals go through `PgxConnStringError`. The migrate path runs its checks inside `openMigration`.

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
    // cse.Driver() and cse.Stage() are safe to log or tag. err.Error() holds no part of the string.
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

**Notes:** Run the password-window and chain checks before the full-text check, so a mutation shows each one failing on its own. What each driver does with a refused string, and which test pins it, is in `docs/development/connection-strings.md`. A password with an unencoded `/`, `#`, `?` or `@` that a driver still accepts can still leak through a later dial, DNS, connect or option error. That is HEU-875.
