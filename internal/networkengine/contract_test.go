package networkengine

import (
	"context"
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

// contractFixtureDir is where both language harnesses read their shared
// fixtures from.
var contractFixtureDir = filepath.Join("..", "..", "engine", "testdata", "contracts")

// contractFixtureSetup represents a setup step that runs before the main request.
type contractFixtureSetup struct {
	ID     string         `json:"id"`
	Op     string         `json:"op"`
	Params map[string]any `json:"params"`
}

// contractFixture represents a single contract test case loaded from JSON.
// Both Rust and Go read the same fixture files to catch serialization drift.
type contractFixture struct {
	Description string                 `json:"description"`
	Setup       []contractFixtureSetup `json:"setup,omitempty"`
	// SetupRaw holds NDJSON lines to send as setup verbatim, bypassing the
	// map[string]any round-trip. Use it when the fixture needs to control its
	// own bytes: duplicate keys, or a specific key order the assertion depends
	// on. Mirrors RequestRaw. Mutually exclusive with Setup.
	//
	// Not for malformed input. Every setup response is asserted to contain
	// "ok":true, so a payload the worker rejects fails the harness rather than
	// exercising anything. Malformed-input coverage belongs on RequestRaw,
	// where the rejection is the assertion.
	//
	// It is no longer needed just because a fixture loads a plan. Re-marshaling
	// reorders keys alphabetically, which used to break adjacent-tagged enum
	// payloads; HEU-648 made that order parse fine.
	SetupRaw         []string         `json:"setup_raw,omitempty"`
	Request          *json.RawMessage `json:"request,omitempty"`
	RequestRaw       *string          `json:"request_raw,omitempty"`
	ExpectedResponse json.RawMessage  `json:"expected_response"`
}

func loadContractFixtures(t *testing.T) []struct {
	name    string
	fixture contractFixture
} {
	t.Helper()

	dir := contractFixtureDir
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
			defer func() { _ = transport.Close() }()

			// Fixtures pick exactly one setup mode. Mixing them is
			// ambiguous because ordering across the two lists is undefined.
			require.False(t,
				len(tc.fixture.Setup) > 0 && len(tc.fixture.SetupRaw) > 0,
				"[%s] fixture has both 'setup' and 'setup_raw'; pick one", tc.name)

			// Run setup steps (e.g., create_tree) before the main request.
			// SetupRaw is sent verbatim, mirroring RequestRaw. See its doc
			// comment on contractFixture for when a fixture needs it.
			var setupLines []string
			if len(tc.fixture.SetupRaw) > 0 {
				setupLines = tc.fixture.SetupRaw
			} else {
				setupLines = make([]string, 0, len(tc.fixture.Setup))
				for _, step := range tc.fixture.Setup {
					setupReq := map[string]any{
						"id":     step.ID,
						"op":     step.Op,
						"params": step.Params,
					}
					setupJSON, err := json.Marshal(setupReq)
					require.NoError(t, err, "failed to marshal setup step %s", step.ID)
					setupLines = append(setupLines, string(setupJSON))
				}
			}

			for i, line := range setupLines {
				resp := sendRawLine(t, transport, line)
				require.Contains(t, resp, `"ok":true`,
					"setup request %d failed: %s", i, resp)
				t.Logf("setup %d: %s", i, resp)
			}

			// Build the request line. For structured requests, re-marshal to
			// produce compact single-line JSON. The NDJSON protocol requires
			// one JSON object per line.
			var requestLine string
			switch {
			case tc.fixture.RequestRaw != nil:
				requestLine = *tc.fixture.RequestRaw
			case tc.fixture.Request != nil:
				compacted, err := json.Marshal(*tc.fixture.Request)
				require.NoError(t, err, "failed to compact request JSON")
				requestLine = string(compacted)
			default:
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

			// Compare the error object if expected has one.
			if errRaw, ok := expected["error"]; ok {
				var expectedErr map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(errRaw, &expectedErr))
				// A JSON null decodes to a nil map without erroring, so the
				// type check has to be explicit or the fixture asserts nothing.
				require.NotNil(t, expectedErr,
					"[%s] expected_response.error is not an object: %s", tc.name, errRaw)

				actualErrRaw, ok := actualMap["error"]
				require.True(t, ok, "[%s] expected error but got none", tc.name)

				var actualErr map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(actualErrRaw, &actualErr))

				assert.NoError(t, checkExpectedError(tc.name, expectedErr, actualErr))
			}

			t.Logf("contract: %s -- %s", tc.name, tc.fixture.Description)
		})
	}
}

