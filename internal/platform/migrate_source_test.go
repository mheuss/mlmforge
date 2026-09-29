package platform

import (
	"testing"

	"github.com/golang-migrate/migrate/v4/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSourceInfo_PlacesVersionsInTheMigrationsDirectory(t *testing.T) {
	dir := FindMigrationsDir(t)
	src, err := source.Open("file://" + dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })
	mg := &migration{source: src, path: dir}

	assert.Equal(t, SourceInfo{Path: dir, InSource: true, Previous: 5, HasPrevious: true}, mg.sourceInfo(6))
	assert.Equal(t, SourceInfo{Path: dir, InSource: true}, mg.sourceInfo(1))
	assert.Equal(t, SourceInfo{Path: dir}, mg.sourceInfo(99))
	assert.Equal(t, SourceInfo{Path: dir}, mg.sourceInfo(-1))
}
