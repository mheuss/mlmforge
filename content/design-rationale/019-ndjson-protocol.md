# 019: NDJSON Protocol

## The Problem

Decision 003 establishes that the Rust engine runs as a subprocess communicating via NDJSON over stdin/stdout. That decision covered the "why" (performance, language boundary) but not the protocol specifics. How are requests correlated with responses? What happens when the worker panics? How does Go cancel a blocked read? How does the worker's own logging reach Go? What error codes does the worker return?

These details matter for reliability, debuggability, and the Go-Rust contract.

## Decisions

### Request-Response Envelope

Every message is a single JSON object followed by a newline. Requests flow from Go to Rust on stdin. Responses flow from Rust to Go on stdout.

Request:
```json
{"id": "req-1", "op": "get_children", "params": {"user_id": "abc"}}
```

Response (success):
```json
{"id": "req-1", "ok": true, "result": [...]}
```

Response (error):
```json
{"id": "req-1", "ok": false, "error": {"code": "STRUCTURE_NOT_FOUND", "message": "no tree loaded"}}
```

The `id` field correlates requests with responses. The Go transport generates monotonically increasing IDs (`req-1`, `req-2`, ...) and validates that the response ID matches. A mismatch indicates a protocol desynchronization and is treated as a fatal error.

The `ok` boolean makes success/failure unambiguous without inspecting the `result` or `error` fields. `result` is omitted on error. `error` is omitted on success.

A request may also carry two optional fields, `trace_id` and `span_id`. They let the worker's log output correlate with the Go caller's trace. Both are omitted when the caller has no active trace. The worker echoes them onto any signal a request produces. The next section describes them.

The same request with trace context set:
```json
{"id": "req-1", "op": "get_children", "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736", "span_id": "00f067aa0ba902b7", "params": {"user_id": "abc"}}
```

### Signal Messages

The worker emits its log output as signals. They flow from Rust to Go on stdout, interleaved with responses. This carries structured logs across the language boundary without a second channel.

A signal is a single JSON object followed by a newline:

```json
{"type": "signal", "level": "warn", "target": "network_engine::commission::binary", "message": "pairing percent outside [0.0, 1.0]", "fields": {"percent": 1.4}, "trace_id": "...", "span_id": "...", "timestamp": "2026-07-03T12:00:00Z"}
```

The `type` field is the discriminator. Signals always carry `"type": "signal"`. Responses never carry a `type` field. The Go reader routes each stdout line by this field. A line with `"type": "signal"` goes to the observability pipeline. Any other line is a response.

The remaining fields describe the log event. `level` is the severity (`error`, `warn`, `info`, `debug`, or `trace`). `target` is the Rust module path that emitted it. `message` is the human-readable text. `fields` holds the structured key-value pairs from the log call. `timestamp` is RFC3339.

`trace_id` and `span_id` appear only when the triggering request carried them. They tie the signal back to the Go operation that caused it.

### RawValue for Params

The Rust `Request` struct deserializes `params` as `Box<serde_json::value::RawValue>` instead of `serde_json::Value`. This preserves the raw JSON bytes without intermediate parsing.

`serde_json::Value` uses `BTreeMap` for objects, which reorders keys alphabetically. This breaks two things: non-string map keys (like `BTreeMap<u8, f64>` in rate tables, where integer keys are serialized as JSON strings) and adjacently-tagged enums when the `type` field appears after the content field.

`RawValue` avoids both problems. The handler deserializes params directly into the target type, bypassing the intermediate `Value` representation. When params are omitted, a default of `{}` is used.

### Error Code Taxonomy

The worker returns an error code in the `error.code` field. There are 42 of
them, grouped below by the area that raises them. Codes are shared across areas
where the condition is the same, so each one is listed once.

**Tree topology.** Raised by `tree_error_to_response` in `handlers/common.rs`,
which maps every `TreeError` variant, plus a few handler-level checks.

| Code | Meaning |
|------|---------|
| `STRUCTURE_NOT_FOUND` | Named structure does not exist in the loaded plan, or no tree has been created |
| `USER_NOT_FOUND` | Referenced user does not exist in the tree |
| `USER_ALREADY_EXISTS` | User already exists in the tree |
| `ROOT_ALREADY_EXISTS` | Tree already has a root node |
| `POSITION_OCCUPIED` | Target position in the tree is already taken |
| `INVALID_POSITION` | Position value is out of range for this tree type |
| `TREE_EXISTS` | A structure with this name already exists. Also returned when restoring a snapshot over a live structure |
| `HAS_CHILDREN` | Cannot remove a node that has children |
| `CANNOT_REMOVE_ROOT` | Cannot remove the root node |
| `TREE_EMPTY` | Operation requires at least one node and the tree has none |
| `INVALID_WIDTH` | Configured matrix width is not valid |
| `SPONSOR_NOT_FOUND` | Named sponsor does not exist. Also raised by board and streamline placement |
| `USER_NOT_IN_HOLDING_TANK` | `place_from_tank` named a user who is not in the tank |
| `UNSUPPORTED_SPILLOVER` | The requested spillover strategy is not implemented for this tree type |
| `SUBTREE_FULL` | Spillover found no open slot in the target subtree |