// expectedErrorKeys is every key a fixture may put under expected_response.error.
// A key outside this set fails the fixture rather than being ignored, so a
// misspelled assertion cannot pass by asserting nothing.
var expectedErrorKeys = map[string]bool{
	"code":             true,
	"message_contains": true,
}

// checkExpectedError compares a fixture's expected error object against the
// worker's actual one.
//
// code is compared exactly when present. message_contains is compared as a
// substring of the actual message when present, and the message is not read at
// all when it is absent.
func checkExpectedError(fixtureName string, expectedErr, actualErr map[string]json.RawMessage) error {
	if len(expectedErr) == 0 {
		return fmt.Errorf("[%s] expected_response.error is empty, so it asserts nothing", fixtureName)
	}

	for key := range expectedErr {
		if !expectedErrorKeys[key] {
			return fmt.Errorf("[%s] unrecognized key %q under expected_response.error", fixtureName, key)
		}
	}

	if codeRaw, ok := expectedErr["code"]; ok {
		// Decode through any for the same reason message_contains does: a JSON
		// null unmarshals into a string as a no-op with no error, which would
		// compare an empty code and report a mismatch that does not exist.
		var wantAny any
		if err := json.Unmarshal(codeRaw, &wantAny); err != nil {
			return fmt.Errorf("[%s] expected error code is not valid JSON: %s", fixtureName, codeRaw)
		}
		want, isString := wantAny.(string)
		if !isString {
			return fmt.Errorf("[%s] expected error code is not a JSON string: %s", fixtureName, codeRaw)
		}

		actualCodeRaw, ok := actualErr["code"]
		if !ok {
			return fmt.Errorf("[%s] expected error code %q but the response carried no code", fixtureName, want)
		}

		var got string
		if err := json.Unmarshal(actualCodeRaw, &got); err != nil {
			return fmt.Errorf("[%s] actual error code is not a JSON string: %s", fixtureName, actualCodeRaw)
		}

		if got != want {
			return fmt.Errorf("[%s] error code mismatch: want %q, got %q", fixtureName, want, got)
		}
	}

	substrRaw, hasSubstr := expectedErr["message_contains"]
	if !hasSubstr {
		return nil
	}

	// Unmarshaling a JSON null into a string is a documented no-op that leaves
	// want empty and returns no error, and strings.Contains against "" is
	// always true. Both have to be rejected explicitly or the key asserts
	// nothing while looking like it asserts something.
	var wantAny any
	if err := json.Unmarshal(substrRaw, &wantAny); err != nil {
		return fmt.Errorf("[%s] message_contains is not valid JSON: %s", fixtureName, substrRaw)
	}
	want, ok := wantAny.(string)
	if !ok {
		return fmt.Errorf("[%s] message_contains is not a JSON string: %s", fixtureName, substrRaw)
	}
	if want == "" {
		return fmt.Errorf("[%s] message_contains is empty, which every message contains", fixtureName)
	}

	actualMsgRaw, hasMessage := actualErr["message"]
	if !hasMessage {
		return fmt.Errorf("[%s] message_contains wants %q but the response carried no error message", fixtureName, want)
	}

	var got string
	if err := json.Unmarshal(actualMsgRaw, &got); err != nil {
		return fmt.Errorf("[%s] actual error message is not a JSON string: %s", fixtureName, actualMsgRaw)
	}

	if !strings.Contains(got, want) {
		return fmt.Errorf("[%s] error message does not contain %q; message was %q", fixtureName, want, got)
	}

	return nil
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

	// Drain the async reader like Call does: the worker emits signal frames
	// (structured logs) on stdout, so skip them and return the next response.
	// A signal emitted mid-fixture must not be read as the response (isSignal,
	// Task 8). Current fixtures use valid configs (no warns → no signals); this
	// keeps the harness correct when a future fixture triggers one.
	for raw := range transport.lines {
		if isSignal(raw) {
			continue
		}
		return strings.TrimSpace(string(raw))
	}

	require.Fail(t, "worker stdout closed before a response was read")
	return ""
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

// --- Board plan contract tests ---
//
// These use the typed EngineClient to verify the Go-Rust wire protocol
// for board plan operations end-to-end.

// boardPlanConfig returns a minimal BoardPlanConfig JSON for a width=2,
// height=1 board (3 positions). Re-entry is enabled so cycling produces
// re-entry events.
func boardPlanConfig() json.RawMessage {
	return json.RawMessage(`{
		"cycle_commission": 500.0,
		"re_entry_enabled": true,
		"re_entry_position": "bottom",
		"max_cycles_per_period": 3,
		"max_cascade_depth": 10,
		"stall_threshold_periods": 3,
		"inactive_compression": false
	}`)
}

func TestContractBoardPlan_CreateAndAddMembers(t *testing.T) {
	client, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	ctx := context.Background()
	const structure = "board_test"

	// Width 2, height 1 = 3 positions per board (1 root + 2 children).
	err = client.CreateBoardPlan(ctx, structure, 2, 1, boardPlanConfig())
	require.NoError(t, err)

	// After creation, one empty board should exist.
	boards, err := client.BoardListBoards(ctx, structure)
	require.NoError(t, err)
	require.Len(t, boards, 1, "should have one initial board")
	assert.Equal(t, 0, boards[0].FilledCount)
	assert.Equal(t, 3, boards[0].TotalPositions)

	// Add first member.
	member1 := "00000000-0000-0000-0000-000000000001"
	sponsor := "00000000-0000-0000-0000-000000000099"
	result1, err := client.BoardAddMember(ctx, structure, member1, sponsor, 1000)
	require.NoError(t, err)
	assert.Equal(t, 0, result1.Position, "first member should get position 0")
	assert.Empty(t, result1.CycleEvents, "no cycling on first add")

	// Add second member.
	member2 := "00000000-0000-0000-0000-000000000002"
	result2, err := client.BoardAddMember(ctx, structure, member2, member1, 2000)
	require.NoError(t, err)
	assert.Equal(t, 1, result2.Position, "second member should get position 1")
	assert.Empty(t, result2.CycleEvents, "no cycling on second add")

	// Verify member lookup.
	info, err := client.BoardGetMember(ctx, structure, member1)
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.NotEmpty(t, info.BoardID, "member should be on a board")

	// Non-existent member returns nil.
	unknown := "00000000-0000-0000-0000-000000000088"
	info, err = client.BoardGetMember(ctx, structure, unknown)
	require.NoError(t, err)
	assert.Nil(t, info, "non-existent member should return nil")
}

func TestContractBoardPlan_CycleOnFill(t *testing.T) {
	client, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	ctx := context.Background()
	const structure = "cycle_test"

	// Width 2, height 1 = 3 positions.
	err = client.CreateBoardPlan(ctx, structure, 2, 1, boardPlanConfig())
	require.NoError(t, err)

	member1 := "00000000-0000-0000-0000-000000000001"
	member2 := "00000000-0000-0000-0000-000000000002"
	member3 := "00000000-0000-0000-0000-000000000003"
	sponsor := "00000000-0000-0000-0000-000000000099"

	// Add members 1 and 2. No cycling yet.
	_, err = client.BoardAddMember(ctx, structure, member1, sponsor, 1000)
	require.NoError(t, err)
	_, err = client.BoardAddMember(ctx, structure, member2, member1, 2000)
	require.NoError(t, err)

	// Member 3 fills the board. This should trigger a cycle.
	result, err := client.BoardAddMember(ctx, structure, member3, member1, 3000)
	require.NoError(t, err)

	require.NotEmpty(t, result.CycleEvents, "filling the board should produce cycle events")

	// The cycled member is the one at position 0 (the top). That's member1.
	cycleEvent := result.CycleEvents[0]
	assert.Equal(t, member1, cycleEvent.CycledMember, "top position should cycle out")

	// After cycling, new boards should be created from the split.
	assert.NotEmpty(t, cycleEvent.NewBoards, "cycling should produce new boards")

	// With re-entry enabled, cycled member re-enters a board.
	assert.NotNil(t, cycleEvent.ReEntryBoard, "cycled member should re-enter with re_entry_enabled")

	// Verify board list grew. The original board is gone (consumed by
	// the cycle), and new split boards exist plus the re-entry board.
	boards, err := client.BoardListBoards(ctx, structure)
	require.NoError(t, err)
	assert.Greater(t, len(boards), 1, "cycling should create new boards")
}

func TestContractBoardPlan_SnapshotRoundTrip(t *testing.T) {
	client, err := NewEngineClient(context.Background(), findWorkerBinary(t))
	require.NoError(t, err)
	defer func() { _ = client.Stop() }()

	ctx := context.Background()
	const structure = "snapshot_test"

	// Create a board plan and add a member.
	err = client.CreateBoardPlan(ctx, structure, 2, 1, boardPlanConfig())
	require.NoError(t, err)

	member1 := "00000000-0000-0000-0000-000000000001"
	sponsor := "00000000-0000-0000-0000-000000000099"
	_, err = client.BoardAddMember(ctx, structure, member1, sponsor, 1000)
	require.NoError(t, err)

	// Take snapshot.
	snapshot, err := client.TakeSnapshot(ctx, structure)
	require.NoError(t, err)
	assert.Equal(t, "board_plan", snapshot.TreeType)
	assert.NotEmpty(t, snapshot.Data, "snapshot data should not be empty")

	// Restore to a new structure name using the snapshot data.
	const restored = "snapshot_restored"
	err = client.RestoreSnapshot(ctx, restored, snapshot.TreeType, snapshot.Data)
	require.NoError(t, err)

	// Verify the restored structure has the same member.
	info, err := client.BoardGetMember(ctx, restored, member1)
	require.NoError(t, err)
	require.NotNil(t, info, "restored structure should have the member")
	assert.NotEmpty(t, info.BoardID)

	// Verify the restored structure has the same board count.
	origBoards, err := client.BoardListBoards(ctx, structure)
	require.NoError(t, err)
	restoredBoards, err := client.BoardListBoards(ctx, restored)
	require.NoError(t, err)
	assert.Equal(t, len(origBoards), len(restoredBoards), "board count should match after restore")
}

// TestPingFixtureMatchesExpectedProtocolVersion pins the shared ping fixture to
// the Go constant.
//
// It reads the fixture off disk and needs no worker binary. Without it, a later
// phase could bump the worker and the fixture together, leave the Go constant
// behind, and see a green suite.
func TestPingFixtureMatchesExpectedProtocolVersion(t *testing.T) {
	path := filepath.Join(contractFixtureDir, "ping.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err, "failed to read %s", path)

	var fixture struct {
		ExpectedResponse struct {
			Result json.RawMessage `json:"result"`
		} `json:"expected_response"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture), "failed to parse %s", path)

	// Decoded separately so a fixture that went back to a bare "pong" reports as
	// the wrong shape rather than as malformed JSON.
	var result pingResult
	require.NoError(t, json.Unmarshal(fixture.ExpectedResponse.Result, &result),
		"%s pins a result that is not a protocol version object: %s",
		path, fixture.ExpectedResponse.Result)

	require.NotNil(t, result.ProtocolVersion,
		"%s must pin a protocol_version", path)
	assert.Equal(t, expectedProtocolVersion, *result.ProtocolVersion,
		"ping.json and expectedProtocolVersion have drifted; the Go job cannot catch this any other way")
}

func TestCheckExpectedError(t *testing.T) {
	raw := func(m map[string]string) map[string]json.RawMessage {
		out := make(map[string]json.RawMessage, len(m))
		for k, v := range m {
			encoded, err := json.Marshal(v)
			require.NoError(t, err)
			out[k] = encoded
		}
		return out
	}

	tests := []struct {
		name        string
		expected    map[string]string
		actual      map[string]string
		wantErr     bool
		errContains []string
	}{
		{
			name:     "code matches and no message_contains skips the message",
			expected: map[string]string{"code": "INVALID_PLAN"},
			actual:   map[string]string{"code": "INVALID_PLAN", "message": "anything at all"},
		},
		{
			name:     "message_contains is a substring",
			expected: map[string]string{"code": "INVALID_PLAN", "message_contains": "failed validation"},
			actual:   map[string]string{"code": "INVALID_PLAN", "message": "plan failed validation: level 3"},
		},
		{
			name:        "message_contains is not a substring",
			expected:    map[string]string{"code": "INVALID_PLAN", "message_contains": "failed validation"},
			actual:      map[string]string{"code": "INVALID_PLAN", "message": "failed to deserialize plan: eof"},
			wantErr:     true,
			errContains: []string{"failed validation", "failed to deserialize plan: eof"},
		},
		{
			name:        "message_contains set but the response carried no message",
			expected:    map[string]string{"code": "INVALID_PLAN", "message_contains": "failed validation"},
			actual:      map[string]string{"code": "INVALID_PLAN"},
			wantErr:     true,
			errContains: []string{"no error message"},
		},
		{
			name:        "code mismatch",
			expected:    map[string]string{"code": "INVALID_PLAN"},
			actual:      map[string]string{"code": "INVALID_PARAMS"},
			wantErr:     true,
			errContains: []string{"INVALID_PLAN", "INVALID_PARAMS"},
		},
		{
			name:        "a misspelled key asserts nothing, so it fails",
			expected:    map[string]string{"code": "INVALID_PLAN", "message_contain": "failed validation"},
			actual:      map[string]string{"code": "INVALID_PLAN", "message": "unrelated"},
			wantErr:     true,
			errContains: []string{"message_contain"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkExpectedError("fx", raw(tc.expected), raw(tc.actual))
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, want := range tc.errContains {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// TestCheckExpectedErrorRejectsNonStringValues covers the branches the
// string-valued table above cannot reach. A JSON null unmarshals into a Go
// string as a no-op with no error, which would leave the wanted substring
// empty and match every message.
func TestCheckExpectedErrorRejectsNonStringValues(t *testing.T) {
	actual := map[string]json.RawMessage{
		"code":    json.RawMessage(`"UNKNOWN_OP"`),
		"message": json.RawMessage(`"unknown operation: bogus"`),
	}

	tests := []struct {
		name        string
		expected    map[string]json.RawMessage
		errContains string
	}{
		{
			name: "message_contains is null",
			expected: map[string]json.RawMessage{
				"code":             json.RawMessage(`"UNKNOWN_OP"`),
				"message_contains": json.RawMessage(`null`),
			},
			errContains: "not a JSON string",
		},
		{
			name: "message_contains is a number",
			expected: map[string]json.RawMessage{
				"code":             json.RawMessage(`"UNKNOWN_OP"`),
				"message_contains": json.RawMessage(`5`),
			},
			errContains: "not a JSON string",
		},
		{
			name: "message_contains is empty",
			expected: map[string]json.RawMessage{
				"code":             json.RawMessage(`"UNKNOWN_OP"`),
				"message_contains": json.RawMessage(`""`),
			},
			errContains: "empty",
		},
		{
			name: "code is not a string",
			expected: map[string]json.RawMessage{
				"code": json.RawMessage(`5`),
			},
			errContains: "not a JSON string",
		},
		{
			name: "code is null",
			expected: map[string]json.RawMessage{
				"code": json.RawMessage(`null`),
			},
			errContains: "not a JSON string",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkExpectedError("fx", tc.expected, actual)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errContains)
		})
	}
}

// TestCheckExpectedErrorRejectsANilMap covers a null expected_response.error
// reaching the check directly. A JSON null decodes to a nil map, which has no
// keys to compare, so it has to be rejected rather than iterated.
func TestCheckExpectedErrorRejectsANilMap(t *testing.T) {
	actual := map[string]json.RawMessage{"code": json.RawMessage(`"ANYTHING"`)}
	err := checkExpectedError("fx", nil, actual)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "asserts nothing")
}

func TestCheckExpectedErrorRejectsAnEmptyErrorObject(t *testing.T) {
	actual := map[string]json.RawMessage{"code": json.RawMessage(`"UNKNOWN_OP"`)}
	err := checkExpectedError("fx", map[string]json.RawMessage{}, actual)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "asserts nothing")
}

// TestCheckExpectedErrorCodeIsOptional pins that a fixture may assert the
// message alone. A message that is diagnostic across two codes is worth
// pinning without also pinning which code carried it.
func TestCheckExpectedErrorCodeIsOptional(t *testing.T) {
	actual := map[string]json.RawMessage{
		"code":    json.RawMessage(`"UNKNOWN_OP"`),
		"message": json.RawMessage(`"unknown operation: bogus"`),
	}
	expected := map[string]json.RawMessage{"message_contains": json.RawMessage(`"bogus"`)}
	assert.NoError(t, checkExpectedError("fx", expected, actual))

	expected["message_contains"] = json.RawMessage(`"NOTPRESENT"`)
	assert.Error(t, checkExpectedError("fx", expected, actual),
		"the message check must still bite when no code is asserted")
}
