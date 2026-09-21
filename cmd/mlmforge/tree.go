package main

import (
	"context"

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

// loaderFor builds the loader a subcommand drives. Injected so a test can
// execute the command without a database or a worker.
type loaderFor func(deps *treeDeps) treeLoader

// newTreeCmd builds the tree command group against the real dependencies.
func newTreeCmd() *cobra.Command {
	return newTreeCmdWith(openTreeDeps, func(d *treeDeps) treeLoader {
		return networkengine.NewTreeLoader(d.store, d.engine)
	})
}

// newTreeCmdWith builds the group over injectable seams.
func newTreeCmdWith(open depsOpener, loader loaderFor) *cobra.Command {
	treeCmd := &cobra.Command{
		Use:   "tree",
		Short: "Tree persistence commands",
		Long:  "Commands that reach the tree persistence layer.",
		Args:  cobra.NoArgs,
	}

	dbURL := treeCmd.PersistentFlags().String("db-url", "", "PostgreSQL connection URL (or set DATABASE_URL env var)")
	worker := treeCmd.PersistentFlags().String("worker", "", "Path to the network-engine-worker binary (or set "+workerPathEnv+" env var)")

	// resolve reads both flags the way migrate reads its own.
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

	treeCmd.AddCommand(newTreeLoadCmd(resolve, open, loader))
	return treeCmd
}

// flagResolver returns the database URL and the worker path.
type flagResolver func() (string, string, error)

func newTreeLoadCmd(resolve flagResolver, open depsOpener, loader loaderFor) *cobra.Command {
	var treeID, treeType, spillover string
	var width int

	cmd := &cobra.Command{
		Use:   "load",
		Short: "Replay a stored tree into the engine",
		Long: "Opens a database pool, starts the engine worker, replays one stored tree, and exits. " +
			"The worker is started and stopped per invocation.",
		Args: cobra.NoArgs,
		// Moving these to the tree group leaves a real invocation dumping
		// usage after the operator message.
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			url, workerPath, err := resolve()
			if err != nil {
				return err
			}
			opts := loadTreeOptions(treeType, width, spillover)
			return withTreeDeps(cmd.Context(), open, url, workerPath,
				func(ctx context.Context, deps *treeDeps) error {
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
