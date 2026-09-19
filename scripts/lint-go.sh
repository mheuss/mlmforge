#!/usr/bin/env bash
# Refuses to lint when the resolved golangci-lint does not match the pin.
# Input paths are arguments so the checks can be run against fixtures.
set -uo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

usage="usage: ${0##*/} [--check-only] [--version-file FILE] [--go-mod FILE] [--workflow FILE] [--] [golangci-lint arguments...]"

check_only=false
version_file=$root/.golangci-lint-version
go_mod=$root/go.mod
workflow=$root/.github/workflows/ci.yml

while [ "$#" -gt 0 ]; do
  case "$1" in
    --check-only) check_only=true; shift ;;
    --version-file|--go-mod|--workflow)
      # Checked before the assignment, because $2 under set -u aborts with a
      # bash diagnostic naming a line number rather than a refusal.
      if [ "$#" -lt 2 ]; then
        echo "option \"$1\" needs a file path; not linting" >&2
        echo "  $usage" >&2
        exit 1
      fi
      case "$1" in
        --version-file) version_file=$2 ;;
        --go-mod) go_mod=$2 ;;
        --workflow) workflow=$2 ;;
      esac
      shift 2
      ;;
    --) shift; break ;;
    -*) echo "unknown option \"$1\"; not linting" >&2; echo "  $usage" >&2; exit 1 ;;
    *) break ;;
  esac
done

# The action prefers version over version-file and logs that it ignored the
# file. The pin file would then be decorative while CI installed something else.
#
# Read over the whole file. Deciding which step a key belongs to needs a YAML
# parser, and four line-oriented attempts each failed on a construct the next
# one did not handle. The cost is that two actions setting one key each look
# like one setting both; the message says so rather than naming a step.
if [ -e "$workflow" ]; then
  # cat's stderr is left alone because it names which of the two happened.
  if ! workflow_body=$(cat "$workflow"); then
    echo "cannot read \"$workflow\"; not linting" >&2
    exit 1
  fi
  # A commented key needs no stripping: the anchored patterns below cannot match
  # a line whose first non-space character is a #.
  if printf '%s\n' "$workflow_body" | grep -qE '^[[:space:]]*["'"'"']?version-file["'"'"']?:' \
    && printf '%s\n' "$workflow_body" | grep -qE '^[[:space:]]*["'"'"']?version["'"'"']?:'; then
    echo "\"$workflow\" sets both version and version-file; not linting" >&2
    echo "  this reads the whole file and does not tell one step from another" >&2
    echo "  where one golangci-lint-action step carries both, the action uses version" >&2
    exit 1
  fi
fi

# A directory can be opened and not read, so the read's own status is the check.
# cat's stderr is left alone because it names which of the two happened.
if ! pin_body=$(cat "$version_file"); then
  echo "cannot read \"$version_file\"; not linting" >&2
  echo "  pinned     not obtained (cat exited non-zero)" >&2
  exit 1
fi

