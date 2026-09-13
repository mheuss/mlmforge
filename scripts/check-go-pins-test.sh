#!/usr/bin/env bash
# Asserts what the pin check reports, not only that it failed, so a deleted
# guard cannot hide behind a shared exit code.
set -uo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
check=$root/scripts/check-go-pins.sh
data=$root/scripts/testdata

pass=0
fail=0

record() {
  local ok=$1 name=$2 detail=$3
  if [ "$ok" = yes ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    echo "FAIL $name: $detail"
  fi
}

# Asserts the exit code and that the output contains a phrase naming the guard.
expect() {
  local want_rc=$1 want_msg=$2 name=$3 wf=$4 mod=$5 mise=${6:-mise-ok.toml} out rc
  out=$("$check" "$data/$wf" "$data/$mod" "$data/$mise" 2>&1)
  rc=$?
  if [ "$rc" != "$want_rc" ]; then
    record no "$name" "wanted rc=$want_rc, got rc=$rc: $out"
  elif [ -n "$want_msg" ] && [[ $out != *"$want_msg"* ]]; then
    record no "$name" "wanted output containing \"$want_msg\", got: $out"
  else
    record yes "$name" ""
  fi
}

# Same, for invocations the fixture pair cannot express.
expect_raw() {
  local want_rc=$1 want_msg=$2 name=$3 out rc
  shift 3
  out=$("$check" "$@" 2>&1)
  rc=$?
  if [ "$rc" != "$want_rc" ]; then
    record no "$name" "wanted rc=$want_rc, got rc=$rc: $out"
  elif [ -n "$want_msg" ] && [[ $out != *"$want_msg"* ]]; then
    record no "$name" "wanted output containing \"$want_msg\", got: $out"
  else
    record yes "$name" ""
  fi
}

expect 0 "go-version 1.27.1" "matching pins"                wf-ok.yml            mod-ok
expect 0 "go-version 1.27.1" "unquoted go-version"          wf-unquoted.yml      mod-ok
expect 0 "go-version 1.27.1" "single-quoted go-version"     wf-single-quoted.yml mod-ok
expect 0 "go-version 1.27.1" "commented and inline mention" wf-commented.yml     mod-ok
expect 0 "go-version 1.27.1" "go directive equals toolchain" wf-ok.yml           mod-equal

expect 1 "go-version is \"1.27.9\""      "ci pin raised alone"          wf-drift.yml     mod-ok
expect 1 "go directive is \"1.28.0\""    "go directive above toolchain" wf-ok.yml        mod-lang-above-toolchain
expect 1 "toolchain directive is \"\""   "toolchain directive missing"  wf-ok.yml        mod-no-toolchain
expect 1 "go directive is \"\""          "go directive missing"         wf-ok.yml        mod-no-go-directive
expect 1 "toolchain is \"default\""      "toolchain default"            wf-ok.yml        mod-toolchain-default
expect 1 "no go-version line"            "no go-version line"           wf-no-pin.yml    mod-ok
expect 1 "has 2 go-version lines"        "two go-version lines"         wf-two-pins.yml  mod-ok
expect 1 "has an empty value"            "empty go-version value"       wf-empty.yml     mod-ok
expect 1 "go-version is \"1.27\""        "partial version is drift"     wf-partial.yml   mod-ok

expect 0 "tools.go 1.27.1"               "mise inline comments"         wf-ok.yml mod-ok mise-commented.toml
expect 0 "tools.go 1.27.1"               "mise header comment, quoted and indented key" wf-ok.yml mod-ok mise-header-comment.toml

expect 1 "tools.go is \"1.27.9\""        "mise tools.go drifted"        wf-ok.yml mod-ok mise-tools-drift.toml
expect 1 "env.GOTOOLCHAIN is \"go1.27.9\"" "mise GOTOOLCHAIN drifted"   wf-ok.yml mod-ok mise-env-drift.toml
expect 1 "no GOTOOLCHAIN under [env]"    "mise env section missing"     wf-ok.yml mod-ok mise-no-env.toml
expect 1 "no go under [tools]"           "mise tools section missing"   wf-ok.yml mod-ok mise-no-tools.toml
expect 1 "no go under [tools]"           "mise keys in the wrong table" wf-ok.yml mod-ok mise-wrong-table.toml
expect 1 "no go under [tools]"           "mise file with no tables"     wf-ok.yml mod-ok mise-no-tables.toml
expect 1 "has 2 tools.go"                "mise duplicate tools.go"      wf-ok.yml mod-ok mise-two-tools.toml
expect 1 "and 2 env.GOTOOLCHAIN"         "mise duplicate GOTOOLCHAIN"   wf-ok.yml mod-ok mise-two-env.toml

expect_raw 1 "cannot read"  "unreadable path"     "$data/wf-ok.yml" "$data/nonexistent" "$data/mise-ok.toml"
expect_raw 1 "usage:"       "too many arguments"  "$data/wf-ok.yml" "$data/mod-ok" "$data/mise-ok.toml" junk

# The defaults are anchored to the script, not the caller's directory. Running
# from the repo root would pass either way, so this runs from somewhere else.
out=$(cd "$root/engine" && "$check" 2>&1)
rc=$?
if [ "$rc" = 0 ]; then
  record yes "defaults resolve from another directory" ""
else
  record no "defaults resolve from another directory" "rc=$rc: $out"
fi

echo "$pass passed, $fail failed, of $((pass + fail))"
[ "$fail" = 0 ]
