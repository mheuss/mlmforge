package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/spf13/cobra"
)

// loadWriterFor builds the writer tree load drives.
type loadWriterFor func(deps *treeDeps) treeLoadWriter

// newTreeCmd builds the tree command group against the real dependencies.
func newTreeCmd() *cobra.Command {
	return newTreeCmdWith(openTreeDeps,
		func(d *treeDeps) treeLoadWriter {
			return networkengine.NewTreeWriter(d.events, d.store, d.engine, d.locker)
		},
		func(d *treeDeps) treeWriter {
			return networkengine.NewTreeWriter(d.events, d.store, d.engine, d.locker)
		},
	)
}

// newTreeCmdWith builds the group over injectable seams.
func newTreeCmdWith(open depsOpener, loader loadWriterFor, writer writerFor) *cobra.Command {
	treeCmd := &cobra.Command{
		Use:   "tree",
		Short: "Tree persistence commands",
		Long:  "Commands that reach the tree persistence layer.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	dbURL := treeCmd.PersistentFlags().String("db-url", "", "PostgreSQL connection URL (or set DATABASE_URL env var)")
	worker := treeCmd.PersistentFlags().String("worker", "", "Path to the network-engine-worker binary (or set "+workerPathEnv+" env var)")

	// resolve reads both flags needed to open the tree dependencies.
	resolve := func() (dbTarget, string, error) {
		target, err := resolveDBURL(*dbURL)
		if err != nil {
			return dbTarget{}, "", err
		}
		path, err := resolveWorkerPath(*worker)
		if err != nil {
			return dbTarget{}, "", err
		}
		return target, path, nil
	}

	treeCmd.AddCommand(
		newTreeLoadCmd(resolve, open, loader),
		newTreeAddRootCmd(resolve, open, writer),
		newTreePlaceCmd(resolve, open, writer),
		newTreeRemoveCmd(resolve, open, writer),
	)
	return treeCmd
}

// flagResolver returns the database target and the worker path.
type flagResolver func() (dbTarget, string, error)

// runTreeCommand resolves the connection flags and runs one tree command under
// the command's own signal context.
func runTreeCommand(cmd *cobra.Command, resolve flagResolver, open depsOpener, run treeRunner) error {
	target, workerPath, err := resolve()
	if err != nil {
		return err
	}
	// Established here rather than on the root command, which would disable the
	// default SIGINT kill for every command in the binary.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return connectError(withTreeDeps(ctx, cmd.ErrOrStderr(), open, target.url, workerPath, run), target)
}

func newTreeLoadCmd(resolve flagResolver, open depsOpener, loader loadWriterFor) *cobra.Command {
	var treeID, treeType, spillover string
	var width int

	cmd := &cobra.Command{
		Use:   "load",
		Short: "Replay a stored tree into the engine",
		Long: "Opens a database pool, starts the engine worker, replays one stored tree, and exits. " +
			"When the tree's store is one event behind its stream, it first redelivers the stream's last event " +
			"and prints a redelivered line. " +
			"Exits 1 when the stream is two or more events past the store or behind it, when the tree has no projection row " +
			"and its stream is past version 1, when a type or matrix flag differs from the stream's first event, " +
			"or when the redelivery fails. A tree created before migration 000009 has no projection row, " +
			"so it is refused once its stream passes version 1. " +
			"The worker is started and stopped per invocation.",
		Args: cobra.NoArgs,
		// Moving this to the tree group leaves a real invocation dumping
		// usage after the error line.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := networkengine.LoadRequest{TreeID: treeID, TreeType: treeType}
			if cmd.Flags().Changed("matrix-width") {
				req.MatrixWidth = &width
			}
			if cmd.Flags().Changed("matrix-spillover") {
				req.MatrixSpillover = &spillover
			}
			return runTreeCommand(cmd, resolve, open, func(ctx context.Context, deps *treeDeps) error {
				return runTreeLoad(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), loader(deps), req)
			})
		},
	}
	cmd.Flags().StringVar(&treeID, "tree-id", "", "Tree to load (UUID)")
	cmd.Flags().StringVar(&treeType, "tree-type", "", "unilevel, binary or matrix")
	cmd.Flags().IntVar(&width, "matrix-width", 0, "Matrix width (matrix trees only)")
	cmd.Flags().StringVar(&spillover, "matrix-spillover", "", "Matrix spillover rule (matrix trees only)")
	_ = cmd.MarkFlagRequired("tree-id")
	_ = cmd.MarkFlagRequired("tree-type")
	return cmd
}