# Trims the whole value, so a leading newline goes too.
# Interior whitespace stays: collapsing it accepts a pin that is not the pin.
pin_raw=$pin_body
pin_raw=${pin_raw#"${pin_raw%%[![:space:]]*}"}
pin_raw=${pin_raw%"${pin_raw##*[![:space:]]}"}
pin=${pin_raw#[vV]}

# Anchored against the whole string. grep matches per line, so a two-line pin
# file would satisfy it on line one.
if ! [[ $pin =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  # %q so a value holding a newline or a carriage return renders on one line.
  echo "pin is not a complete version; not linting" >&2
  echo "  pinned     $(printf '%q' "$pin_raw")   ($version_file)" >&2
  echo "  a pin must be MAJOR.MINOR.PATCH, with or without a leading v" >&2
  exit 1
fi

if ! resolved=$(command -v golangci-lint); then
  echo "no golangci-lint on PATH; not linting" >&2
  echo "  pinned     v$pin   ($version_file)" >&2
  echo "  installed  not obtained (command -v golangci-lint found nothing)" >&2
  echo "  built with not obtained" >&2
  exit 1
fi

# stderr goes to its own file. Merging it lets a log line mentioning the
# pinned version satisfy the comparison against a binary that does not.
version_err=$(mktemp)
trap 'rm -f "$version_err"' EXIT
version_rc=0
version_out=$("$resolved" version 2>"$version_err") || version_rc=$?
if [ "$version_rc" != 0 ]; then
  echo "golangci-lint version command exited $version_rc; not linting" >&2
  echo "  pinned     v$pin   ($version_file)" >&2
  echo "  installed  not obtained (\"$resolved version\" exited $version_rc)" >&2
  echo "  built with not obtained" >&2
  echo "  stderr     $(printf '%q' "$(cat "$version_err")")" >&2
  exit 1
fi

# Anchored to the start of a line, and counted. An unanchored search over the
# whole output takes the leftmost match anywhere in it.
installed=
built_with=
version_matches=0
while IFS= read -r version_line; do
  if [[ $version_line =~ ^golangci-lint\ has\ version\ ([^[:space:]]+)\ built\ with\ (go[^[:space:]]+) ]]; then
    version_matches=$((version_matches + 1))
    installed=${BASH_REMATCH[1]}
    built_with=${BASH_REMATCH[2]}
  fi
done <<< "$version_out"

if [ "$version_matches" != 1 ]; then
  echo "golangci-lint version output has $version_matches version lines; not linting" >&2
  echo "  pinned     v$pin   ($version_file)" >&2
  echo "  installed  not obtained ($version_matches lines matched, one expected)" >&2
  echo "  built with not obtained" >&2
  echo "  output     $(printf '%q' "$version_out")" >&2
  exit 1
fi

# Read only here, where it is used, so an unreadable go.mod cannot preempt the
# version mismatch a developer is actually hitting.
directive_note="go directive not obtained"
if directive=$(awk '/^go /{print $2; exit}' "$go_mod" 2>/dev/null) && [ -n "$directive" ]; then
  directive_note="$go_mod go directive is $directive"
fi

if [ "${installed#[vV]}" != "$pin" ]; then
  echo "golangci-lint does not match the pin; not linting" >&2
  echo "  pinned     v$pin   ($version_file)" >&2
  echo "  installed  $(printf '%q' "$installed")    ($resolved)" >&2
  echo "  built with $(printf '%q' "$built_with")  ($directive_note)" >&2
  echo >&2
  echo "  install the pinned version:" >&2
  echo "    go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$pin" >&2
  exit 1
fi

# Major and minor only. The panic this guards against names a language version,
# which carries no patch component.
bw=${built_with#go}
bw_major=${bw%%.*}; bw_rest=${bw#*.}; bw_minor=${bw_rest%%.*}
d_major=${directive%%.*}; d_rest=${directive#*.}; d_minor=${d_rest%%.*}
# A prerelease minor reads as 28rc1 on either side, which is not an integer.
bw_minor=${bw_minor%%[![:digit:]]*}
d_minor=${d_minor%%[![:digit:]]*}

# Skipped unless all four parse, because the arithmetic would otherwise report a
# bash line number and lint anyway. An absent directive is that same case: it
# leaves both of its components empty. Bounded, because a digit string longer
# than the arithmetic accepts is the same failure as a non-digit one.
if [[ $bw_major =~ ^[0-9]{1,9}$ ]] && [[ $bw_minor =~ ^[0-9]{1,9}$ ]] \
  && [[ $d_major =~ ^[0-9]{1,9}$ ]] && [[ $d_minor =~ ^[0-9]{1,9}$ ]] \
  && { [ "$bw_major" -lt "$d_major" ] \
    || { [ "$bw_major" -eq "$d_major" ] && [ "$bw_minor" -lt "$d_minor" ]; }; }; then
  echo "golangci-lint was built with an older Go line than this module targets; not linting" >&2
  echo "  pinned     v$pin   ($version_file)" >&2
  echo "  installed  $(printf '%q' "$installed")    ($resolved)" >&2
  echo "  built with $(printf '%q' "$built_with")  ($directive_note)" >&2
  echo >&2
  echo "  rebuild it against the current toolchain:" >&2
  echo "    go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$pin" >&2
  exit 1
fi

exit 0
