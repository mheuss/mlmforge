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

echo "$pass passed, $fail failed, of $((pass + fail))"
[ "$fail" = 0 ]
