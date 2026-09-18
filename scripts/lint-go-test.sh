#!/usr/bin/env bash
# Asserts what the lint wrapper reports, not only that it failed, so a deleted
# guard cannot hide behind a shared exit code.
set -uo pipefail

# cat's diagnostics are localized and one assertion matches on their text.
export LC_ALL=C

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
check=$root/scripts/lint-go.sh
data=$root/scripts/testdata

pass=0
fail=0

record() {
  local ok=$1 name=$2 detail=$3
  if [ "$ok" = yes ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    echo "FAIL $name: $detail"
  fi
}

# Asserts the exit code and that the output contains a phrase naming the guard.
expect() {
  local want_rc=$1 want_msg=$2 name=$3 out rc
  shift 3
  out=$("$check" "$@" 2>&1)
  rc=$?
  if [ "$rc" != "$want_rc" ]; then
    record no "$name" "wanted rc=$want_rc, got rc=$rc: $out"
  elif [ -n "$want_msg" ] && [[ $out != *"$want_msg"* ]]; then
    record no "$name" "wanted output containing \"$want_msg\", got: $out"
  else
    record yes "$name" ""
  fi
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# A golangci-lint that prints a chosen version line and records how it was
# called. The success path is the one line no refusal test reaches.
stub_dir() {
  # Split, because under set -u bash declares every name in one local
  # statement before running its assignments, so $name is unset in $dir.
  local name=$1 line=$2 rc=${3:-0}
  local dir=$work/$name
  mkdir -p "$dir"
  cat > "$dir/golangci-lint" <<STUB
#!/usr/bin/env bash
if [ "\$1" = version ]; then
  printf '%s\n' '$line'
  exit $rc
fi
printf '%s\n' "\$@" > "\${STUB_ARGS:-/dev/null}"
[ -n "\${STUB_CALLS:-}" ] && echo x >> "\$STUB_CALLS"
exit \${STUB_RUN_RC:-0}
STUB
  chmod +x "$dir/golangci-lint"
  printf '%s' "$dir"
}

# Same as expect, with a stub directory prepended to PATH.
expect_with() {
  local dir=$1
  shift
  PATH="$dir:$PATH" expect "$@"
}

ok_line='golangci-lint has version 2.13.2 built with go1.27.0 from abc1234 on 2026-08-27T23:01:12Z'
old_line='golangci-lint has version 2.11.4 built with go1.26.1 from def5678 on 2026-06-01T00:00:00Z'

expect 1 "cannot read" "unreadable pin file" \
  --check-only --version-file "$data/nonexistent"
expect 1 "not obtained" "unreadable pin file marks pinned as not obtained" \
  --check-only --version-file "$data/nonexistent"

expect 1 "Is a directory" "a directory as the pin file keeps cat's reason" \
  --check-only --version-file "$data"

expect 1 "needs a file path" "option with no operand" \
  --check-only --version-file

expect 1 "is not a complete version" "partial pin" \
  --check-only --version-file "$data/lintver-partial"
expect 1 "is not a complete version" "latest pin" \
  --check-only --version-file "$data/lintver-latest"
expect 1 "is not a complete version" "empty pin file" \
  --check-only --version-file "$data/lintver-empty"
expect 1 "is not a complete version" "two-line pin file" \
  --check-only --version-file "$data/lintver-twolines"
# Pins the escaping in the regex. Dropping the backslashes leaves every other
# case green while the guard starts accepting a three-line pin.
expect 1 "is not a complete version" "non-dot separators" \
  --check-only --version-file "$data/lintver-separators"
expect 0 "" "bare pin without a leading v" \
  --check-only --version-file "$data/lintver-bare"
expect 0 "" "leading blank lines are trimmed like the action trims them" \
  --check-only --version-file "$data/lintver-leading"
# The only assertion that uses the script's own default paths. Without it a
# wrong filename or a wrong number of .. leaves the suite green and breaks
# every real invocation.
expect 0 "" "the default pin file is the tracked one" --check-only
a=$("$check" --check-only --version-file "$data/lintver-ok" 2>&1); arc=$?
b=$("$check" --check-only --version-file "$data/lintver-bare" 2>&1); brc=$?
if [ "$arc" = "$brc" ] && [ "$a" = "$b" ]; then
  record yes "a leading v changes nothing" ""
else
  record no "a leading v changes nothing" "rc $arc/$brc, out \"$a\" vs \"$b\""
fi

expect_with "$(stub_dir match "$ok_line")" 0 "" "version matches the pin" \
  --check-only --version-file "$data/lintver-ok"
drift_dir=$(stub_dir drift "$old_line")
expect_with "$drift_dir" 1 "installed  2.11.4" "version differs from the pin" \
  --check-only --version-file "$data/lintver-ok"
expect_with "$drift_dir" 1 "$drift_dir/golangci-lint" "the mismatch names the resolved path" \
  --check-only --version-file "$data/lintver-ok"
expect_with "$drift_dir" 1 "pinned     v2.13.2" "the mismatch names the pin" \
  --check-only --version-file "$data/lintver-ok"
expect_with "$(stub_dir badrc "$ok_line" 3)" 1 "version command exited 3" "version command fails" \
  --check-only --version-file "$data/lintver-ok"
expect_with "$(stub_dir garbage 'not a version line')" 1 "does not parse" "version output does not parse" \
  --check-only --version-file "$data/lintver-ok"

# An empty directory as the whole PATH, so command -v resolves nothing.
empty=$work/emptypath
mkdir -p "$empty"
# /usr/bin and /bin so the script's own `env bash` shebang still resolves.
# golangci-lint is in neither; it lives in ~/.local/bin on this machine.
out=$(PATH="$empty:/usr/bin:/bin" "$check" --check-only --version-file "$data/lintver-ok" 2>&1)
rc=$?
if [ "$rc" = 1 ] && [[ $out == *"not obtained"* ]] && [[ $out == *"v2.13.2"* ]]; then
  record yes "no binary on PATH" ""
else
  record no "no binary on PATH" "rc=$rc: $out"
fi
# Pins the v-strip on the installed version. Real golangci-lint prints no
# leading v, so nothing else would notice if the strip were removed.
v_line='golangci-lint has version v2.13.2 built with go1.27.0 from abc1234 on 2026-08-27T23:01:12Z'
expect_with "$(stub_dir vprefixed "$v_line")" 0 "" "a binary reporting a leading v still matches" \
  --check-only --version-file "$data/lintver-ok"
expect_with "$(stub_dir drift2 "$old_line")" 1 "go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2" \
  "the mismatch names the install command" --check-only --version-file "$data/lintver-ok"

echo "$pass passed, $fail failed, of $((pass + fail))"
[ "$fail" = 0 ]
