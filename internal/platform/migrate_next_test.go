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
	src, err := source.Open("file://" + dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })
	mg := &migration{source: src, path: dir}

	versions := []int{database.NilVersion}
	for v, verr := src.First(); verr == nil; v, verr = src.Next(v) {
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
}

func (closeFailingSource) First() (uint, error) { return 1, nil }

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

func (closeFailingSource) ReadUp(uint) (io.ReadCloser, string, error) {
	return io.NopCloser(strings.NewReader("")), "", nil
}

func (closeFailingSource) ReadDown(uint) (io.ReadCloser, string, error) {
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
