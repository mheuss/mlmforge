#!/usr/bin/env bash
# Asserts what the lint wrapper reports, not only that it failed, so a deleted
# guard cannot hide behind a shared exit code.
set -uo pipefail

# cat's diagnostics are localized and one assertion matches on their text.
export LC_ALL=C

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
check=$root/scripts/lint-go.sh
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

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# A golangci-lint that prints a chosen version line and records how it was
# called. The success path is the one line no refusal test reaches.
stub_dir() {
  # Split, because under set -u bash declares every name in one local
  # statement before running its assignments, so $name is unset in $dir.
  local name=$1 line=$2 rc=${3:-0}
  local dir=$work/$name
  mkdir -p "$dir"
  cat > "$dir/golangci-lint" <<STUB
#!/usr/bin/env bash
if [ "\$1" = version ]; then
  printf '%s\n' '$line'
  exit $rc
fi
printf '%s\n' "\$@" > "\${STUB_ARGS:-/dev/null}"
[ -n "\${STUB_CALLS:-}" ] && echo x >> "\$STUB_CALLS"
exit \${STUB_RUN_RC:-0}
STUB
  chmod +x "$dir/golangci-lint"
  printf '%s' "$dir"
}

# Same as expect, with a stub directory prepended to PATH.
expect_with() {
  local dir=$1
  shift
  PATH="$dir:$PATH" expect "$@"
}

ok_line='golangci-lint has version 2.13.2 built with go1.27.0 from abc1234 on 2026-08-27T23:01:12Z'
old_line='golangci-lint has version 2.11.4 built with go1.26.1 from def5678 on 2026-06-01T00:00:00Z'

expect 1 "cannot read" "unreadable pin file" \
  --check-only --version-file "$data/nonexistent"
expect 1 "not obtained" "unreadable pin file marks pinned as not obtained" \
  --check-only --version-file "$data/nonexistent"

expect 1 "Is a directory" "a directory as the pin file keeps cat's reason" \
  --check-only --version-file "$data"

expect 1 "needs a file path" "option with no operand" \
  --check-only --version-file

expect 1 "is not a complete version" "partial pin" \
  --check-only --version-file "$data/lintver-partial"
expect 1 "is not a complete version" "latest pin" \
  --check-only --version-file "$data/lintver-latest"
expect 1 "is not a complete version" "empty pin file" \
  --check-only --version-file "$data/lintver-empty"
expect 1 "is not a complete version" "two-line pin file" \
  --check-only --version-file "$data/lintver-twolines"
# Pins the escaping in the regex. Dropping the backslashes leaves every other
# case green while the guard starts accepting a three-line pin.
expect 1 "is not a complete version" "non-dot separators" \
  --check-only --version-file "$data/lintver-separators"
expect 0 "" "bare pin without a leading v" \
  --check-only --version-file "$data/lintver-bare"
expect 0 "" "leading blank lines are trimmed like the action trims them" \
  --check-only --version-file "$data/lintver-leading"
# The only assertion that uses the script's own default paths. Without it a
# wrong filename or a wrong number of .. leaves the suite green and breaks
# every real invocation.
expect 0 "" "the default pin file is the tracked one" --check-only
a=$("$check" --check-only --version-file "$data/lintver-ok" 2>&1); arc=$?
b=$("$check" --check-only --version-file "$data/lintver-bare" 2>&1); brc=$?
if [ "$arc" = "$brc" ] && [ "$a" = "$b" ]; then
  record yes "a leading v changes nothing" ""
else
  record no "a leading v changes nothing" "rc $arc/$brc, out \"$a\" vs \"$b\""
fi

expect_with "$(stub_dir match "$ok_line")" 0 "" "version matches the pin" \
  --check-only --version-file "$data/lintver-ok"
drift_dir=$(stub_dir drift "$old_line")
expect_with "$drift_dir" 1 "installed  2.11.4" "version differs from the pin" \
  --check-only --version-file "$data/lintver-ok"
expect_with "$drift_dir" 1 "$drift_dir/golangci-lint" "the mismatch names the resolved path" \
  --check-only --version-file "$data/lintver-ok"
expect_with "$drift_dir" 1 "pinned     v2.13.2" "the mismatch names the pin" \
  --check-only --version-file "$data/lintver-ok"
expect_with "$(stub_dir badrc "$ok_line" 3)" 1 "version command exited 3" "version command fails" \
  --check-only --version-file "$data/lintver-ok"
expect_with "$(stub_dir garbage 'not a version line')" 1 "has 0 version lines" "version output has no version line" \
  --check-only --version-file "$data/lintver-ok"

