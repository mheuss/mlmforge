package platform

import (
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/golang-migrate/migrate/v4/database"
	"github.com/stretchr/testify/assert"
)

// serverError stands in for a Postgres error that carries an SQLSTATE.
type serverError struct {
	state string
}

func (serverError) Error() string      { return "pq: duplicate key" }
func (e serverError) SQLState() string { return e.state }

func TestIsBodyFailure_OnlyAPostgresErrorFromTheFileCounts(t *testing.T) {
	unique := serverError{state: "23505"}
	body := database.Error{Err: "migration failed: duplicate key", OrigErr: unique, Query: []byte("CREATE INDEX x")}

	assert.True(t, isBodyFailure(body))
	assert.False(t, isBodyFailure(database.Error{Err: "migration failed", OrigErr: driver.ErrBadConn}),
		"a dropped connection may have committed")
	for _, state := range []string{"55P03", "40P01", "42601"} {
		assert.True(t, isBodyFailure(database.Error{Err: "migration failed: x", OrigErr: serverError{state: state}}), state)
	}
	for _, state := range []string{"08006", "53100", "57014", "57P01", "58030", "XX000", "", "235"} {
		assert.False(t, isBodyFailure(database.Error{Err: "migration failed: x", OrigErr: serverError{state: state}}), state)
	}
	assert.False(t, isBodyFailure(database.Error{Err: "migration failed", OrigErr: errors.New("connection reset")}),
		"a body error with no SQLSTATE may have committed")
	assert.False(t, isBodyFailure(&database.Error{OrigErr: serverError{state: "23505"}, Query: []byte("TRUNCATE schema_migrations")}),
		"a failed record write is not a body failure")
	assert.False(t, isBodyFailure(database.Error{Err: "transaction commit failed", OrigErr: serverError{state: "23505"}}))
	assert.False(t, isBodyFailure(errors.New("an error shape never seen before")))
	assert.False(t, isBodyFailure(errors.Join(body, errors.New("other"))), "a wrapped body error is not recognised")
}

func TestApplyError_LeavesTheMigrationFileOutOfItsText(t *testing.T) {
	body := database.Error{Err: "migration failed: duplicate key, Key (tree_id)=(t) is duplicated.", OrigErr: serverError{state: "23505"},
		Query: []byte("-- this file forces nothing\nCREATE INDEX x")}
	bookkeeping := &database.Error{OrigErr: serverError{state: "23505"}, Query: []byte("TRUNCATE schema_migrations")}

	assert.Equal(t, "apply migrations: migration failed: duplicate key, Key (tree_id)=(t) is duplicated. (details: pq: duplicate key)",
		(&ApplyError{Err: body}).Error())
	assert.Equal(t, "apply migrations: pq: duplicate key", (&ApplyError{Err: bookkeeping}).Error())
	assert.Equal(t, "rollback migration: pq: duplicate key", (&RollbackError{Err: bookkeeping}).Error())
}

func TestDatabaseErrorText_KeepsTheLineNumberWithoutTheFile(t *testing.T) {
	got := databaseErrorText(database.Error{Err: "migration failed: syntax error (column 3)", OrigErr: serverError{state: "23505"},
		Line: 4, Query: []byte("-- a comment\nSELEC 1")})

	assert.Equal(t, "migration failed: syntax error (column 3) in line 4 (details: pq: duplicate key)", got)
}

func TestDatabaseErrorText_KeepsTheLineNumberWithoutAMessage(t *testing.T) {
	got := databaseErrorText(database.Error{OrigErr: serverError{state: "23505"}, Line: 4, Query: []byte("SELECT 1")})

	assert.Equal(t, "pq: duplicate key in line 4", got)
}
