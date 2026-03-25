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
	assert.True(t, cycleEvent.EarnedCommission, "cycled member should earn commission")

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