# A stale binary whose stderr names the pinned version. Before the parse was
# anchored and stdout-only, this passed at rc=0 against a 2.11.4 binary.
noisy=$work/noisy
mkdir -p "$noisy"
cat > "$noisy/golangci-lint" <<'NOISY'
#!/usr/bin/env bash
printf '%s\n' 'warn: plugin has version 2.13.2 built with go1.27.0 (cached)' >&2
printf '%s\n' 'golangci-lint has version 2.11.4 built with go1.26.1 from x on y'
NOISY
chmod +x "$noisy/golangci-lint"
expect_with "$noisy" 1 "installed  2.11.4" "a log line naming the pin does not satisfy the check" \
  --check-only --version-file "$data/lintver-ok"

# Two version lines on stdout. Taking the first would report a version the
# binary may not be.
twolines=$work/twoversions
mkdir -p "$twolines"
cat > "$twolines/golangci-lint" <<'TWOV'
#!/usr/bin/env bash
printf '%s\n' 'golangci-lint has version 9.9.9 built with go1.20.0 from x on y'
printf '%s\n' 'golangci-lint has version 2.13.2 built with go1.27.0 from x on y'
TWOV
chmod +x "$twolines/golangci-lint"
expect_with "$twolines" 1 "has 2 version lines" "two version lines are refused" \
  --check-only --version-file "$data/lintver-ok"

# Pins the ^ anchor alone. The decoy is on stdout, so separating stderr does
# not help; only the anchor rejects it.
indented=$work/indented
mkdir -p "$indented"
cat > "$indented/golangci-lint" <<'IND'
#!/usr/bin/env bash
printf '%s\n' '  golangci-lint has version 9.9.9 built with go1.20.0 (from a plugin)'
printf '%s\n' 'golangci-lint has version 2.13.2 built with go1.27.0 from x on y'
IND
chmod +x "$indented/golangci-lint"
expect_with "$indented" 0 "" "an indented decoy line is not a version line" \
  --check-only --version-file "$data/lintver-ok"

# Pins the stderr separation alone. The decoy is anchored and well formed, so
# the anchor does not help; only reading stdout alone rejects it.
errdecoy=$work/errdecoy
mkdir -p "$errdecoy"
cat > "$errdecoy/golangci-lint" <<'ERRD'
#!/usr/bin/env bash
printf '%s\n' 'golangci-lint has version 9.9.9 built with go1.20.0 from x on y' >&2
printf '%s\n' 'golangci-lint has version 2.13.2 built with go1.27.0 from x on y'
ERRD
chmod +x "$errdecoy/golangci-lint"
expect_with "$errdecoy" 0 "" "a version line on stderr is not the binary's version" \
  --check-only --version-file "$data/lintver-ok"

# Pins the widened built-with capture. go[0-9.]+ would truncate rc1 away and
# report a value the binary did not print.
rcver=$work/rcver
mkdir -p "$rcver"
cat > "$rcver/golangci-lint" <<'RCV'
#!/usr/bin/env bash
printf '%s\n' 'golangci-lint has version 2.11.4 built with go1.28rc1 from x on y'
RCV
chmod +x "$rcver/golangci-lint"
expect_with "$rcver" 1 "built with go1.28rc1" "a prerelease toolchain is reported whole" \
  --check-only --version-file "$data/lintver-ok"

# An empty directory as the whole PATH, so command -v resolves nothing.
empty=$work/emptypath
mkdir -p "$empty"
# /usr/bin and /bin so the script's own `env bash` shebang still resolves.
# golangci-lint is in neither; it lives in ~/.local/bin on this machine.
out=$(PATH="$empty:/usr/bin:/bin" "$check" --check-only --version-file "$data/lintver-ok" 2>&1)
rc=$?
if [ "$rc" = 1 ] && [[ $out == *"not obtained"* ]] && [[ $out == *"v2.13.2"* ]]; then
  record yes "no binary on PATH" ""
else
  record no "no binary on PATH" "rc=$rc: $out"
fi
# Pins the v-strip on the installed version. Real golangci-lint prints no
# leading v, so nothing else would notice if the strip were removed.
v_line='golangci-lint has version v2.13.2 built with go1.27.0 from abc1234 on 2026-08-27T23:01:12Z'
expect_with "$(stub_dir vprefixed "$v_line")" 0 "" "a binary reporting a leading v still matches" \
  --check-only --version-file "$data/lintver-ok"
