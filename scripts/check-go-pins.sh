#!/usr/bin/env bash
# Fails when the Go version pins disagree. A CI Go newer than go.mod's
# toolchain directive raises no error, so the two drift apart green.
#
# Paths are arguments so the checks can be run against fixtures. Defaults are
# anchored to the repo rather than the working directory, so it runs from
# anywhere.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

if [ "$#" -gt 2 ]; then
  echo "usage: ${0##*/} [workflow-file] [go-mod-file]" >&2
  exit 1
fi

wf=${1:-$root/.github/workflows/ci.yml}
mod=${2:-$root/go.mod}

for f in "$wf" "$mod"; do
  if [ ! -r "$f" ]; then
    echo "cannot read \"$f\"" >&2
    exit 1
  fi
done

# Anchored so a commented-out or inline mention does not match. \047 is an
# apostrophe, so the value is read whether it is bare, single- or double-quoted.
ci_go=$(awk '/^[[:space:]]*go-version:/ {
  sub(/^[^:]*:[[:space:]]*/, "")
  sub(/[[:space:]]*#.*$/, "")
  gsub(/["\047[:space:]]/, "")
  print
}' "$wf")
ci_go_lines=$(grep -cE '^[[:space:]]*go-version:' "$wf" || true)

mod_toolchain_raw=$(awk '/^toolchain /{print $2}' "$mod")
mod_toolchain=${mod_toolchain_raw#go}
mod_lang=$(awk '/^go /{print $2}' "$mod")

if [ "$ci_go_lines" -gt 1 ]; then
  echo "\"$wf\" has $ci_go_lines go-version lines; this check reads one" >&2
  exit 1
fi

if [ "$ci_go_lines" -eq 0 ]; then
  echo "no go-version line in \"$wf\"" >&2
  exit 1
fi

if [ -z "$ci_go" ]; then
  echo "the go-version line in \"$wf\" has an empty value" >&2
  exit 1
fi

if [ -z "$mod_lang" ] || [ -z "$mod_toolchain" ]; then
  echo "\"$mod\" go directive is \"$mod_lang\" and toolchain directive is \"$mod_toolchain_raw\"; both are required here" >&2
  exit 1
fi

if [ "$ci_go" != "$mod_toolchain" ]; then
  echo "\"$wf\" go-version is \"$ci_go\"; \"$mod\" toolchain is \"$mod_toolchain_raw\"" >&2
  exit 1
fi

if ! printf '%s\n%s\n' "$mod_lang" "$mod_toolchain" | sort -V -C; then
  echo "\"$mod\" go directive is \"$mod_lang\"; its toolchain is \"$mod_toolchain_raw\"" >&2
  exit 1
fi

echo "\"$wf\" go-version $ci_go; \"$mod\" toolchain $mod_toolchain_raw, go directive $mod_lang"
