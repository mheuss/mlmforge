package platform

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const settingsBase = "postgres://u:p@h/db?sslmode=disable"

func TestRefuseUnusedSettings_RefusesEachUnusedSetting(t *testing.T) {
	for _, name := range []string{"x-migrations-table", "x-migrations-table-quoted", "x-statement-timeout", "x-multi-statement-max-size"} {
		err := refuseUnusedSettings("up", settingsBase+"&"+name+"=1")

		var unused *UnusedSettingError
		require.ErrorAs(t, err, &unused, name)
		assert.Equal(t, &UnusedSettingError{Command: "up", Name: name}, unused)
	}
}

func TestRefuseUnusedSettings_RefusesAMultiStatementValueThatIsNeitherTrueNorFalse(t *testing.T) {
	for _, value := range []string{"yes", "on", "2"} {
		err := refuseUnusedSettings("version", settingsBase+"&x-multi-statement="+value)

		var invalid *InvalidSettingError
		require.ErrorAs(t, err, &invalid, value)
		assert.Equal(t, &InvalidSettingError{Command: "version", Name: "x-multi-statement", Value: value}, invalid)
	}
}

func TestRefuseUnusedSettings_PassesWhatItDoesNotRefuse(t *testing.T) {
	for _, dbURL := range []string{
		settingsBase,
		settingsBase + "&x-multi-statement=true",
		settingsBase + "&x-multi-statement=0",
		settingsBase + "&x-multi-statement=",
		settingsBase + "&x-other=1",
		"postgres://u:p@h:notaport/db?x-statement-timeout=1",
	} {
		assert.NoError(t, refuseUnusedSettings("up", dbURL), dbURL)
	}
}

func TestMigrateCommands_RefuseUnusedSettingsBeforeOpeningAnything(t *testing.T) {
	calls := map[string]func(dbURL, dir string) error{
		"up":   func(dbURL, dir string) error { return MigrateUp(context.Background(), dbURL, dir, nil) },
		"down": func(dbURL, dir string) error { return MigrateDown(context.Background(), dbURL, dir, nil) },
		"version": func(dbURL, dir string) error {
			_, err := MigrateVersion(context.Background(), dbURL, dir, nil)
			return err
		},
		"reset-dirty": func(dbURL, dir string) error {
			_, err := ResetDirty(context.Background(), dbURL, dir, nil)
			return err
		},
		"reset-dirty --after-failed-down": func(dbURL, dir string) error {
			_, err := ResetAfterFailedDown(context.Background(), dbURL, dir, nil)
			return err
		},
	}
	for command, call := range calls {
		t.Run(command, func(t *testing.T) {
			for _, name := range unusedSettings {
				var unused *UnusedSettingError
				require.ErrorAs(t, call(refusedURL+"&"+name+"=5", FindMigrationsDir(t)), &unused, name)
				assert.Equal(t, &UnusedSettingError{Command: command, Name: name}, unused)
			}

			var invalid *InvalidSettingError
			require.ErrorAs(t, call(refusedURL+"&x-multi-statement=yes", FindMigrationsDir(t)), &invalid)
			assert.Equal(t, command, invalid.Command)
		})
	}
}

func TestRefusals_AcceptAnEmptyMultiStatementOnEveryCommand(t *testing.T) {
	dbURL := settingsBase + "&x-multi-statement="
	for _, command := range []string{"up", "down", "version", "reset-dirty", "reset-dirty --after-failed-down"} {
		assert.NoError(t, refuseUnusedSettings(command, dbURL), command)
		assert.NoError(t, refuseMultiStatement(command, dbURL), command)
	}
}

func TestMigrateVersion_StillAcceptsMultiStatementOn(t *testing.T) {
	dsn := newResetDatabase(t)

	_, err := MigrateVersion(context.Background(), dsn+"&x-multi-statement=true", FindMigrationsDir(t), nil)

	require.NoError(t, err)
}