expect_with "$(stub_dir drift2 "$old_line")" 1 "go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2" \
  "the mismatch names the install command" --check-only --version-file "$data/lintver-ok"

# These cases compare the whole output, because some mutations here change only
# what lands on stderr and leave the exit code alone.
expect_silent() {
  local dir=$1 name=$2 out rc
  shift 2
  out=$(PATH="$dir:$PATH" "$check" "$@" 2>&1)
  rc=$?
  if [ "$rc" = 0 ] && [ -z "$out" ]; then
    record yes "$name" ""
  else
    record no "$name" "rc=$rc, out \"$out\""
  fi
}

bw_dir=$(stub_dir bw "$ok_line")
expect_silent "$bw_dir" "built-with go line meets the directive" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-ok"
expect_with "$bw_dir" 1 "older Go line than this module targets" "built-with go line below the directive" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-ahead"
# Major and minor only. A full-string or patch-aware comparison refuses this,
# because the binary's patch is below the directive's while the line matches.
expect_silent "$bw_dir" "a directive patch above the binary's is not a mismatch" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-patch-ahead"

# Pins the ^ anchor in the awk program. Without it the first line holding
# "go " is a comment, whose second field is the word go, and the comparison
# then errors instead of refusing.
expect_with "$bw_dir" 1 "go directive is 1.28.0" "a commented go line is not the directive" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-comment"
# Pins the exit in the awk program. Both directives are behind the binary, so
# dropping it still refuses and only the reported value changes.
expect_with "$bw_dir" 1 "go directive is 1.28.0)" "only the first go line is the directive" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-twogo"
# Pins the guard on an obtained directive. Without it the comparison runs on an
# empty value and bash reports it, at the same exit code.
expect_silent "$bw_dir" "a go.mod with no directive skips the comparison" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-nodirective"
# Pins awk's stderr redirect. Without it the missing file is reported on a run
# that is meant to lint.
expect_silent "$bw_dir" "a missing go.mod skips the comparison" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-nonexistent"

# A prerelease built-with whose version matches the pin. Comparing the minor
# component whole errors on rc1 and lints anyway.
rc_match='golangci-lint has version 2.13.2 built with go1.28rc1 from x on y'
expect_silent "$(stub_dir bwrc "$rc_match")" "a prerelease built-with above the directive is not a mismatch" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-ok"

# The two cases that vary the major. Without them every fixture holds major 1
# on both sides, so the major comparison and its equality test are unpinned:
# reversing the first or removing the second changes no result.
expect_with "$bw_dir" 1 "go directive is 2.0.0" "a directive a major ahead refuses" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-major-ahead"
major_ahead='golangci-lint has version 2.13.2 built with go2.0.0 from x on y'
expect_silent "$(stub_dir bwmajor "$major_ahead")" "a binary a major ahead is not a mismatch" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-ahead"

# The note is only ever printed by a refusal, so reaching it needs a version
# mismatch as well. Without its own guard the note names a directive it did not
# read, as an empty value in a sentence that says it has one.
expect_with "$drift_dir" 1 "go directive not obtained" "an absent directive is reported as absent" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-nodirective"

# The directive carries a prerelease too, and Go writes that form. Truncating
# only the binary's side leaves this refusal unreachable.
expect_with "$bw_dir" 1 "older Go line than this module targets" "a prerelease directive above the binary refuses" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-rc"
# Both components are unparsable here, so this pins the combination rather than
# either test. The two cases below isolate them one at a time.
expect_silent "$bw_dir" "an unparsable directive skips the comparison" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-unparsable"

# The binary's own prerelease minor has to survive truncation into a number.
# Without it this refusal is skipped as unparsable instead.
bw_rc_behind='golangci-lint has version 2.13.2 built with go1.26rc1 from x on y'
expect_with "$(stub_dir bwrcbehind "$bw_rc_behind")" 1 "older Go line than this module targets" \
  "a prerelease binary below the directive refuses" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-ok"

# Each parse test is the only one that can refuse its own input, so each needs
# an input where the others pass. Without one, a test can be deleted and every
# case stays green.
expect_silent "$(stub_dir bwmajorbad 'golangci-lint has version 2.13.2 built with gox.27 from x on y')" \
  "an unparsable built-with major skips the comparison" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-ok"
expect_silent "$(stub_dir bwminorbad 'golangci-lint has version 2.13.2 built with go1.rc from x on y')" \
  "an unparsable built-with minor skips the comparison" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-ok"
