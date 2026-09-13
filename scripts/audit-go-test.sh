#!/usr/bin/env bash
# Asserts which packages audit-go.sh would scan, and that it refuses rather
# than scanning a short list. The scan itself needs a network and a real module
# graph and is not covered here. Package selection is the half that fails
# silently: over-exclude and the scan reports clean over less code than anyone
# thinks.
#
# The hazard cases run against fixture modules in a temp directory, so nothing
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

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# A fixture module holding the package that makes the substring hazard visible.
# No package in the real repo contains internal/testutil as a substring, so
# against the repo alone a substring exclusion looks identical to a correct one.
fixture=$work/fixture
mkdir -p "$fixture"
printf 'module example.com/fixture\n\ngo 1.21\n' > "$fixture/go.mod"
for pkg in cmd/app internal/testutil internal/testutil_helpers internal/store; do
  mkdir -p "$fixture/$pkg"
  printf 'package %s\n' "${pkg##*/}" > "$fixture/$pkg/doc.go"
done

# A module with a go.mod and no packages at all.
empty=$work/empty
mkdir -p "$empty"
printf 'module example.com/empty\n\ngo 1.21\n' > "$empty/go.mod"

# A go that prints some packages, writes to stderr, and exits non-zero. This is
# the shape that silently shrinks a scan when a list's exit status goes unread.
shim=$work/shim
mkdir -p "$shim"
cat > "$shim/go" <<'SHIM'
#!/usr/bin/env bash
if [ "$1" = list ] && [ "$2" = -m ]; then echo "example.com/partial"; exit 0; fi
if [ "$1" = list ]; then
  echo "example.com/partial/cmd/app"
  echo "go: some package failed to load" >&2
  exit 1
fi
exit 0
SHIM
chmod +x "$shim/go"

run() {
  local out rc
  out=$("$@" 2>"$work/stderr")
  rc=$?
  printf '%s\n' "$out"
  return $rc
}

# Real repo: stdout only, so toolchain chatter on stderr cannot be read as an
# import path.
listed=$(run "$audit" --list)
list_rc=$?
listed_stderr=$(cat "$work/stderr")

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

# The substring hazard, against the fixture.
fixture_listed=$(run "$audit" --list "$fixture")
fixture_rc=$?
fixture_stderr=$(cat "$work/stderr")

if [ "$fixture_rc" != 0 ]; then
  record no "a package named after the excluded one survives" "rc=$fixture_rc: $fixture_stderr"
  record no "the fixture's testutil is excluded" "rc=$fixture_rc: $fixture_stderr"
else
  if printf '%s\n' "$fixture_listed" | grep -qxF "example.com/fixture/internal/testutil_helpers"; then
    record yes "a package named after the excluded one survives" ""
  else
    record no "a package named after the excluded one survives" "testutil_helpers was dropped"
  fi

  if printf '%s\n' "$fixture_listed" | grep -qxF "example.com/fixture/internal/testutil"; then
    record no "the fixture's testutil is excluded" "it appears in the list"
  else
    record yes "the fixture's testutil is excluded" ""
  fi
fi

# A go list that fails partway must be refused, not scanned.
out=$(PATH="$shim:$PATH" "$audit" --list "$fixture" 2>&1)
rc=$?
if [ "$rc" = 1 ] && [[ $out == *"exited non-zero"* ]]; then
  record yes "refuses a go list that fails partway" ""
else
  record no "refuses a go list that fails partway" "rc=$rc: $out"
fi

# go list exits 0 with no output when nothing matches.
out=$("$audit" --list "$empty" 2>&1)
rc=$?
if [ "$rc" = 1 ] && [[ $out == *"no packages"* ]]; then
  record yes "refuses a module with no packages" ""
else
  record no "refuses a module with no packages" "rc=$rc: $out"
fi

out=$("$audit" --list "$fixture" extra 2>&1)
rc=$?
if [ "$rc" = 1 ] && [[ $out == *"usage:"* ]]; then
  record yes "rejects extra arguments" ""
else
  record no "rejects extra arguments" "rc=$rc: $out"
fi

out=$("$audit" --bogus 2>&1)
rc=$?
if [ "$rc" = 1 ] && [[ $out == *"unknown flag"* ]]; then
  record yes "rejects an unknown flag" ""
else
  record no "rejects an unknown flag" "rc=$rc: $out"
fi

out=$("$audit" --list "$work/nonexistent" 2>&1)
rc=$?
if [ "$rc" = 1 ] && [[ $out == *"no readable go.mod"* ]]; then
  record yes "rejects a root with no go.mod" ""
else
  record no "rejects a root with no go.mod" "rc=$rc: $out"
fi

echo "$pass passed, $fail failed, of $((pass + fail))"
[ "$fail" = 0 ]
