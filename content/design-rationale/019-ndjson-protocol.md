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

The worker uses a fixed set of error codes. Handlers return these codes in the `error.code` field.

| Code | Meaning |
|------|---------|
| `STRUCTURE_NOT_FOUND` | Named structure does not exist in the loaded plan, or no tree has been created |
| `USER_NOT_FOUND` | Referenced user does not exist in the tree |
| `USER_ALREADY_EXISTS` | User already exists in the tree |
| `ROOT_ALREADY_EXISTS` | Tree already has a root node |
| `POSITION_OCCUPIED` | Target position in the tree is already taken |
| `INVALID_POSITION` | Position value is not valid for this tree type |
| `TREE_EXISTS` | A tree with this name already exists |
| `INVALID_PLAN` | Compensation plan data is malformed or invalid |
| `UNSUPPORTED_PLAN_VERSION` | Plan `version` is not supported by this engine build. Distinct from `INVALID_PLAN`: the plan parsed cleanly but targets a schema version the engine does not implement |
| `HAS_CHILDREN` | Cannot remove a node that has children |
| `NO_ROOT` | Reserved. Tree operation requires a root but none has been set. Currently handled via `STRUCTURE_NOT_FOUND`. |
| `NO_PLAN` | Commission calculation requires a loaded plan |
| `INVALID_PARAMS` | Params are missing, malformed, or not a JSON object |
| `MISSING_PARAM` | A required parameter is absent |
| `INVALID_UUID` | A user ID is not a valid UUID |
| `CALCULATION_ERROR` | Commission calculation failed (bad input data) |
| `INVALID_REQUEST` | Request JSON itself is malformed |
| `UNKNOWN_OP` | Unrecognized operation name |
| `PARSE_ERROR` | Reserved. JSON parsing failed on the request or params. Currently handled via `INVALID_PARAMS` or `INVALID_REQUEST`. |
| `INTERNAL_ERROR` | Handler panicked unexpectedly |

The error codes evolved during implementation to be more specific. The original design used generic codes like `NO_TREE`, `NOT_FOUND`, and `DUPLICATE_USER`. Implementation revealed that callers need finer distinctions (e.g., `POSITION_OCCUPIED` vs. `USER_ALREADY_EXISTS`, `TREE_EXISTS` vs. `ROOT_ALREADY_EXISTS`).

On the Go side, `EngineError` wraps these codes. Callers use `errors.As` to match on specific codes without parsing error message strings.

### Panic Recovery

The worker's main loop wraps each `dispatch` call in `panic::catch_unwind`. If a handler panics, the worker returns an `INTERNAL_ERROR` response and continues processing the next request.

Without this, a panic in any handler would crash the process. The Go side would see EOF on stdout and need to restart the worker. With catch_unwind, one bad request does not break the connection. The Go caller gets a typed error and can decide whether to retry or surface it.

`AssertUnwindSafe` is used because the handler takes `&mut WorkerState`. This is safe in practice because a panicking handler does not produce a partial state mutation. Tree operations are atomic (insert or error, never partial insert). Plan loading replaces the entire plan.

### Context Cancellation

A background goroutine owns the read side of stdout. It runs for the life of the transport and drains stdout line by line into a buffered channel.

`StdioTransport.Call` writes the request to stdin, then reads lines from the channel. It forwards signals to the observability pipeline and returns the first response to the caller. The read selects against `ctx.Done()`.

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

### JSON Object Key Order Is Not Part Of The Contract

Object key order in worker responses is deterministic and identical in every
build configuration. It is **not** an interoperability guarantee, and the Go
side must not depend on it.

Go decodes with `encoding/json` struct tags, which ignore order. A reorder on
the Rust side is a heads-up that something changed, not a compatibility break.
Nothing on either side may compare raw response bytes.

For the record, since the mechanism is easy to re-derive wrongly:

| What | Order emitted |
|---|---|
| `serde_json::Value` payloads (the `result` body) | Sorted, because `Value` is backed by a `BTreeMap` |
| The response envelope (`id`, `ok`, `result`, `error`) | Struct declaration order, which is *not* alphabetical |

So "the worker sorts its keys" is true of payloads and false of the envelope.
Writing the guarantee down as "sorted" would have been wrong at the top level of
every response.

This was worth stating because it was briefly untrue. Until HEU-648, a
`serde_json/preserve_order` dev-feature reached the worker through Cargo feature
unification under `cargo test` and `cargo clippy --all-targets`, but never under
`cargo build`. `Value` payloads emitted insertion order in test builds and
sorted order in the shipped binary, so the wire format was verified against a
configuration that never shipped. `network-engine-worker/src/protocol.rs` holds
a regression test, `value_payload_emits_sorted_keys`, that fails if that feature
returns. It fires only at workspace scope, which is what CI runs.

Deterministic-and-build-independent is the property that matters. If a future
change moves responses off `serde_json::Value` and serializes from typed structs
instead, the emitted order changes and that is not a contract break. ADR-029's
volume work is a live candidate for exactly that.

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
- **Cross-language observability.** The worker's logs cross the boundary as signals on the same stdout stream. Go forwards them to its telemetry pipeline, correlated by trace context when the request carried it.
