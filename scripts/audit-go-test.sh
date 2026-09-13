#!/usr/bin/env bash
# Asserts which packages audit-go.sh would scan. The scan itself needs a
# network and a real module graph and is not covered here. Package selection is
# the half that fails silently: over-exclude and the scan reports clean over
# less code than anyone thinks.
#
# The hazard cases run against a fixture module in a temp directory, so nothing
# here writes into the repo.
set -uo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
audit=$root/scripts/audit-go.sh

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

# A fixture module holding the package that makes the substring hazard visible.
# No package in the real repo contains internal/testutil as a substring, so
# against the repo alone a substring exclusion looks identical to a correct one.
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT

printf 'module example.com/fixture\n\ngo 1.27.0\n' > "$fixture/go.mod"
for pkg in cmd/app internal/testutil internal/testutil_helpers internal/store; do
  mkdir -p "$fixture/$pkg"
  printf 'package %s\n' "${pkg##*/}" > "$fixture/$pkg/doc.go"
done

# Real repo: stdout only, so toolchain chatter on stderr cannot be read as an
# import path.
listed_err=$(mktemp)
listed=$("$audit" --list 2>"$listed_err")
list_rc=$?
listed_stderr=$(cat "$listed_err")
rm -f "$listed_err"

if [ "$list_rc" = 0 ]; then
  record yes "--list exits 0" ""
else
  record no "--list exits 0" "rc=$list_rc: $listed_stderr"
fi

if ! all=$(cd "$root" && go list ./...); then
  record no "go list succeeds in the repo" "$all"
  all=""
fi

if ! module=$(cd "$root" && go list -m); then
  record no "go list -m succeeds in the repo" "$module"
  module=""
fi

# The delta, computed fresh rather than pinned, so adding a package does not
# make this stale. Asserting the delta IS internal/testutil catches a pattern
# that starts eating something else, which a count alone would not.
dropped=$(comm -23 <(printf '%s\n' "$all" | sort) <(printf '%s\n' "$listed" | sort))

if [ "$dropped" = "$module/internal/testutil" ]; then
  record yes "the scan excludes internal/testutil and nothing else" ""
else
  record no "the scan excludes internal/testutil and nothing else" "dropped: ${dropped:-<nothing>}"
fi

if printf '%s\n' "$listed" | grep -qxF "$module/internal/testutil"; then
  record no "internal/testutil is excluded" "it appears in the list"
else
  record yes "internal/testutil is excluded" ""
fi

# Every listed package must be a real package in the module.
missing=""
while IFS= read -r pkg; do
  [ -n "$pkg" ] || continue
  printf '%s\n' "$all" | grep -qxF "$pkg" || missing="$missing $pkg"
done <<< "$listed"
if [ -z "$missing" ]; then
  record yes "every listed package exists in the module" ""
else
  record no "every listed package exists in the module" "not in go list:$missing"
fi

# The substring hazard, against the fixture. A package whose import path merely
# contains the excluded one must survive.
fixture_listed=$("$audit" --list "$fixture" 2>/dev/null)
fixture_rc=$?
if [ "$fixture_rc" != 0 ]; then
  record no "a package named after the excluded one survives" "--list rc=$fixture_rc"
elif printf '%s\n' "$fixture_listed" | grep -qxF "example.com/fixture/internal/testutil_helpers"; then
  record yes "a package named after the excluded one survives" ""
else
  record no "a package named after the excluded one survives" "testutil_helpers was dropped from the scan"
fi

if printf '%s\n' "$fixture_listed" | grep -qxF "example.com/fixture/internal/testutil"; then
  record no "the fixture's testutil is excluded" "it appears in the list"
else
  record yes "the fixture's testutil is excluded" ""
fi

out=$("$audit" --list "$fixture" extra 2>&1)
rc=$?
if [ "$rc" = 1 ] && [[ $out == *"usage:"* ]]; then
  record yes "rejects extra arguments" ""
else
  record no "rejects extra arguments" "rc=$rc: $out"
fi

out=$("$audit" --list "$fixture/nonexistent" 2>&1)
rc=$?
if [ "$rc" = 1 ] && [[ $out == *"no readable go.mod"* ]]; then
  record yes "rejects a root with no go.mod" ""
else
  record no "rejects a root with no go.mod" "rc=$rc: $out"
fi

echo "$pass passed, $fail failed, of $((pass + fail))"
[ "$fail" = 0 ]
