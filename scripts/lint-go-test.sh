#!/usr/bin/env bash
# Asserts what the lint wrapper reports, not only that it failed, so a deleted
# guard cannot hide behind a shared exit code.
set -uo pipefail

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

expect 1 "read failed" "a directory as the pin file" \
  --check-only --version-file /tmp
expect 1 "needs a file path" "option with no operand" \
  --check-only --version-file

echo "$pass passed, $fail failed, of $((pass + fail))"
[ "$fail" = 0 ]
