#!/usr/bin/env bash
# Fails when the CI, go.mod and mise.toml Go version pins disagree. Paths are
# arguments so the checks can be run against fixtures. Defaults are anchored to
# the repo rather than the working directory, so it runs from anywhere.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

if [ "$#" -gt 3 ]; then
  echo "usage: ${0##*/} [workflow-file] [go-mod-file] [mise-file]" >&2
  exit 1
fi

wf=${1:-$root/.github/workflows/ci.yml}
mod=${2:-$root/go.mod}
mise=${3:-$root/mise.toml}

for f in "$wf" "$mod" "$mise"; do
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

# Tracks the current table, so a key is only read under the table that makes
# mise act on it. A bare key match would accept go = under [env], which pins
# nothing and would still be reported as tools.go.
mise_read() {
  awk -v want="$1" -v key="$2" '
    /^[[:space:]]*\[/ { sect=$0; sub(/#.*$/, "", sect); gsub(/[[:space:]]/, "", sect); next }
    sect == want && $0 ~ "^[[:space:]]*\"?" key "\"?[[:space:]]*=" {
      sub(/^[^=]*=[[:space:]]*/, "")
      sub(/[[:space:]]*#.*$/, "")
      gsub(/["\047[:space:]]/, "")
      print
    }' "$3"
}

mise_count() {
  awk -v want="$1" -v key="$2" '
    /^[[:space:]]*\[/ { sect=$0; sub(/#.*$/, "", sect); gsub(/[[:space:]]/, "", sect); next }
    sect == want && $0 ~ "^[[:space:]]*\"?" key "\"?[[:space:]]*=" { n++ }
    END { print n + 0 }' "$3"
}

mise_go=$(mise_read "[tools]" go "$mise")
mise_gotoolchain_raw=$(mise_read "[env]" GOTOOLCHAIN "$mise")
mise_gotoolchain=${mise_gotoolchain_raw#go}
mise_go_lines=$(mise_count "[tools]" go "$mise")
mise_env_lines=$(mise_count "[env]" GOTOOLCHAIN "$mise")

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

if [ "$mise_go_lines" -gt 1 ] || [ "$mise_env_lines" -gt 1 ]; then
  echo "\"$mise\" has $mise_go_lines tools.go and $mise_env_lines env.GOTOOLCHAIN lines; this check reads one of each" >&2
  exit 1
fi

if [ -z "$mise_go" ]; then
  echo "\"$mise\" has no go under [tools]" >&2
  exit 1
fi

if [ -z "$mise_gotoolchain" ]; then
  echo "\"$mise\" has no GOTOOLCHAIN under [env]" >&2
  exit 1
fi

if [ "$mise_go" != "$mod_toolchain" ]; then
  echo "\"$mise\" tools.go is \"$mise_go\"; \"$mod\" toolchain is \"$mod_toolchain_raw\"" >&2
  exit 1
fi

if [ "$mise_gotoolchain" != "$mod_toolchain" ]; then
  echo "\"$mise\" env.GOTOOLCHAIN is \"$mise_gotoolchain_raw\"; \"$mod\" toolchain is \"$mod_toolchain_raw\"" >&2
  exit 1
fi

echo "\"$wf\" go-version $ci_go; \"$mod\" toolchain $mod_toolchain_raw, go directive $mod_lang; \"$mise\" tools.go $mise_go, env.GOTOOLCHAIN $mise_gotoolchain_raw"
