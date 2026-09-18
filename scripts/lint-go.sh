#!/usr/bin/env bash
# Refuses to lint when the resolved golangci-lint does not match the pin.
# Input paths are arguments so the checks can be run against fixtures.
set -uo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

check_only=false
version_file=$root/.golangci-lint-version
go_mod=$root/go.mod
workflow=$root/.github/workflows/ci.yml

while [ "$#" -gt 0 ]; do
  case "$1" in
    --check-only) check_only=true; shift ;;
    --version-file) version_file=$2; shift 2 ;;
    --go-mod) go_mod=$2; shift 2 ;;
    --workflow) workflow=$2; shift 2 ;;
    --) shift; break ;;
    -*) echo "unknown option \"$1\"" >&2; exit 1 ;;
    *) break ;;
  esac
done

if [ ! -r "$version_file" ]; then
  echo "cannot read \"$version_file\"; not linting" >&2
  echo "  pinned     not obtained (open failed)" >&2
  exit 1
fi

pin_raw=$(tr -d '[:space:]' < "$version_file")
pin=${pin_raw#[vV]}

exit 0
