package platform

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateStreamName(t *testing.T) {
	tests := []struct {
		name    string
		stream  string
		wantErr bool
	}{
		{name: "valid simple", stream: "order-abc", wantErr: false},
		{name: "valid multi-hyphen", stream: "commission-period-2026-01", wantErr: false},
		{name: "valid uuid id", stream: "order-550e8400-e29b-41d4-a716-446655440000", wantErr: false},
		{name: "empty string", stream: "", wantErr: true},
		{name: "no hyphen", stream: "singleton", wantErr: true},
		{name: "hyphen only", stream: "-", wantErr: true},
		{name: "empty category", stream: "-abc", wantErr: true},
		{name: "empty id", stream: "order-", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateStreamName(tt.stream)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrInvalidStreamName)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
