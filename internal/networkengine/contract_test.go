package networkengine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contractFixtureSetup represents a setup step that runs before the main request.
type contractFixtureSetup struct {
	ID     string         `json:"id"`
	Op     string         `json:"op"`
	Params map[string]any `json:"params"`
}

// contractFixture represents a single contract test case loaded from JSON.
// Both Rust and Go read the same fixture files to catch serialization drift.
type contractFixture struct {
	Description      string                 `json:"description"`
	Setup            []contractFixtureSetup `json:"setup,omitempty"`
	Request          *json.RawMessage       `json:"request,omitempty"`
	RequestRaw       *string                `json:"request_raw,omitempty"`
	ExpectedResponse json.RawMessage        `json:"expected_response"`
}

func loadContractFixtures(t *testing.T) []struct {
	name    string
	fixture contractFixture
} {
	t.Helper()

	dir := filepath.Join("..", "..", "engine", "testdata", "contracts")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err, "failed to read fixtures dir: %s", dir)

	var fixtures []struct {
		name    string
		fixture contractFixture
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		require.NoError(t, err, "failed to read %s", path)

		var f contractFixture
		require.NoError(t, json.Unmarshal(data, &f), "failed to parse %s", path)

		name := strings.TrimSuffix(entry.Name(), ".json")
		fixtures = append(fixtures, struct {
			name    string
			fixture contractFixture
		}{name: name, fixture: f})
	}

	sort.Slice(fixtures, func(i, j int) bool {
		return fixtures[i].name < fixtures[j].name
	})

	return fixtures
}

func TestContractFixtures(t *testing.T) {
	binaryPath := findWorkerBinary(t)

	fixtures := loadContractFixtures(t)
	require.NotEmpty(t, fixtures, "no contract fixtures found")

	for _, tc := range fixtures {
		t.Run(tc.name, func(t *testing.T) {
			// Spawn a fresh worker for each fixture to avoid state leaking.
			transport, err := NewStdioTransport(binaryPath)
			require.NoError(t, err)
			defer transport.Close()

			// Run setup steps (e.g., create_tree) before the main request.
			for _, step := range tc.fixture.Setup {
				setupReq := map[string]any{
					"id":     step.ID,
					"op":     step.Op,
					"params": step.Params,
				}
				setupJSON, err := json.Marshal(setupReq)
				require.NoError(t, err, "failed to marshal setup step %s", step.ID)
				resp := sendRawLine(t, transport, string(setupJSON))
				t.Logf("setup %s: %s", step.ID, resp)
			}

			// Build the request line. For structured requests, re-marshal to
			// produce compact single-line JSON. The NDJSON protocol requires
			// one JSON object per line.
			var requestLine string
			if tc.fixture.RequestRaw != nil {
				requestLine = *tc.fixture.RequestRaw
			} else if tc.fixture.Request != nil {
				compacted, err := json.Marshal(*tc.fixture.Request)
				require.NoError(t, err, "failed to compact request JSON")
				requestLine = string(compacted)
			} else {
				t.Fatalf("fixture %s has neither 'request' nor 'request_raw'", tc.name)
			}

			// Send request and read raw response (bypassing the transport's
			// error handling so we can inspect error responses too).
			actual := sendRawLine(t, transport, requestLine)

			// Parse expected and actual for field comparison.
			var expected map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(tc.fixture.ExpectedResponse, &expected))

			var actualMap map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(actual), &actualMap),
				"failed to parse actual response: %s", actual)

			// Compare ok field.
			assertJSONFieldEqual(t, tc.name, "ok", expected, actualMap)

			// Compare id field.
			assertJSONFieldEqual(t, tc.name, "id", expected, actualMap)

			// Compare result if expected has one.
			if _, ok := expected["result"]; ok {
				assertJSONFieldEqual(t, tc.name, "result", expected, actualMap)
			}

			// Compare error code if expected has one.
			if errRaw, ok := expected["error"]; ok {
				var expectedErr map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(errRaw, &expectedErr))

				actualErrRaw, ok := actualMap["error"]
				require.True(t, ok, "[%s] expected error but got none", tc.name)

				var actualErr map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(actualErrRaw, &actualErr))

				if code, ok := expectedErr["code"]; ok {
					assert.JSONEq(t, string(code), string(actualErr["code"]),
						"[%s] error code mismatch", tc.name)
				}
			}

			t.Logf("contract: %s -- %s", tc.name, tc.fixture.Description)
		})
	}
}

// sendRawLine writes a raw line to the worker's stdin and reads the response.
// This bypasses StdioTransport.Call so we can test malformed requests and
// inspect error responses without the transport converting them to Go errors.
func sendRawLine(t *testing.T, transport *StdioTransport, line string) string {
	t.Helper()

	transport.mu.Lock()
	defer transport.mu.Unlock()

	_, err := fmt.Fprintf(transport.stdin, "%s\n", line)
	require.NoError(t, err, "failed to write to worker stdin")

	resp, err := transport.reader.ReadBytes('\n')
	require.NoError(t, err, "failed to read from worker stdout")

	return strings.TrimSpace(string(resp))
}

// assertJSONFieldEqual compares a single field between expected and actual
// response maps using JSON equality.
func assertJSONFieldEqual(t *testing.T, fixture, field string, expected, actual map[string]json.RawMessage) {
	t.Helper()

	exp, ok := expected[field]
	if !ok {
		return
	}
	act, ok := actual[field]
	require.True(t, ok, "[%s] expected field '%s' in response", fixture, field)
	assert.JSONEq(t, string(exp), string(act), "[%s] field '%s' mismatch", fixture, field)
}
