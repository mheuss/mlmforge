//! Production code on the numeric boundary holds no f64 and no lossy conversion.
//!
//! A text scan cannot see an untyped float literal such as `try_from(0.07)`,
//! or a float reached through a type alias defined outside the scanned files.

use std::path::{Path, PathBuf};

const SCANNED: &[&str] = &[
    "src/config",
    "src/serde_helpers.rs",
    "src/commission",
    "src/rank",
    "src/board_plan/types.rs",
    "src/decimal.rs",
    "src/lib.rs",
    "../network-engine-worker/src/handlers",
];

/// Test-only files inside a scanned directory.
const TEST_ONLY: &[&str] = &["src/commission/test_helpers.rs"];

const FORBIDDEN: &[&str] = &[
    "f64", "f32", "to_f64", "as_f64", "from_f64", "to_f32", "as_f32", "from_f32",
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

/// Where a scan stands at the end of a line.
#[derive(Clone, Copy, PartialEq)]
enum Open {
    Code,
    BlockComment,
    Str,
    RawStr(usize),
}

/// `line` with string and char literal contents and comments blanked out,
/// carrying an unclosed comment or string across lines in `open`.
fn code_only(line: &str, open: &mut Open) -> String {
    let chars: Vec<char> = line.chars().collect();
    let mut out = String::new();
    let mut i = 0;
    while i < chars.len() {
        let c = chars[i];
        let next = chars.get(i + 1).copied();
        match *open {
            Open::BlockComment => {
                if c == '*' && next == Some('/') {
                    *open = Open::Code;
                    i += 1;
                }
            }
            Open::Str => {
                if c == '\\' {
                    i += 1;
                } else if c == '"' {
                    *open = Open::Code;
                    out.push('"');
                }
            }
            Open::RawStr(hashes) => {
                if c == '"' && (1..=hashes).all(|k| chars.get(i + k) == Some(&'#')) {
                    *open = Open::Code;
                    out.push('"');
                    i += hashes;
                }
            }
            Open::Code => {
                let after_word = i > 0 && (chars[i - 1].is_alphanumeric() || chars[i - 1] == '_');
                let hashes = chars[i + 1..].iter().take_while(|&&h| h == '#').count();
                if c == '/' && next == Some('/') {
                    break;
                } else if c == '/' && next == Some('*') {
                    *open = Open::BlockComment;
                    i += 1;
                } else if c == 'r' && !after_word && chars.get(i + 1 + hashes) == Some(&'"') {
                    *open = Open::RawStr(hashes);
                    out.push('"');
                    i += 1 + hashes;
                } else if c == '"' {
                    *open = Open::Str;
                    out.push('"');
                } else if c == '\'' && chars.get(i + 2) == Some(&'\'') {
                    out.push_str("' '");
                    i += 2;
                } else if c == '\'' && next == Some('\\') {
                    let close = chars[i + 2..].iter().position(|&q| q == '\'');
                    out.push_str("' '");
                    i += close.map_or(1, |p| p + 2);
                } else {
                    out.push(c);
                }
            }
        }
        i += 1;
    }
    out
}

/// Each production line of `text` as (line number, code), dropping items
/// gated by `#[cfg(test)]`.
fn production_lines(text: &str) -> Vec<(usize, String)> {
    let mut open = Open::Code;
    let code: Vec<String> = text.lines().map(|l| code_only(l, &mut open)).collect();
    let mut out = Vec::new();
    let mut i = 0;
    while i < code.len() {
        if code[i].trim() != "#[cfg(test)]" {
            out.push((i + 1, code[i].clone()));
            i += 1;
            continue;
        }
        i += 1;
        let mut depth = 0i32;
        let mut opened = false;
        while i < code.len() {
            depth += code[i].matches('{').count() as i32 - code[i].matches('}').count() as i32;
            opened |= code[i].contains('{');
            let ends_item = code[i].trim_end().ends_with(';') || code[i].trim_end().ends_with('}');
            i += 1;
            if (opened && depth <= 0) || (!opened && ends_item) {
                break;
            }
        }
    }
    out
}

fn is_hit(code: &str, word: &str) -> bool {
    code.match_indices(word).any(|(i, _)| {
        let before: Vec<char> = code[..i].chars().rev().take(2).collect();
        let after = code[i + word.len()..].chars().next();
        let word_char = |c: char| c.is_alphanumeric() || c == '_';
        let literal_suffix = match before.as_slice() {
            [d, ..] if d.is_ascii_digit() => true,
            ['_', d, ..] if d.is_ascii_digit() => true,
            _ => false,
        };
        let starts_word = literal_suffix || !before.first().is_some_and(|&c| word_char(c));
        starts_word && !after.is_some_and(word_char)
    })
}

fn violations() -> Vec<String> {
    let root = Path::new(env!("CARGO_MANIFEST_DIR"));
    let mut files = Vec::new();
    for scanned in SCANNED {
        let path = root.join(scanned);
        assert!(path.exists(), "expected {} to exist", path.display());
        rust_files(&path, &mut files);
    }
    let test_only: Vec<PathBuf> = TEST_ONLY.iter().map(|p| root.join(p)).collect();
    let mut found = Vec::new();
    for file in files.iter().filter(|f| !test_only.contains(f)) {
        let text = std::fs::read_to_string(file).unwrap();
        for (n, code) in production_lines(&text) {
            if FORBIDDEN.iter().any(|word| is_hit(&code, word)) {
                found.push(format!("{}:{}: {}", file.display(), n, code.trim()));
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

#[test]
fn a_cfg_test_item_hides_only_itself() {
    let text = concat!(
        "#[cfg(test)]\n",
        "mod helpers;\n",
        "fn a() -> f64 {}\n",
        "#[cfg(test)]\n",
        "mod tests {\n",
        "    const J: &str = r#\"{\"a\": \"}\"}\"#;\n",
        "    fn b() -> f64 {}\n",
        "}\n",
        "fn c() -> f32 {}\n",
        "const M: &str = \"multi\n",
        "line f64 } text\";\n",
        "fn d() -> f64 {}\n",
    );
    let lines: Vec<usize> = production_lines(text)
        .into_iter()
        .filter(|(_, code)| FORBIDDEN.iter().any(|w| is_hit(code, w)))
        .map(|(n, _)| n)
        .collect();
    assert_eq!(lines, vec![3, 9, 12]);
}

#[test]
fn literal_suffixes_and_conversions_are_hits_and_text_is_not() {
    let mut open = Open::Code;
    for code in [
        "x(0.07f64)",
        "x(0.07_f64)",
        "Decimal::from_f64(v)",
        "let v: f32 = 0.0;",
    ] {
        assert!(
            FORBIDDEN
                .iter()
                .any(|w| is_hit(&code_only(code, &mut open), w)),
            "{code}"
        );
    }
    for code in [
        "let x = \"an f64 here\";",
        "// an f64 here",
        "/* f64 */ let y = 1;",
        "let half = 0.5;",
        "let u = to_f64x;",
        "let r = r#\"an f64 \" here\"#;",
    ] {
        assert!(
            !FORBIDDEN
                .iter()
                .any(|w| is_hit(&code_only(code, &mut open), w)),
            "{code}"
        );
    }
}
