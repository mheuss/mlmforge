package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mlmforge/mlmforge/internal/networkengine"
)

// treeDeps is what one tree subcommand needs for one invocation.
type treeDeps struct {
	store   networkengine.TreeStore
	engine  networkengine.TreeEngine
	release func() error
}

// depsOpener builds the dependencies for one invocation. A non-nil treeDeps
// must carry a non-nil release.
type depsOpener func(ctx context.Context, dbURL, workerPath string) (*treeDeps, error)

// reachablePool is narrowed to the two methods startEngine calls, so a test
// can observe the release without building a pool.
type reachablePool interface {
	Ping(ctx context.Context) error
	Close()
}

// startEngine reaches the database and then starts the worker, releasing pool
// if either fails.
//
// The database is checked first because it is the cheaper of the two to reach
// and the only one that costs a subprocess to find out about.
func startEngine(ctx context.Context, workerPath string, pool reachablePool) (*networkengine.EngineClient, error) {
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("reach database: %w", err)
	}
	engine, err := networkengine.NewEngineClient(ctx, workerPath)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("start worker at %s: %w", workerPath, err)
	}
	return engine, nil
}

// treeRunner is one tree operation.
type treeRunner func(ctx context.Context, deps *treeDeps) error

// openTreeDeps opens a pool and starts the worker.
func openTreeDeps(ctx context.Context, dbURL, workerPath string) (*treeDeps, error) {
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}
	engine, err := startEngine(ctx, workerPath, pool)
	if err != nil {
		return nil, err
	}
	return &treeDeps{
		store:  networkengine.NewPostgresTreeStore(pool),
		engine: engine,
		release: func() error {
			stopErr := engine.Stop()
			pool.Close()
			if stopErr != nil {
				return fmt.Errorf("stop worker: %w", stopErr)
			}
			return nil
		},
	}, nil
}

// withTreeDeps runs one operation and releases the dependencies afterwards.
func withTreeDeps(ctx context.Context, open depsOpener, dbURL, workerPath string, run treeRunner) (err error) {
	deps, err := open(ctx, dbURL, workerPath)
	if err != nil {
		return err
	}
	// Deferred rather than called after run, so a panic releases too.
	defer func() {
		err = errors.Join(err, deps.release())
	}()
	return run(ctx, deps)
}
