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

# Reading is the check. A readable directory passes -r and then fails the read,
# which exits 0 when the status is not tested.
if ! pin_body=$(cat "$version_file" 2>/dev/null); then
  echo "cannot read \"$version_file\"; not linting" >&2
  echo "  pinned     not obtained (read failed)" >&2
  exit 1
fi

# Surrounding whitespace only. Deleting all of it would accept "2.13. 2", which
# the action reads as a different string than this script would.
pin_raw=$(printf '%s' "$pin_body" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')
pin=${pin_raw#[vV]}

exit 0
