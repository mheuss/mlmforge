#!/usr/bin/env bash
# One definition of the Go vulnerability scan, so CI and the local audit
# command cannot drift in what they cover.
#
# --list prints the packages that would be scanned and exits. The scan needs a
# network and a real module graph, so it cannot be run against fixtures. Which
# packages it covers can, and that is the half with a silent failure: a pattern
# that excludes too much reports clean over less code than you think.
#
# The module root is an argument so the package selection can be exercised
# against a fixture module rather than this repo.
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

# Exclude internal/testutil from scan — test-only infrastructure
# that pulls in docker/docker via testcontainers-go. These vulns
# are in the Docker daemon, not the client, and the package never
# ships in any binary.
#
# Matched whole-line against the full import path. A substring match would also
# drop a package whose name merely contains this one.
mapfile -t packages < <(printf '%s\n' "$all" | grep -vxF "$module/internal/testutil" || true)

if [ "${#packages[@]}" -eq 0 ]; then
  echo "no packages left after excluding \"$module/internal/testutil\" from $(printf '%s\n' "$all" | grep -c .) listed" >&2
  exit 1
fi

if [ "$list_only" = true ]; then
  printf '%s\n' "${packages[@]}"
  exit 0
fi

cd "$root" && govulncheck "${packages[@]}"
