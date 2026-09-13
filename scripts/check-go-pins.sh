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

# go-version counts only under an actions/setup-go step's with, never a matrix
# or env entry and never a commented-out line. An inline with mapping is not
# read, so it fails the empty case rather than passing it. \047 is an
# apostrophe, so the value is read bare, single- or double-quoted.
setup_go_pins() {
  awk '
    function indent(s,   i) { i = match(s, /[^[:space:]]/); return i ? i - 1 : -1 }
    function flush() {
      if (in_step && has_setup && got) print val
      in_step = 0; has_setup = 0; got = 0; val = ""; in_with = 0
    }
    /^[[:space:]]*$/ { next }
    /^[[:space:]]*#/ { next }
    {
      here = indent($0)
      if ($0 ~ /^[[:space:]]*-[[:space:]]*(uses|name|run|id):/) {
        flush()
        in_step = 1
        step_indent = here
      } else if (in_step && here <= step_indent) {
        flush()
      }
      if (!in_step) next
      if (in_with && here <= with_indent) in_with = 0
      if ($0 ~ /uses:[[:space:]]*actions\/setup-go/) has_setup = 1
      if ($0 ~ /^[[:space:]]*with:[[:space:]]*$/) { in_with = 1; with_indent = here; next }
      if (in_with && $0 ~ /^[[:space:]]*go-version:/) {
        val = $0
        sub(/^[^:]*:[[:space:]]*/, "", val)
        sub(/[[:space:]]*#.*$/, "", val)
        gsub(/["\047[:space:]]/, "", val)
        got = 1
      }
    }
    END { flush() }' "$1"
}

ci_go=$(setup_go_pins "$wf")
ci_go_lines=$(setup_go_pins "$wf" | grep -c '' || true)
wf_go_version_lines=$(grep -cE '^[[:space:]]*go-version:' "$wf" || true)
# Counted from the raw file rather than from setup_go_pins. A step whose first
# key is not uses, name, run or id is invisible to that parser, and a bare dash
# has no key at all, so a second pin could go uncompared and still report one.
wf_setup_go_steps=$(grep -cE 'uses:[[:space:]]*actions/setup-go' "$wf" || true)

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

if [ "$wf_setup_go_steps" -gt 1 ]; then
  echo "\"$wf\" has $wf_setup_go_steps actions/setup-go steps; this check reads one" >&2
  exit 1
fi

if [ "$ci_go_lines" -eq 0 ]; then
  if [ "$wf_go_version_lines" -gt 0 ]; then
    echo "\"$wf\" go-version keys found: $wf_go_version_lines; under with: in an actions/setup-go step: 0" >&2
  else
    echo "no go-version line in \"$wf\"" >&2
  fi
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
