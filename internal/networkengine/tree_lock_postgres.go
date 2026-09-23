package networkengine

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// treeLockNamespace is the first advisory lock key of every tree write lock.
// Its four bytes spell "tree" in ASCII.
const treeLockNamespace int32 = 0x74726565

// treeLockPollInterval is the pause between two attempts on a held lock.
const treeLockPollInterval = 100 * time.Millisecond

// treeLockApplicationName names every lock connection in pg_stat_activity.
const treeLockApplicationName = "mlmforge-tree-lock"

// treeUnlockTimeout bounds the unlock call and the close that follows it.
const treeUnlockTimeout = 5 * time.Second

// treeLockKey derives the second advisory lock key from a tree ID.
//
// Two binaries that derive different keys for one tree do not exclude each
// other, so this function must not change.
func treeLockKey(treeID uuid.UUID) int32 {
	h := fnv.New32a()
	_, _ = h.Write(treeID[:])
	return int32(h.Sum32())
}

// PostgresTreeLocker holds each tree lock on a database connection of its own.
type PostgresTreeLocker struct {
	dbURL string
	poll  time.Duration
}

// NewPostgresTreeLocker creates a locker that opens one connection per lock.
func NewPostgresTreeLocker(dbURL string) *PostgresTreeLocker {
	return &PostgresTreeLocker{dbURL: dbURL, poll: treeLockPollInterval}
}

// Lock polls for the tree's session-level advisory lock until it is granted
// or ctx ends.
func (l *PostgresTreeLocker) Lock(ctx context.Context, treeID uuid.UUID) (func() error, error) {
	// The pool parser strips pool-only settings such as pool_max_conns. A plain
	// connection would send them to Postgres, which rejects them.
	cfg, err := pgxpool.ParseConfig(l.dbURL)
	if err != nil {
		return nil, fmt.Errorf("parse the database URL for the lock on tree %s: %w", treeID, err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = treeLockApplicationName
	conn, err := pgx.ConnectConfig(ctx, cfg.ConnConfig)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("open the lock connection for tree %s: %w", treeID, err)
	}
	key := treeLockKey(treeID)
	for {
		var granted bool
		err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, $2)", treeLockNamespace, key).Scan(&granted)
		if err != nil {
			closeLockConn(conn)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, fmt.Errorf("the pg_try_advisory_lock query for tree %s failed: %w", treeID, err)
		}
		if granted {
			var once sync.Once
			return func() error {
				err := fmt.Errorf("unlock of tree %s was called after the lock was released", treeID)
				once.Do(func() { err = releaseTreeLock(conn, treeID, key) })
				return err
			}, nil
		}
		select {
		case <-ctx.Done():
			closeLockConn(conn)
			return nil, ctx.Err()
		case <-time.After(l.poll):
		}
	}
}

// closeLockConn closes a lock connection that holds no lock.
func closeLockConn(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), treeUnlockTimeout)
	defer cancel()
	_ = conn.Close(ctx)
}

// releaseTreeLock unlocks the tree and closes the connection that held it.
func releaseTreeLock(conn *pgx.Conn, treeID uuid.UUID, key int32) error {
	ctx, cancel := context.WithTimeout(context.Background(), treeUnlockTimeout)
	defer cancel()

	var released bool
	unlockErr := conn.QueryRow(ctx, "SELECT pg_advisory_unlock($1, $2)", treeLockNamespace, key).Scan(&released)
	// Closing ends the session, which drops the lock even when the unlock call
	// failed.
	closeErr := conn.Close(ctx)

	var errs []error
	switch {
	case unlockErr != nil:
		errs = append(errs, fmt.Errorf("the pg_advisory_unlock query for tree %s failed: %w", treeID, unlockErr))
	case !released:
		errs = append(errs, fmt.Errorf("pg_advisory_unlock for tree %s returned false", treeID))
	}
	if closeErr != nil {
		errs = append(errs, fmt.Errorf("closing the lock connection for tree %s returned an error: %w", treeID, closeErr))
	}
	return errors.Join(errs...)
}