**Plan loading.** Raised by `handle_load_plan` and by the commission handlers'
`require_plan` gate. See [028](028-commission-config-from-validated-state.md).

| Code | Meaning |
|------|---------|
| `INVALID_PLAN` | Plan failed to deserialize, or failed validation |
| `UNSUPPORTED_PLAN_VERSION` | Plan `version` is not supported by this engine build. Distinct from `INVALID_PLAN`: the plan parsed cleanly but targets a schema version the engine does not implement |
| `NO_PLAN` | The operation requires a loaded plan and `load_plan` has not been called |

**Board plan.** Mapped from `BoardPlanError` in `handlers/board_plan.rs`.

| Code | Meaning |
|------|---------|
| `BOARD_NOT_FOUND` | Named board does not exist in the structure |
| `MEMBER_NOT_FOUND` | Referenced member is not on the board. Also raised by streamline |
| `MEMBER_ALREADY_EXISTS` | Member is already on the board. Also raised by streamline |
| `MEMBER_NOT_DISPLACED` | Operation requires a displaced member and this one is not displaced |
| `NO_BOARDS_AVAILABLE` | No board has an open slot for placement |
| `INVALID_DIMENSIONS` | Board width or height is outside the allowed bounds |

**Streamline.** Mapped from `StreamlineError` in `handlers/streamline.rs`.

| Code | Meaning |
|------|---------|
| `STREAM_NOT_FOUND` | Named stream does not exist in the structure |
| `STREAM_FROZEN` | Stream is frozen after a rank demotion and cannot take placements |
| `STREAM_CHOICE_NOT_ALLOWED` | Caller supplied a stream but the config does not permit enrollment stream choice |
| `SPONSOR_NOT_OWNER` | Sponsor does not own the stream they named |
| `NO_STREAMS_AVAILABLE` | No stream can take the placement |
| `NO_OWNED_STREAMS` | Sponsor owns no stream to place into |
| `TREE_ERROR` | A `TreeError` surfaced from inside a stream's underlying `UnilevelTree` |
| `STREAMLINE_ERROR` | Fallback for a streamline condition with no dedicated code |

**Calculation and state.**

| Code | Meaning |
|------|---------|
| `CALCULATION_ERROR` | Commission calculation failed (bad input data) |
| `EVALUATION_ERROR` | Rank evaluation failed, including non-convergence |
| `SERIALIZATION_ERROR` | Snapshot could not be serialized |

**Protocol.**

| Code | Meaning |
|------|---------|
| `INVALID_REQUEST` | Request JSON itself is malformed |
| `INVALID_PARAMS` | Params are malformed or not a JSON object. Omitting `params` entirely is legal and deserializes as `{}`, so a genuinely required field that is absent returns `MISSING_PARAM` instead |
| `MISSING_PARAM` | A required parameter is absent |
| `INVALID_UUID` | A user ID is not a valid UUID |
| `UNKNOWN_OP` | Unrecognized operation name |
| `UNSUPPORTED_OP` | Known operation, but not supported for this structure type |
| `INTERNAL_ERROR` | Handler panicked unexpectedly |

The codes grew more specific during implementation. The original design used
generic codes like `NO_TREE`, `NOT_FOUND`, and `DUPLICATE_USER`. Implementation
revealed that callers need finer distinctions, for example `POSITION_OCCUPIED`
versus `USER_ALREADY_EXISTS`, and `TREE_EXISTS` versus `ROOT_ALREADY_EXISTS`.
None of the generic codes survive in the worker. Two codes this document once
reserved, `NO_ROOT` and `PARSE_ERROR`, were never implemented and have been
dropped. `NO_ROOT`'s condition is reported as `STRUCTURE_NOT_FOUND` or
`TREE_EMPTY`, and `PARSE_ERROR`'s as `INVALID_PARAMS` or `INVALID_REQUEST`.

On the Go side, `EngineError` wraps these codes. Callers use `errors.As` to
match on specific codes without parsing error message strings.

### Panic Recovery

The worker's main loop wraps each `dispatch` call in `panic::catch_unwind`. If a handler panics, the worker returns an `INTERNAL_ERROR` response and continues processing the next request.

Without this, a panic in any handler would crash the process. The Go side would see EOF on stdout and need to restart the worker. With catch_unwind, one bad request does not break the connection. The Go caller gets a typed error and can decide whether to retry or surface it.

`AssertUnwindSafe` is used because the handler takes `&mut WorkerState`. This is safe in practice because a panicking handler does not produce a partial state mutation. Tree operations are atomic (insert or error, never partial insert). Plan loading replaces the entire plan.

### Context Cancellation

