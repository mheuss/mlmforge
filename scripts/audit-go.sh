#!/usr/bin/env bash
# Runs the Go vulnerability scan. --list prints the packages it would scan and
# exits, without needing a network or a real module graph. The module root is
# an argument so that listing can be run against a fixture.
set -euo pipefail

list_only=false
if [ "${1:-}" = --list ]; then
  list_only=true
  shift
fi

if [ "$#" -gt 1 ]; then
  echo "usage: ${0##*/} [--list] [module-root]" >&2
  exit 1
fi

case "${1:-}" in
  -*)
    echo "unknown flag \"$1\"; usage: ${0##*/} [--list] [module-root]" >&2
    exit 1
    ;;
esac

root=${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}

if [ ! -r "$root/go.mod" ]; then
  echo "no readable go.mod under \"$root\"" >&2
  exit 1
fi

if ! module=$(cd "$root" && go list -m); then
  echo "go list -m exited non-zero in \"$root\"" >&2
  exit 1
fi

# A process substitution swallows its own exit status, so the list is captured
# first and its status read before anything is filtered. A go list that fails
# partway prints some packages and exits non-zero, which would otherwise shrink
# the scan silently.
if ! all=$(cd "$root" && go list ./...); then
  echo "go list ./... exited non-zero in \"$root\"" >&2
  exit 1
fi

# go list exits 0 with empty output when nothing matches, warning only on
# stderr. Checked before filtering, because printf on an empty string emits a
# blank line and an array holding one empty string is not an empty array.
if [ -z "$all" ]; then
  echo "go list ./... produced no packages in \"$root\"" >&2
  exit 1
fi

# internal/testutil is test-only and never ships in a binary. ADR-025 carries
# what it pulls in and why those advisories are accepted.
#
# Matched whole-line against the full import path. A substring match would also
# drop a package whose name merely contains this one.
listed_count=$(printf '%s\n' "$all" | grep -c .)
filtered=$(printf '%s\n' "$all" | grep -vxF "$module/internal/testutil") || grep_rc=$?
if [ "${grep_rc:-0}" -gt 1 ]; then
  echo "grep exited $grep_rc while filtering $listed_count listed packages in \"$root\"" >&2
  exit 1
fi
mapfile -t packages < <(printf '%s' "${filtered:+$filtered$'\n'}")

if [ "${#packages[@]}" -eq 0 ]; then
  echo "no packages left after excluding \"$module/internal/testutil\" from $listed_count listed" >&2
  exit 1
fi

if [ "$list_only" = true ]; then
  printf '%s\n' "${packages[@]}"
  exit 0
fi

cd "$root" && govulncheck "${packages[@]}"
