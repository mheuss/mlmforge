//! Production code on the numeric boundary holds no f64 and no lossy conversion.

use std::path::{Path, PathBuf};

const SCANNED: &[&str] = &[
    "src/config",
    "src/serde_helpers.rs",
    "src/commission",
    "src/rank",
    "src/board_plan/types.rs",
    "../network-engine-worker/src/handlers",
];

const FORBIDDEN: &[&str] = &[
    "f64",
    "f32",
    "to_f64",
    "as_f64",
    "parse::<f64>",
    "parse::<f32>",
];

fn rust_files(path: &Path, out: &mut Vec<PathBuf>) {
    if path.is_dir() {
        for entry in std::fs::read_dir(path).unwrap() {
            rust_files(&entry.unwrap().path(), out);
        }
    } else if path.extension().is_some_and(|e| e == "rs") {
        out.push(path.to_path_buf());
    }
}

fn violations() -> Vec<String> {
    let root = Path::new(env!("CARGO_MANIFEST_DIR"));
    let mut files = Vec::new();
    for scanned in SCANNED {
        rust_files(&root.join(scanned), &mut files);
    }
    assert!(
        !files.is_empty(),
        "the guard found no files under {}",
        root.display()
    );
    let mut found = Vec::new();
    for file in files {
        let text = std::fs::read_to_string(&file).unwrap();
        let production = text.split("#[cfg(test)]").next().unwrap();
        for (n, line) in production.lines().enumerate() {
            let code = line.split("//").next().unwrap();
            for word in FORBIDDEN {
                let hit = code.match_indices(word).any(|(i, _)| {
                    let before = code[..i].chars().next_back();
                    let after = code[i + word.len()..].chars().next();
                    !before.is_some_and(|c| c.is_alphanumeric() || c == '_')
                        && !after.is_some_and(|c| c.is_alphanumeric() || c == '_')
                });
                if hit {
                    found.push(format!("{}:{}: {}", file.display(), n + 1, line.trim()));
                }
            }
        }
    }
    found
}

#[test]
fn no_f64_or_lossy_conversion_on_the_numeric_boundary() {
    let found = violations();
    assert!(
        found.is_empty(),
        "found on the numeric boundary:\n{}",
        found.join("\n")
    );
}
