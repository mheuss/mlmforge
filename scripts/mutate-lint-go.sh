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
# Every row has to run this many cases. A mutation that truncates the run and
# goes red otherwise reads the same as one the whole suite caught.
baseline_ran=${baseline##*of }
if ! [[ $baseline_ran =~ ^[0-9]+$ ]]; then
  echo "baseline reports no denominator, refusing to measure: $baseline" >&2
  exit 1
fi

pass=0
fail=0

# The tracked script is never edited. The suite reads the copy through
# LINT_GO_SCRIPT, so an interrupted run cannot leave a mutated wrapper behind.
#
# want is caught, when the suite must go red, or equivalent, when no input can
# distinguish the mutation and the suite must stay green.
M() {
  local want=$1 label=$2 expr=$3 out ran got sed_rc detail
  if ! cp "$src" "$copy"; then
    printf 'FAIL  %-44s could not restore the copy\n' "$label"
    fail=$((fail + 1))
    return
  fi
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
    # Checked as a number first. Output with no "of N" in it leaves ran holding
    # the whole line, and a numeric test on that errors rather than failing.
    if ! [[ $ran =~ ^[0-9]+$ ]] || [ "$ran" != "$baseline_ran" ]; then
      got="wrong-count ($out)"
    else
      case "$out" in
        *", 0 failed"*) got=equivalent ;;
        *)              got=caught ;;
      esac
    fi
  fi
  detail=$got
  case "$got" in not-applied*) got=not-applied ;; esac
  if [ "$got" = "$want" ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf 'FAIL  %-44s wanted %s, got %s\n' "$label" "$want" "$detail"
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
M caught "both tests joined with or"    's@&& grep -qE@|| grep -qE@' 
M caught "key anchors dropped"           's@\^\[\[:space:\]\]\*\[@[[:space:]]*[@g'
M caught "workflow exists guard->true"   's|\[ -e "$workflow" \]|true|'
M caught "workflow read status ignored"  's|if ! workflow_body=$(cat "$workflow"); then|workflow_body=$(cat "$workflow" 2>/dev/null); if false; then|'
M caught "both-inputs headline deleted"  '/sets both version and version-file; not linting/d'
M caught "scope caveat line deleted"     '/key-shaped lines anywhere in the file/d'
M caught "which-input-wins line deleted" '/the action uses version" >&2/d' 
M caught "here-strings back to pipes"   's@grep -qE \(.*\) <<< "\$workflow_body"@printf "%s\\n" "$workflow_body" | grep -qE \1@' 
M caught "space before colon dropped"   's@\[\[:space:\]\]\*:@:@g' 

# --- controls ---
M not-applied "control: matches nothing" 's|NOT_PRESENT_ANYWHERE|x|'
# Proves a mutated copy can come back green at all. Without it nothing shows the
# suite is discriminating rather than merely sensitive to any edit.
M equivalent  "control: comment text only" 's|# Trims the whole value|# trims the whole value|'

echo "$pass passed, $fail failed, of $((pass + fail))"
[ "$fail" = 0 ]
