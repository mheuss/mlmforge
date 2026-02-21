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

/// Sends a JSON line to the worker and reads the response.
/// Panics if the worker exits without responding (EOF).
///
/// NOTE: Creates a new BufReader per call. This is safe because the NDJSON
/// protocol guarantees one response line per request, and the underlying
/// ChildStdout is not wrapped in a persistent BufReader. If BufReader were
/// kept across calls, it could buffer partial data from the next response.
/// Creating a fresh BufReader per call avoids that risk at negligible cost
/// for integration tests.
pub fn send_receive(child: &mut Child, request: &str) -> String {
    let stdin = child.stdin.as_mut().expect("stdin not available");
    writeln!(stdin, "{}", request).expect("failed to write to stdin");
    stdin.flush().expect("failed to flush stdin");

    let stdout = child.stdout.as_mut().expect("stdout not available");
    let mut reader = BufReader::new(stdout);
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
    line.trim().to_string()
}
