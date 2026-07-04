// Each integration test file (`tests/*.rs`) compiles `mod common;` into its own
// crate, so a helper used by only some test binaries reads as dead code in the
// others. Allow it here rather than sprinkling per-fn attributes.
#![allow(dead_code)]

use std::io::{BufRead, BufReader, Write};
use std::process::{Child, Command, Stdio};

/// Spawns the network-engine-worker binary for integration testing.
pub fn spawn_worker() -> Child {
    Command::new(env!("CARGO_BIN_EXE_network-engine-worker"))
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("failed to spawn worker")
}

/// Returns true if `line` parses as JSON carrying `"type":"signal"`.
///
/// The worker interleaves NDJSON `signal` lines (engine warnings) with response
/// lines on stdout. Callers use this to tell them apart. A line that is not
/// valid JSON, or lacks the discriminator, is not a signal.
pub fn is_signal_line(line: &str) -> bool {
    serde_json::from_str::<serde_json::Value>(line)
        .ok()
        .and_then(|v| {
            v.get("type")
                .and_then(|t| t.as_str())
                .map(|s| s == "signal")
        })
        .unwrap_or(false)
}

/// Sends a JSON line to the worker and reads the response, skipping any signal
/// lines emitted while the request was handled. Panics if the worker exits
/// without responding (EOF).
///
/// NOTE: Creates a new BufReader per call. This is safe because the worker only
/// writes in response to a request and blocks on stdin afterward, so nothing is
/// buffered past the response line when the reader is dropped. Signal lines that
/// precede the response are drained within this same reader, so they are never
/// mistaken for the next request's response. This holds only while signals are
/// emitted synchronously within dispatch. A background logger would break it.
pub fn send_receive(child: &mut Child, request: &str) -> String {
    let stdin = child.stdin.as_mut().expect("stdin not available");
    writeln!(stdin, "{}", request).expect("failed to write to stdin");
    stdin.flush().expect("failed to flush stdin");

    let stdout = child.stdout.as_mut().expect("stdout not available");
    let mut reader = BufReader::new(stdout);
    loop {
        let mut line = String::new();
        let bytes_read = reader
            .read_line(&mut line)
            .expect("failed to read from stdout");
        if bytes_read == 0 {
            let stderr = child.stderr.as_mut().map(|s| {
                let mut buf = String::new();
                let mut reader = BufReader::new(s);
                let _ = reader.read_line(&mut buf);
                buf
            });
            panic!(
                "worker exited without responding. stderr: {}",
                stderr.unwrap_or_default().trim()
            );
        }
        let trimmed = line.trim().to_string();
        if is_signal_line(&trimmed) {
            continue;
        }
        return trimmed;
    }
}

/// Sends a request and returns every stdout line the worker emits while handling
/// it, in order, up to and including the first non-signal (response) line.
///
/// Signal lines flush ahead of the response on the same thread, so the returned
/// Vec holds any signals first and the response last. Use this when a test must
/// inspect the signals themselves, not just the response.
pub fn send_collect(child: &mut Child, request: &str) -> Vec<String> {
    let stdin = child.stdin.as_mut().expect("stdin not available");
    writeln!(stdin, "{}", request).expect("failed to write to stdin");
    stdin.flush().expect("failed to flush stdin");

    let stdout = child.stdout.as_mut().expect("stdout not available");
    let mut reader = BufReader::new(stdout);
    let mut lines = Vec::new();
    loop {
        let mut line = String::new();
        let bytes_read = reader
            .read_line(&mut line)
            .expect("failed to read from stdout");
        if bytes_read == 0 {
            panic!("worker exited without responding");
        }
        let trimmed = line.trim().to_string();
        let is_signal = is_signal_line(&trimmed);
        lines.push(trimmed);
        if !is_signal {
            return lines;
        }
    }
}
