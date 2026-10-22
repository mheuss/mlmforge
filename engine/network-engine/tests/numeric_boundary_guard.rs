//! Production code on the numeric boundary holds no f64 and no lossy conversion.
//!
//! This is a best-effort tripwire. The decimal newtypes and the exact JSON
//! number reader are what keep floats out. A text scan only catches the
//! obvious reintroduction. It cannot see an untyped float literal such as
//! `try_from(0.07)`, or a float reached through a type alias defined outside
//! the scanned files.

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

/// Files left out of the scan, as (file, the module file declaring it, its
/// declaration), where the declaration must be test-gated.
const TEST_ONLY: &[(&str, &str, &str)] = &[(
    "src/commission/test_helpers.rs",
    "src/commission/mod.rs",
    "mod test_helpers;",
)];

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
    BlockComment(usize),
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
            Open::BlockComment(depth) => {
                if c == '*' && next == Some('/') {
                    *open = if depth == 1 {
                        Open::Code
                    } else {
                        Open::BlockComment(depth - 1)
                    };
                    i += 1;
                } else if c == '/' && next == Some('*') {
                    *open = Open::BlockComment(depth + 1);
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
                let word_at = |k: usize| chars[k].is_alphanumeric() || chars[k] == '_';
                let prefixed =
                    i > 0 && matches!(chars[i - 1], 'b' | 'c') && !(i > 1 && word_at(i - 2));
                let raw_start = i == 0 || !word_at(i - 1) || prefixed;
                let hashes = chars[i + 1..].iter().take_while(|&&h| h == '#').count();
                if c == '/' && next == Some('/') {
                    break;
                } else if c == '/' && next == Some('*') {
                    *open = Open::BlockComment(1);
                    i += 1;
                } else if c == 'r' && raw_start && chars.get(i + 1 + hashes) == Some(&'"') {
                    *open = Open::RawStr(hashes);
                    out.push('"');
                    i += 1 + hashes;
                } else if c == '"' {
                    *open = Open::Str;
                    out.push('"');
                } else if c == '\'' && next == Some('\\') {
                    let close = chars
                        .get(i + 3..)
                        .and_then(|rest| rest.iter().position(|&q| q == '\''));
                    out.push_str("' '");
                    i += close.map_or(1, |p| p + 3);
                } else if c == '\'' && chars.get(i + 2) == Some(&'\'') {
                    out.push_str("' '");
                    i += 2;
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
            let next =
                depth + code[i].matches('{').count() as i32 - code[i].matches('}').count() as i32;
            if next < 0 {
                break;
            }
            depth = next;
            opened |= code[i].contains('{');
            let end = code[i].trim_end();
            let ends_item = end.ends_with(';') || end.ends_with('}') || end.ends_with(',');
            i += 1;
            if (opened && depth <= 0) || (!opened && ends_item) {
                break;
            }
        }
    }
    out
}

/// Where a scan of `text` stands after its last line.
fn end_state(text: &str) -> Open {
    let mut open = Open::Code;
    for line in text.lines() {
        code_only(line, &mut open);
    }
    open
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
        let before = files.len();
        rust_files(&path, &mut files);
        assert!(
            files.len() > before,
            "expected {} to hold at least one .rs file",
            path.display()
        );
    }
    for (_, module, declaration) in TEST_ONLY {
        let text = std::fs::read_to_string(root.join(module)).unwrap();
        let ungated: Vec<_> = production_lines(&text)
            .into_iter()
            .filter(|(_, code)| code.contains(declaration))
            .collect();
        assert!(
            ungated.is_empty(),
            "expected `{declaration}` in {module} to be test-gated, found it ungated at {ungated:?}"
        );
    }
    let test_only: Vec<PathBuf> = TEST_ONLY.iter().map(|(f, _, _)| root.join(f)).collect();
    let mut found = Vec::new();
    for file in files.iter().filter(|f| !test_only.contains(f)) {
        let text = std::fs::read_to_string(file).unwrap();
        assert!(
            end_state(&text) == Open::Code,
            "expected the scan of {} to end outside any comment or string",
            file.display()
        );
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
        "struct S {\n",
        "    #[cfg(test)]\n",
        "    pub a: u32,\n",
        "    pub b: f64,\n",
        "}\n",
        "enum E {\n",
        "    #[cfg(test)]\n",
        "    A,\n",
        "    B(f64),\n",
        "}\n",
        "fn m(x: u8, #[cfg(test)] y: u8,\n",
        "    #[cfg(test)]\n",
        "    z: u8,\n",
        "    w: f32) {\n",
        "    match x {\n",
        "        #[cfg(test)]\n",
        "        0 => 1,\n",
        "        _ => 2.0f64 as u8,\n",
        "    };\n",
        "}\n",
    );
    let lines: Vec<usize> = production_lines(text)
        .into_iter()
        .filter(|(_, code)| FORBIDDEN.iter().any(|w| is_hit(code, w)))
        .map(|(n, _)| n)
        .collect();
    assert_eq!(lines, vec![3, 9, 12, 16, 21, 26, 30]);
}

#[test]
fn literal_suffixes_and_conversions_are_hits_and_text_is_not() {
    for code in [
        "x(0.07f64)",
        "x(0.07_f64)",
        "Decimal::from_f64(v)",
        "let v: f32 = 0.0;",
        "let q = ('\\'', '\"'); let v: f64 = 0.0;",
        "let q = ('\\'','\"'); let v: f64 = 0.0;",
        "let p = br\"C:\\\"; let v: f64 = 0.0;",
        "/* a /* b */ it's \" */ let v: f64 = 0.0;",
    ] {
        assert!(
            FORBIDDEN
                .iter()
                .any(|w| is_hit(&code_only(code, &mut Open::Code), w)),
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
                .any(|w| is_hit(&code_only(code, &mut Open::Code), w)),
            "{code}"
        );
    }
}
