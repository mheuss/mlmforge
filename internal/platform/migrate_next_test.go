package platform

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextTarget_WritesTheNextThatSourceInfoReports(t *testing.T) {
	dir := FindMigrationsDir(t)
	mg := sourceMigration(t, dir)

	versions := []int{database.NilVersion}
	for v, verr := mg.source.First(); verr == nil; v, verr = mg.source.Next(v) {
		versions = append(versions, int(v))
	}
	require.Greater(t, len(versions), 2, "versions read from %s", dir)

	checked := 0
	for _, version := range versions {
		info := mg.sourceInfo(version)
		require.NoError(t, info.Err, "version %d", version)
		if !info.HasNext {
			continue
		}
		checked++
		to, err := mg.nextTarget(Record{Version: version, Dirty: true})
		require.NoError(t, err, "version %d", version)
		assert.Equal(t, int(info.Next), to.Version, "version %d", version)
	}
	assert.Equal(t, len(versions)-1, checked, "versions with a next migration")
}

// closeFailingSource is a source.Driver over migrations 1, 2 and 3 whose down readers fail to close.
type closeFailingSource struct {
	source.Driver
	upErr    error
	firstErr error
}

func (f closeFailingSource) First() (uint, error) {
	if f.firstErr != nil {
		return 0, f.firstErr
	}
	return 1, nil
}

func (closeFailingSource) Prev(v uint) (uint, error) {
	if v <= 1 {
		return 0, os.ErrNotExist
	}
	return v - 1, nil
}

func (closeFailingSource) Next(v uint) (uint, error) {
	if v >= 3 {
		return 0, os.ErrNotExist
	}
	return v + 1, nil
}

func (f closeFailingSource) ReadUp(v uint) (io.ReadCloser, string, error) {
	if v < 1 || v > 3 {
		return nil, "", os.ErrNotExist
	}
	if f.upErr != nil {
		return nil, "", f.upErr
	}
	return io.NopCloser(strings.NewReader("")), "", nil
}

func (closeFailingSource) ReadDown(v uint) (io.ReadCloser, string, error) {
	if v < 1 || v > 3 {
		return nil, "", os.ErrNotExist
	}
	return failingCloser{strings.NewReader("")}, "", nil
}

// failingCloser is a reader whose Close always fails.
type failingCloser struct{ io.Reader }

func (failingCloser) Close() error { return errors.New("close failed") }

func TestNextTarget_ADownFileThatFailsToCloseStillCounts(t *testing.T) {
	mg := &migration{source: closeFailingSource{}, path: "/migrations"}

	to, err := mg.nextTarget(Record{Version: 2, Dirty: true})

	require.NoError(t, err)
	assert.Equal(t, Record{Version: 3}, to)
}

func TestNextTarget_AnUnreadableUpFileNamesTheMigration(t *testing.T) {
	mg := &migration{source: closeFailingSource{upErr: os.ErrPermission}, path: "/migrations"}

	_, err := mg.nextTarget(Record{Version: 2, Dirty: true})

	var notWritten *NotWrittenError
	require.ErrorAs(t, err, &notWritten)
	assert.Equal(t, "read migration 2 from /migrations: permission denied", err.Error())
}

func TestNextTarget_ARefusalFromSourceInfoRefusesTheReset(t *testing.T) {
	mg := &migration{source: closeFailingSource{firstErr: os.ErrPermission}, path: "/migrations"}

	to, err := mg.nextTarget(Record{Version: 2, Dirty: true})

	var notWritten *NotWrittenError
	require.ErrorAs(t, err, &notWritten, "returned %v", to)
	assert.Equal(t, "read migration 2 from /migrations: permission denied", err.Error())
}

func TestNextTarget_AnUnreadableDirectoryFromNoVersionNamesTheDirectory(t *testing.T) {
	mg := &migration{source: closeFailingSource{firstErr: os.ErrPermission}, path: "/migrations"}

	_, err := mg.nextTarget(Record{Version: database.NilVersion, Dirty: true})

	var notWritten *NotWrittenError
	require.ErrorAs(t, err, &notWritten)
	assert.Equal(t, "read the migrations directory /migrations: permission denied", err.Error())
}
