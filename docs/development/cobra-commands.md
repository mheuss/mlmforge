# Cobra commands and their argument rules

Setting `Args` on a cobra command does not make it refuse anything. Whether the
rule runs depends on the command being runnable, and on no help flag being
present.

Read in cobra v1.10.2, measured against the built binary on 2026-09-21 and
2026-09-22 for HEU-834.

## A command with no run function ignores its `Args`

Cobra returns the help error for a command with neither `Run` nor `RunE` before
it validates arguments. The help is printed and the process exits 0.

So a group that only holds subcommands accepts anything:

```
mlmforge tree bogus      rc 0   help text, no error    (before HEU-834)
mlmforge tree bogus      rc 1   unknown command "bogus" (after)
```

Giving the group a `RunE` that prints help is what brings `Args` into reach.
`cobra.NoArgs` then does the refusing.

```go
Args: cobra.NoArgs,
RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
```

The root command behaves differently. It fails earlier, while cobra resolves
which command was meant, and that check applies only to a command with no
parent.

## A leaf with no `Args` takes whatever it is given

A runnable command with no `Args` rule accepts stray positional arguments and
runs. For `migrate up` that meant a mistyped argument applied the migration and
exited 0. Set `cobra.NoArgs` on every leaf that takes no positional arguments.

## `--help` bypasses the rule

Cobra handles the help flag before it validates arguments. So
`mlmforge migrate bogus --help` prints help and exits 0, even with `NoArgs`
set. Nothing runs. HEU-845 tracks whether that should change.

## How the binary holds new commands to this

The `cmd/mlmforge` tests walk the command tree. One walk sends an unknown
subcommand to every group. The other sends a stray argument to every command
with no subcommands. A command added without the rule fails one of them, with
no case to write by hand.

A leaf that genuinely takes a positional argument will fail the leaf walk. That
is the point to decide how the walk should treat it, not to delete the walk.

## An exit code says whether the operation asked for was performed

It does not say whether everything downstream of the operation succeeded.

1. `mlmforge tree bogus` exits 1. The operation asked for does not exist (HEU-834).
2. A run can succeed and still fail to release the worker and pool. That run exits 0. The release failure goes to stderr. A write command whose tree lock release failed does the same.
3. `tree place` can confirm its append and still fail to project. That run exits 0. The projection failure goes to stderr (HEU-301).

The same rule gives 1 when `tree place` cannot confirm its append. The operation is not known to have been performed. That applies the rule. It is not an exception to it.
