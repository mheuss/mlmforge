package platform

import (
	"errors"
	"testing"

	"github.com/golang-migrate/migrate/v4/database"
	"github.com/stretchr/testify/assert"
)

// serverError stands in for a Postgres error that carries an SQLSTATE.
type serverError struct{}

func (serverError) Error() string    { return "pq: duplicate key" }
func (serverError) SQLState() string { return "23505" }

func TestIsBodyFailure_OnlyAPostgresErrorFromTheFileCounts(t *testing.T) {
	body := database.Error{Err: "migration failed: duplicate key", OrigErr: serverError{}, Query: []byte("CREATE INDEX x")}

	assert.True(t, isBodyFailure(body))
	assert.False(t, isBodyFailure(database.Error{Err: "migration failed", OrigErr: errors.New("connection reset")}),
		"a body error with no SQLSTATE may have committed")
	assert.False(t, isBodyFailure(&database.Error{OrigErr: serverError{}, Query: []byte("TRUNCATE schema_migrations")}),
		"a failed record write is not a body failure")
	assert.False(t, isBodyFailure(database.Error{Err: "transaction commit failed", OrigErr: serverError{}}))
	assert.False(t, isBodyFailure(errors.New("an error shape never seen before")))
	assert.False(t, isBodyFailure(errors.Join(body, errors.New("other"))), "a wrapped body error is not recognised")
}

func TestApplyError_LeavesTheMigrationFileOutOfItsText(t *testing.T) {
	body := database.Error{Err: "migration failed: duplicate key, Key (tree_id)=(t) is duplicated.", OrigErr: serverError{},
		Query: []byte("-- this file forces nothing\nCREATE INDEX x")}
	bookkeeping := &database.Error{OrigErr: serverError{}, Query: []byte("TRUNCATE schema_migrations")}

	assert.Equal(t, "apply migrations: migration failed: duplicate key, Key (tree_id)=(t) is duplicated. (details: pq: duplicate key)",
		(&ApplyError{Err: body}).Error())
	assert.Equal(t, "apply migrations: pq: duplicate key", (&ApplyError{Err: bookkeeping}).Error())
	assert.Equal(t, "rollback migration: pq: duplicate key", (&RollbackError{Err: bookkeeping}).Error())
}
