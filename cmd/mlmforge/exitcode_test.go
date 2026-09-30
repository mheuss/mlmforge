package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExitCode_ReadsTheCodeThroughAWrap(t *testing.T) {
	err := &exitCodeError{code: 3, err: errors.New("appended")}

	assert.Equal(t, 3, exitCode(errors.Join(errors.New("context"), err)))
	assert.Equal(t, 0, exitCode(nil))
	assert.Equal(t, 1, exitCode(errors.New("anything else")))
}