expect_silent "$bw_dir" "an unparsable directive major skips the comparison" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-major-unparsable"
expect_silent "$bw_dir" "an unparsable directive minor skips the comparison" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-minor-unparsable"
# All digits and still not a number the arithmetic accepts, which is the same
# failure an unparsable value causes.
expect_silent "$bw_dir" "a directive too long to compare skips the comparison" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-huge"

wf_args=(--check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-ok")
expect_with "$bw_dir" 1 "sets both version and version-file" "workflow sets both inputs" \
  "${wf_args[@]}" --workflow "$data/wf-lint-both.yml"
expect_silent "$bw_dir" "workflow sets version-file alone" \
  "${wf_args[@]}" --workflow "$data/wf-lint-file-only.yml"
expect_silent "$bw_dir" "workflow sets version alone" \
  "${wf_args[@]}" --workflow "$data/wf-lint-version-only.yml"
# Two steps, one key each. Counting across the file rather than within a step
# refuses this, and the message would name a step that does not exist.
expect_silent "$bw_dir" "two steps with one input each are not both" \
  "${wf_args[@]}" --workflow "$data/wf-lint-two-steps.yml"
# Commenting a key out is how someone switches the pin over, so it has to be
# read as absent rather than as present.
expect_silent "$bw_dir" "a commented-out version is not set" \
  "${wf_args[@]}" --workflow "$data/wf-lint-both-commented.yml"
# Another action's step carries both key names. Only this action's step counts.
expect_silent "$bw_dir" "another action setting both inputs is not this one" \
  "${wf_args[@]}" --workflow "$data/wf-lint-other-action.yml"
# The same, with a later item so the close rule decides it rather than the END
# clause. Both rules carry their own check that the item was this action's.
expect_silent "$bw_dir" "another action's item is closed without refusing" \
  "${wf_args[@]}" --workflow "$data/wf-lint-other-action-first.yml"
# Tabs are not legal YAML indentation, so this is malformed input rather than a
# shape to support. It is here because scanning for the first non-space alone
# reports every tab-indented line at indent 0, which ends the item at its first
# nested list and hides both keys.
expect_with "$bw_dir" 1 "sets both version and version-file" "a tab-indented step is still one item" \
  "${wf_args[@]}" --workflow "$data/wf-lint-tab-indent.yml"
# A commented-out uses: still contains the action name, and the name match is
# not anchored, so the step would be entered from a line that is not there.
expect_silent "$bw_dir" "a commented-out uses is not this action's step" \
  "${wf_args[@]}" --workflow "$data/wf-lint-commented-uses.yml"
# The step has to end at the next list item. Without that, a later step's key
# counts toward this one and the refusal names a step that sets one input.
expect_silent "$bw_dir" "a key in the next step does not count toward this one" \
  "${wf_args[@]}" --workflow "$data/wf-lint-key-after-step.yml"
# A list nested under one of the step's own keys is indented deeper than the
# step. Ending the step there hides both keys being set on it.
expect_with "$bw_dir" 1 "sets both version and version-file" "a nested list does not end the step" \
  "${wf_args[@]}" --workflow "$data/wf-lint-nested-list.yml"
# A mapping's keys come in any order, and the action's own key is one of them.
# Anchoring on uses: drops every key written above it.
expect_with "$bw_dir" 1 "sets both version and version-file" "keys before uses are still this step's" \
  "${wf_args[@]}" --workflow "$data/wf-lint-keys-before-uses.yml"
# The compact form puts the list marker and uses: on one line, so the rule that
# closes the previous item has to run before the one that opens this one. It
# also puts the item marker at the same indent as uses:, which is the only
# shape that tells the boundary's <= from a <.
expect_with "$bw_dir" 1 "sets both version and version-file" "a compact uses closes the item before it" \
  "${wf_args[@]}" --workflow "$data/wf-lint-compact-uses.yml"
# Quoting a key is valid YAML and leaves the name unchanged.
expect_with "$bw_dir" 1 "sets both version and version-file" "quoted key names are the same keys" \
  "${wf_args[@]}" --workflow "$data/wf-lint-quoted-keys.yml"
# A directory can be opened and not read. The pin file refuses on this and the
# workflow read did not, so it skipped the guard and leaked grep's diagnostic.
expect 1 "Is a directory" "a directory as the workflow keeps cat's reason" \
  --check-only --version-file "$data/lintver-ok" --go-mod "$data/lintmod-ok" --workflow "$data"
expect_silent "$bw_dir" "a missing workflow skips the check" \
  "${wf_args[@]}" --workflow "$data/wf-lint-nonexistent.yml"

echo "$pass passed, $fail failed, of $((pass + fail))"
[ "$fail" = 0 ]
