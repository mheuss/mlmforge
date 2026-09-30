package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExitCode_ReadsTheCodeThroughAWrap(t *testing.T) {
	err := &exitCodeError{code: 3, err: errors.New("appended")}

	assert.Equal(t, 3, exitCode(errors.Join(errors.New("context"), err)))
}

func TestExitCode_IsZeroWithoutAnError(t *testing.T) {
	assert.Equal(t, 0, exitCode(nil))
}

func TestExitCode_IsOneForAnUncodedError(t *testing.T) {
	assert.Equal(t, 1, exitCode(errors.New("anything else")))
}
