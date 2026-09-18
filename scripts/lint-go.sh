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

# A directory can be opened and not read, so the read's own status is the check.
# cat's stderr is left alone because it names which of the two happened.
if ! pin_body=$(cat "$version_file"); then
  echo "cannot read \"$version_file\"; not linting" >&2
  echo "  pinned     not obtained (cat exited non-zero)" >&2
  exit 1
fi

# Trims the whole value rather than each line, so a leading newline goes too.
# Interior whitespace stays: collapsing it accepts a pin that is not the pin.
pin_raw=$pin_body
pin_raw=${pin_raw#"${pin_raw%%[![:space:]]*}"}
pin_raw=${pin_raw%"${pin_raw##*[![:space:]]}"}
pin=${pin_raw#[vV]}

exit 0
