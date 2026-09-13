#!/usr/bin/env bash
# Asserts which packages the audit script would scan, and that it refuses
# rather than scanning a short list. Hazard cases run against fixture modules
# in a temp directory, so nothing here writes into the repo.
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

# A fixture module holding a package whose path contains the excluded one as a
# substring. Without such a package a substring exclusion looks identical to a
# whole-line one.
fixture=$work/fixture
mkdir -p "$fixture"
printf 'module example.com/fixture\n\ngo 1.21\n' > "$fixture/go.mod"
for pkg in cmd/app internal/testutil internal/testutil_helpers internal/store; do
  mkdir -p "$fixture/$pkg"
  printf 'package %s\n' "${pkg##*/}" > "$fixture/$pkg/doc.go"
done

empty=$work/empty
mkdir -p "$empty"
printf 'module example.com/empty\n\ngo 1.21\n' > "$empty/go.mod"

# A module whose only package is the excluded one, so filtering empties the
# list. This reaches a different refusal from the empty module above.
only=$work/only
mkdir -p "$only/internal/testutil"
printf 'module example.com/only\n\ngo 1.21\n' > "$only/go.mod"
printf 'package testutil\n' > "$only/internal/testutil/doc.go"

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

# A go whose module query fails. The partway shim above answers list -m before
# it fails, so without this the module-query branch is never reached.
modshim=$work/modshim
mkdir -p "$modshim"
cat > "$modshim/go" <<'SHIM'
#!/usr/bin/env bash
if [ "$1" = list ] && [ "$2" = -m ]; then
  echo "go: cannot determine module path" >&2
  exit 1
fi
exit 0
SHIM
chmod +x "$modshim/go"

# A grep that emits part of its output and exits 2. Exit 1 means no match and is
# ordinary; anything above it is an error the filter must not absorb.
grepshim=$work/grepshim
mkdir -p "$grepshim"
cat > "$grepshim/grep" <<'SHIM'
#!/usr/bin/env bash
for arg in "$@"; do
  if [ "$arg" = -vxF ]; then
    echo "example.com/fixture/cmd/app"
    echo "grep: input error" >&2
    exit 2
  fi
done
exec /usr/bin/grep "$@"
SHIM
chmod +x "$grepshim/grep"

# A govulncheck that records the arguments it was handed. The scan path is the
# one line --list never executes, so nothing else asserts what it passes.
vulnshim=$work/vulnshim
mkdir -p "$vulnshim"
cat > "$vulnshim/govulncheck" <<'SHIM'
#!/usr/bin/env bash
printf '%s\n' "$@" > "$VULNSHIM_ARGS"
exit 0
SHIM
chmod +x "$vulnshim/govulncheck"

# stderr goes to a caller-named file, so a later call cannot overwrite the text
# an earlier failure message is about to read.
run() {
  local errfile=$1 out rc
  shift
  out=$("$@" 2>"$errfile")
  rc=$?
  printf '%s\n' "$out"
  return $rc
}

# Real repo: stdout only, so toolchain chatter on stderr cannot be read as an
# import path.
listed=$(run "$work/repo.err" "$audit" --list)
list_rc=$?
listed_stderr=$(cat "$work/repo.err")

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

# Lines only in "listed". This is what checks that --list prints import paths
# and nothing else, and it fails if chatter ever reaches stdout.
extra=$(comm -13 <(printf '%s\n' "$all" | sort) <(printf '%s\n' "$listed" | sort))

if [ "$list_rc" != 0 ]; then
  record no "--list prints nothing but import paths" "--list rc=$list_rc, so there was no output to check"
elif [ -z "$extra" ]; then
  record yes "--list prints nothing but import paths" ""
else
  record no "--list prints nothing but import paths" "not in go list: $extra"
fi

if printf '%s\n' "$listed" | grep -qxF "$module/internal/testutil"; then
  record no "internal/testutil is excluded" "it appears in the list"
else
  record yes "internal/testutil is excluded" ""
fi

# The substring hazard, against the fixture.
fixture_listed=$(run "$work/fixture.err" "$audit" --list "$fixture")
fixture_rc=$?
fixture_stderr=$(cat "$work/fixture.err")

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
if [ "$rc" = 1 ] && [[ $out == *"go list ./... exited non-zero"* ]]; then
  record yes "refuses a go list that fails partway" ""
else
  record no "refuses a go list that fails partway" "rc=$rc: $out"
fi

# go list exits 0 with no output when nothing matches.
out=$("$audit" --list "$empty" 2>&1)
rc=$?
if [ "$rc" = 1 ] && [[ $out == *"produced no packages"* ]]; then
  record yes "refuses a module with no packages" ""
else
  record no "refuses a module with no packages" "rc=$rc: $out"
fi

out=$("$audit" --list "$only" 2>&1)
rc=$?
if [ "$rc" = 1 ] && [[ $out == *"no packages left after excluding"* ]]; then
  record yes "refuses a module whose only package is excluded" ""
else
  record no "refuses a module whose only package is excluded" "rc=$rc: $out"
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

# A module query that fails must be refused before any package list is read.
out=$(PATH="$modshim:$PATH" "$audit" --list "$fixture" 2>&1)
rc=$?
if [ "$rc" = 1 ] && [[ $out == *"go list -m exited non-zero"* ]]; then
  record yes "refuses a failing module query" ""
else
  record no "refuses a failing module query" "rc=$rc: $out"
fi

# A grep error must be refused rather than read as a shorter package list.
out=$(PATH="$grepshim:$PATH" "$audit" --list "$fixture" 2>&1)
rc=$?
if [ "$rc" = 1 ] && [[ $out == *"grep exited 2"* ]]; then
  record yes "refuses a grep that errors partway" ""
else
  record no "refuses a grep that errors partway" "rc=$rc: $out"
fi

# The scan path hands govulncheck the same list --list prints. Without this the
# scan could be changed to ./... and every other assertion would still pass.
VULNSHIM_ARGS=$work/vulnshim.args
export VULNSHIM_ARGS
scan_out=$(PATH="$vulnshim:$PATH" "$audit" "$fixture" 2>&1)
scan_rc=$?
scanned=$(cat "$work/vulnshim.args" 2>/dev/null)
if [ "$scan_rc" != 0 ]; then
  record no "the scan receives the listed packages" "rc=$scan_rc: $scan_out"
elif [ "$scanned" = "$fixture_listed" ]; then
  record yes "the scan receives the listed packages" ""
else
  record no "the scan receives the listed packages" "scanned: ${scanned:-<nothing>}"
fi

echo "$pass passed, $fail failed, of $((pass + fail))"
[ "$fail" = 0 ]
