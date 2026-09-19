#!/usr/bin/env bash
# Checks that scripts/lint-go-test.sh can fail. Each row breaks one guard in a
# copy of the wrapper and asserts whether the suite notices.
#
# Every row declares the outcome it expects, so a row whose expression stops
# matching the code fails the run instead of reading as an untested guard.
set -uo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
src=$root/scripts/lint-go.sh
suite=$root/scripts/lint-go-test.sh

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# The wrapper derives its default paths from its own location, so the copy sits
# under a scripts/ directory whose parent carries the files those defaults name.
# Without this every row fails the default-paths case and reads as caught.
mkdir -p "$work/scripts" "$work/.github"
ln -s "$root/.golangci-lint-version" "$work/.golangci-lint-version"
ln -s "$root/go.mod" "$work/go.mod"
ln -s "$root/.github/workflows" "$work/.github/workflows"
copy=$work/scripts/lint-go.sh

# An unmutated copy has to be green, or every row below is measuring the
# harness rather than the mutation.
cp "$src" "$copy"
baseline=$(LINT_GO_SCRIPT=$copy bash "$suite" 2>&1 | tail -1)
case "$baseline" in
  *", 0 failed"*) ;;
  *) echo "baseline is not green, refusing to measure: $baseline" >&2; exit 1 ;;
esac

pass=0
fail=0

# The tracked script is never edited. The suite reads the copy through
# LINT_GO_SCRIPT, so an interrupted run cannot leave a mutated wrapper behind.
#
# want is caught, when the suite must go red, or equivalent, when no input can
# distinguish the mutation and the suite must stay green.
M() {
  local want=$1 label=$2 expr=$3 out ran got sed_rc
  cp "$src" "$copy"
  sed -i "$expr" "$copy"
  sed_rc=$?
  if [ "$sed_rc" != 0 ]; then
    got="not-applied (sed exited $sed_rc)"
  elif cmp -s "$src" "$copy"; then
    got="not-applied (matched nothing)"
  elif ! bash -n "$copy" 2>/dev/null; then
    got="syntax-broken"
  else
    out=$(LINT_GO_SCRIPT=$copy bash "$suite" 2>&1 | tail -1)
    ran=${out##*of }
    if [ -z "$ran" ] || [ "$ran" -lt 2 ] 2>/dev/null; then
      got="no-count ($out)"
    else
      case "$out" in
        *", 0 failed"*) got=equivalent ;;
        *)              got=caught ;;
      esac
    fi
  fi
  case "$got" in not-applied*) got=not-applied ;; esac
  if [ "$got" = "$want" ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf 'FAIL  %-44s wanted %s, got %s\n' "$label" "$want" "$got"
  fi
}

# --- the go.mod directive block and the built-with comparison ---
M caught "awk print \$2 -> XXX"          's|{print $2; exit}|{print "XXX"; exit}|'
M caught "awk anchor dropped"            's|/\^go /|/go /|'
M caught "awk exit dropped"              's|{print $2; exit}|{print $2}|'
M caught "awk 2>/dev/null dropped"       's|"$go_mod" 2>/dev/null|"$go_mod"|'
M caught "note guard -> true"            's|&& \[ -n "$directive" \]|\&\& true|'
M caught "d_minor truncation dropped"    's|d_minor=${d_minor%%\[!\[:digit:\]\]\*}|d_minor=$d_minor|'
M caught "bw_minor truncation dropped"   's|bw_minor=${bw_minor%%\[!\[:digit:\]\]\*}|bw_minor=$bw_minor|'
M caught "digit bound dropped"           's|{1,9}|+|g'
M caught "d_major parse test -> true"    's|\[\[ $d_major =~ \^\[0-9\]{1,9}\$ \]\]|true|'
M caught "d_minor parse test -> true"    's|\[\[ $d_minor =~ \^\[0-9\]{1,9}\$ \]\]|true|'
M caught "bw_major parse test -> true"   's|\[\[ $bw_major =~ \^\[0-9\]{1,9}\$ \]\]|true|'
M caught "bw_minor parse test -> true"   's|\[\[ $bw_minor =~ \^\[0-9\]{1,9}\$ \]\]|true|'
M caught "major -lt -> -gt"              's|"$bw_major" -lt "$d_major"|"$bw_major" -gt "$d_major"|'
M caught "minor -lt -> -gt"              's|"$bw_minor" -lt "$d_minor"|"$bw_minor" -gt "$d_minor"|'
M caught "major -eq -> true"             's|\[ "$bw_major" -eq "$d_major" \]|true|'
M caught "built-with headline deleted"   '/older Go line than this module targets/d'

# --- the workflow guard ---
M caught "version-file pattern broken"   's@version-file\[@versionXfile[@'
M caught "version pattern broken"        's@?version\[@?versionX[@'
M caught "both tests joined with or"     's@&& printf@|| printf@'
M caught "key anchors dropped"           's@\^\[\[:space:\]\]\*\[@[[:space:]]*[@g'
M caught "workflow exists guard->true"   's|\[ -e "$workflow" \]|true|'
M caught "workflow read status ignored"  's|if ! workflow_body=$(cat "$workflow"); then|workflow_body=$(cat "$workflow" 2>/dev/null); if false; then|'
M caught "both-inputs headline deleted"  '/sets both version and version-file; not linting/d'
M caught "scope caveat line deleted"     '/does not tell one step from another/d'

# --- controls ---
M not-applied "control: matches nothing" 's|NOT_PRESENT_ANYWHERE|x|'

echo "$pass passed, $fail failed, of $((pass + fail))"
[ "$fail" = 0 ]