A background goroutine owns the read side of stdout. It runs for the life of the transport and drains stdout line by line into a buffered channel.

`StdioTransport.Call` writes the request to stdin, then reads lines from the channel. It forwards signals to a registered signal handler and returns the first response to the caller. The handler is opt-in: with none registered, `deliverSignal` drops the line (`transport_stdio.go:258`). `observability.Observer.HandleSignal` is the intended handler, wired via `WithSignalHandler`, but nothing in production registers it today. The read selects against `ctx.Done()`.

```
write request to stdin
loop:
  select:
    case line received:
      signal   -> forward to observability, keep reading
      response -> unmarshal and return
    case context cancelled: return ctx.Err()
```

This prevents a hung worker from blocking the Go caller indefinitely. A caller with a timeout context gets a clean cancellation instead of waiting forever for a response that may never come.

A dedicated reader also removes the pipe-buffer deadlock a synchronous reader risks. A per-call synchronous read stops when the caller cancels. The worker can then block writing to a full stdout pipe. The background reader keeps reading after a cancellation and buffers what arrives, so a cancelled caller no longer strands the worker. The worker is single-threaded and emits signals only while handling a request. On a normal call, the request's signals and its response all arrive within that call's window, so the channel returns to empty. Cancellation is the exception. The worker may finish and write a response after `Call` has already returned `ctx.Err()`, and the reader buffers that late line. To keep it from being read as the next call's response, a cancelled `Call` marks the transport closed. Later calls fail the closed check and return before reading, so the stale line never reaches the id check.

The mutex ensures only one request is in flight at a time. Combined with the atomic `closed` flag (checked before acquiring the mutex), this prevents races between concurrent `Call` and `Close` operations.

### Stderr Capture

The worker's stderr is piped to a `bytes.Buffer` in the Go transport. When a read from stdout fails (e.g., the worker crashes and Go gets EOF), the error message includes the stderr contents.

This surfaces Rust panic messages, assertion failures, and any `eprintln!` diagnostic output. Without stderr capture, a worker crash would produce only "read response: EOF" with no indication of why the worker died.

### Intentionally Unexposed Operations

The Rust library provides `get_branch`, `count_downline`, and `count_branch` operations that are not exposed through the NDJSON protocol. These are internal operations used during commission calculations (e.g., active leg counting). Exposing them would add protocol surface area without a Go-side caller. They remain available for future exposure if needed.

### Mode-Dispatched Operations

Some operations serve multiple calculation modes through one op string, selected by the loaded plan rather than a distinct op.

`calculate_binary_pairing` handles both binary modes. When the plan's `BinaryCommissionMode` is `CycleStep`, the call dispatches internally to the private `calculate_binary_cycle_step`, so those results are reachable over the seam through `calculate_binary_pairing`. There is deliberately no separate `calculate_binary_cycle_step` op. A Go caller selects the mode by the plan it loads, not by the op it sends.

The commission ops are `calculate_unilevel`, `calculate_binary_pairing`, `calculate_matrix`, `calculate_stairstep`, `calculate_generation`, `calculate_streamline`, and `board_calculate_commissions`. That is one op per plan type.

## What We Considered

**Multiplexed requests.** Allow multiple in-flight requests with ID-based response matching. The subprocess model is inherently single-threaded (one stdin, one stdout). Multiplexing adds complexity for no throughput benefit. The mutex serializes requests, which matches the worker's single-threaded dispatch loop.

**Streaming responses.** Large result sets (full downline queries) could stream as multiple NDJSON lines. This complicates the protocol (framing, end-of-stream markers) and the Go transport (buffered reads, partial result handling). Single-response-per-request is simpler. If result sets become too large, pagination at the operation level (e.g., `get_downline` with depth limits) is the right solution.

**Separate error channel.** Use stderr for structured error responses, stdout for success only. This breaks the request-response correlation. A single channel with the `ok` flag is simpler and keeps each request paired with exactly one response.

**Protobuf wire format.** Binary serialization for performance. Adds a code generation step and tooling dependency. NDJSON is human-readable, debuggable with `cat`, and fast enough for the expected message sizes. The protocol overhead is negligible compared to the tree walk computation.

## What This Enables

- **Debuggable protocol.** NDJSON messages are human-readable. Pipe the worker's stdin/stdout to a file and inspect the conversation directly.
- **Typed error handling.** Go callers match on error codes, not message strings. Adding a new error code requires no Go-side changes until a caller needs to handle it specifically.
- **Resilient worker.** A panic in one handler does not crash the process. The Go side sees a typed error and the connection stays alive.
- **Clean cancellation.** Go contexts propagate through to the transport layer. Timeouts and cancellations work as expected.
- **Cross-language observability.** The worker's logs cross the boundary as signals on the same stdout stream, correlated by trace context when the request carried it. The Go side can forward them to its telemetry pipeline by registering a handler. That wiring is not in place yet, so signals are drained and discarded today.
