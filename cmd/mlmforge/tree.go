package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/spf13/cobra"
)

// loadTreeOptions turns the matrix flags into loader options.
func loadTreeOptions(treeType string, width int, spillover string) []networkengine.LoadTreeOption {
	if treeType != "matrix" {
		return nil
	}
	return []networkengine.LoadTreeOption{networkengine.WithMatrixParams(width, spillover)}
}

// loaderFor builds the loader a subcommand drives.
type loaderFor func(deps *treeDeps) treeLoader

// newTreeCmd builds the tree command group against the real dependencies.
func newTreeCmd() *cobra.Command {
	return newTreeCmdWith(openTreeDeps,
		func(d *treeDeps) treeLoader {
			return networkengine.NewTreeLoader(d.store, d.engine)
		},
		func(d *treeDeps) treeWriter {
			return networkengine.NewTreeWriter(d.events, d.store, d.engine, d.locker)
		},
	)
}

// newTreeCmdWith builds the group over injectable seams.
func newTreeCmdWith(open depsOpener, loader loaderFor, writer writerFor) *cobra.Command {
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
	resolve := func() (string, string, error) {
		url, err := resolveDBURL(*dbURL)
		if err != nil {
			return "", "", err
		}
		path, err := resolveWorkerPath(*worker)
		if err != nil {
			return "", "", err
		}
		return url, path, nil
	}

	treeCmd.AddCommand(
		newTreeLoadCmd(resolve, open, loader),
		newTreeAddRootCmd(resolve, open, writer),
		newTreePlaceCmd(resolve, open, writer),
		newTreeRemoveCmd(resolve, open, writer),
	)
	return treeCmd
}

// flagResolver returns the database URL and the worker path.
type flagResolver func() (string, string, error)

// runTreeCommand resolves the connection flags and runs one tree command under
// the command's own signal context.
func runTreeCommand(cmd *cobra.Command, resolve flagResolver, open depsOpener, run treeRunner) error {
	url, workerPath, err := resolve()
	if err != nil {
		return err
	}
	// Established here rather than on the root command, which would disable the
	// default SIGINT kill for every command in the binary.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return withTreeDeps(ctx, cmd.ErrOrStderr(), open, url, workerPath, run)
}

func newTreeLoadCmd(resolve flagResolver, open depsOpener, loader loaderFor) *cobra.Command {
	var treeID, treeType, spillover string
	var width int

	cmd := &cobra.Command{
		Use:   "load",
		Short: "Replay a stored tree into the engine",
		Long: "Opens a database pool, starts the engine worker, replays one stored tree, and exits. " +
			"The worker is started and stopped per invocation.",
		Args: cobra.NoArgs,
		// Moving this to the tree group leaves a real invocation dumping
		// usage after the error line.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := loadTreeOptions(treeType, width, spillover)
			return runTreeCommand(cmd, resolve, open, func(ctx context.Context, deps *treeDeps) error {
				return runTreeLoad(ctx, cmd.OutOrStdout(), loader(deps), treeID, treeType, opts)
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
