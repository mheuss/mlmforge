//! Width contract, Rust layer (HEU-513 MVF-2, Task 15).
//!
//! The engine config must reject over-max values for every field the width
//! manifest tightens. This is the third layer of the boundary check, alongside
//! the Go per-field boundary test (`internal/config/width_contract_test.go`,
//! Task 13) and the Go schema cross-check (Task 14).
//!
//! Approach: for each manifest field, load its engine-shape fixture, assert the
//! whole plan deserializes into the engine `CompensationPlan` (pristine-first —
//! so a broken fixture can't make the rejection below pass for the wrong reason),
//! then set that one field to `over_max` via its `engine_pointer` and assert serde
//! now rejects the payload. Deserializing the whole plan reaches the nested typed
//! field through serde, so no per-struct Rust type mapping is needed — which would
//! be wrong anyway (Go `UnilevelCommission.CommissionableDepth` is Rust
//! `LevelCommissionConfig.max_depth`, and `BoardCyclingConfig` lives in two Rust
//! modules). Completeness is enforced Go-side (Task 12); this pins each known
//! field's Rust type as narrow.

use network_engine::config::CompensationPlan;
use serde::Deserialize;
use serde_json::Value;
use std::fs;
use std::path::PathBuf;

#[derive(Deserialize)]
struct Manifest {
    fields: Vec<Field>,
}

/// Only the fields this test reads. serde ignores the rest of each manifest entry
/// (go_type, rust_type, schema_pointer, go_fixture, go_pointer).
#[derive(Deserialize)]
struct Field {
    go_struct: String,
    go_field: String,
    over_max: i64,
    engine_fixture: String,
    engine_pointer: String,
}

fn contract_dir() -> PathBuf {
    // The test CWD is not reliable; anchor on the crate root.
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../testdata/config_contract")
}

fn load_manifest() -> Manifest {
    let path = contract_dir().join("width_manifest.json");
    let data = fs::read_to_string(&path)
        .unwrap_or_else(|e| panic!("read manifest {}: {e}", path.display()));
    let manifest: Manifest = serde_json::from_str(&data).expect("parse width manifest");
    assert!(
        !manifest.fields.is_empty(),
        "width manifest declares no fields"
    );
    manifest
}

fn load_engine_fixture(name: &str) -> Value {
    let path = contract_dir().join("fixtures").join(format!("{name}.json"));
    let data = fs::read_to_string(&path)
        .unwrap_or_else(|e| panic!("read fixture {}: {e}", path.display()));
    // The serde_json `preserve_order` dev-feature keeps object key order, so each
    // structure stays type-before-config and the adjacently-tagged StructureConfig
    // deserializes (a sorted Value would emit config-before-type and fail).
    serde_json::from_str(&data).expect("parse engine fixture")
}

/// Sets the number at an RFC 6901 engine pointer to `over_max`. The pointer must
/// already resolve to a number — a stale manifest pointer panics loudly rather
/// than leaving the document unmutated, which would let the rejection below pass
/// vacuously.
fn set_over_max(doc: &mut Value, pointer: &str, over_max: i64) {
    let slot = doc
        .pointer_mut(pointer)
        .unwrap_or_else(|| panic!("engine_pointer {pointer} does not resolve in the fixture"));
    assert!(
        slot.is_number(),
        "engine_pointer {pointer} targets {slot}, not a number"
    );
    *slot = Value::from(over_max);
}

#[test]
fn rust_config_rejects_over_max_widths() {
    let manifest = load_manifest();
    for f in &manifest.fields {
        let label = format!("{}.{}", f.go_struct, f.go_field);
        let pristine = load_engine_fixture(&f.engine_fixture);

        // Pristine-first: the unmutated fixture must deserialize, or the rejection
        // below could be true for an unrelated reason (a silent vacuous pass).
        assert!(
            serde_json::from_value::<CompensationPlan>(pristine.clone()).is_ok(),
            "{label}: fixture {} must deserialize before mutation",
            f.engine_fixture
        );

        let mut doc = pristine;
        set_over_max(&mut doc, &f.engine_pointer, f.over_max);
        assert!(
            serde_json::from_value::<CompensationPlan>(doc).is_err(),
            "{label}: engine payload must reject {} at {} — is the Rust type too wide?",
            f.over_max,
            f.engine_pointer
        );
    }
}
