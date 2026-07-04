package networkengine

import "encoding/json"

// isSignal reports whether an NDJSON line from the worker is a signal message
// (a structured log/metric/trace event) rather than a protocol response.
// Signals carry "type":"signal"; responses never carry a "type" field.
// See content/design-rationale/019-ndjson-protocol.md.
func isSignal(line json.RawMessage) bool {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return false
	}
	return probe.Type == "signal"
}
