package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSessionTimeoutDefaults_MatchTheMeasuredRule(t *testing.T) {
	assert.Equal(t, "30s", defaultStatementTimeout)
	assert.Equal(t, "11s", defaultLockTimeout)
}
