package platform

import (
	"errors"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/assert"
)

func TestRecord_StringNamesTheStoredState(t *testing.T) {
	assert.Equal(t, "none", Record{Version: -1}.String())
	assert.Equal(t, "-1, dirty", Record{Version: -1, Dirty: true}.String())
	assert.Equal(t, "6, dirty", Record{Version: 6, Dirty: true}.String())
	assert.Equal(t, "5, clean", Record{Version: 5}.String())
}

func TestMigrateErrors_ErrorStrings(t *testing.T) {
	cases := map[string]error{
		"migration record is not dirty: 5, clean":                       &NotDirtyError{Record: Record{Version: 5}},
		"migration record is -1, dirty":                                 &NegativeVersionError{Record: Record{Version: -1, Dirty: true}},
		"migration record is 9, dirty; /m has no migration 9":           &VersionNotInSourceError{Record: Record{Version: 9, Dirty: true}, Path: "/m"},
		"migrate up refused: the database URL sets x-multi-statement=1": &MultiStatementError{Command: "up", Value: "1"},
		"migration record is 6, dirty":                                  &DirtyError{Record: Record{Version: 6, Dirty: true}},
		"apply migrations: boom":                                        &ApplyError{Err: errors.New("boom")},
		"rollback migration: boom":                                      &RollbackError{Err: errors.New("boom")},
		"closing the migration drivers failed: boom":                    &ReleaseError{What: "closing the migration drivers failed", Err: errors.New("boom")},
	}
	for want, err := range cases {
		assert.Equal(t, want, err.Error())
	}
}

func TestSplitRelease_SeparatesReleaseFailuresAtAnyJoinDepth(t *testing.T) {
	op := errors.New("op")
	unlock := &ReleaseError{What: "releasing the migration lock failed", Err: errors.New("u")}
	closing := &ReleaseError{What: "closing the migration drivers failed", Err: errors.New("c")}

	releases, rest := SplitRelease(errors.Join(errors.Join(op, unlock), closing))

	assert.Same(t, op, rest)
	assert.Equal(t, []error{unlock, closing}, releases)
}

func TestSplitRelease_ASucceededOperationLeavesNoRest(t *testing.T) {
	unlock := &ReleaseError{What: "releasing the migration lock failed", Err: errors.New("u")}

	releases, rest := SplitRelease(errors.Join(nil, unlock))

	assert.NoError(t, rest)
	assert.Equal(t, []error{unlock}, releases)
}

func TestSplitRelease_KeepsTheLibrarySentinelsBesideAReleaseFailure(t *testing.T) {
	unlock := &ReleaseError{What: "releasing the migration lock failed", Err: errors.New("u")}

	_, noChange := SplitRelease(errors.Join(migrate.ErrNoChange, unlock))
	_, dirty := SplitRelease(errors.Join(migrate.ErrDirty{Version: 6}, unlock))

	assert.ErrorIs(t, noChange, migrate.ErrNoChange)
	assert.Equal(t, migrate.ErrDirty{Version: 6}, dirty)
}

func TestSplitRelease_Nil(t *testing.T) {
	releases, rest := SplitRelease(nil)

	assert.NoError(t, rest)
	assert.Empty(t, releases)
}

func TestMigrateErrors_WrappersUnwrapToTheirCause(t *testing.T) {
	cause := errors.New("boom")
	assert.ErrorIs(t, &ApplyError{Err: cause}, cause)
	assert.ErrorIs(t, &RollbackError{Err: cause}, cause)
	assert.ErrorIs(t, &ReleaseError{What: "x", Err: cause}, cause)
}
