package networkengine

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsSignal(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		{
			name: "signal warn line",
			line: `{"type":"signal","level":"warn","target":"network_engine::commission","message":"pairing percent outside [0.0, 1.0]","fields":{"percent":1.5}}`,
			want: true,
		},
		{
			name: "signal with only type",
			line: `{"type":"signal"}`,
			want: true,
		},
		{
			name: "protocol response",
			line: `{"id":"req-1","ok":true,"result":null}`,
			want: false,
		},
		{
			name: "response carrying a result payload",
			line: `{"id":"req-2","ok":true,"result":{"user_id":"u1","depth":3}}`,
			want: false,
		},
		{
			name: "empty object",
			line: `{}`,
			want: false,
		},
		{
			name: "type present but not signal",
			line: `{"type":"response"}`,
			want: false,
		},
		{
			name: "malformed json",
			line: `{"type":`,
			want: false,
		},
		{
			name: "empty line",
			line: ``,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isSignal(json.RawMessage(tt.line)))
		})
	}
}
