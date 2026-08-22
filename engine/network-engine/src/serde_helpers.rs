//! Serde helpers shared by the plan config types and the worker's request
//! params.

use std::collections::BTreeMap;

/// Deserializes a value, reading an explicit JSON `null` as `T::default()`.
///
/// A nil Go slice or map marshals to JSON `null`, not `[]` or `{}`.
/// `#[serde(default)]` covers an *absent* key; it does not cover a key present
/// with a null value. So a caller that leaves a collection unset sends the one
/// shape neither plain serde path accepts.
///
/// Pair it with the field's requiredness:
///
/// | The field is | Attribute | Absent | Null |
/// |---|---|---|---|
/// | Required | `#[serde(deserialize_with = "null_as_empty")]` | error | empty |
/// | Optional | `#[serde(default, deserialize_with = "null_as_empty")]` | empty | empty |
///
/// Do **not** reach for Go's `omitempty` on a required field. On its own it
/// breaks the call: when the collection is empty the key vanishes, this
/// attribute has no `default` to fall back on, and the caller gets
/// `INVALID_PARAMS`. The real trap is what comes next — add `default` to make it
/// work again and a dropped field becomes indistinguishable from an empty one,
/// which on a money path pays zero instead of complaining. Keep required fields
/// null-tolerant and nothing more.
///
/// This widens null to `T::default()` for any `T: Default`. On a collection
/// that reads as "empty", which is what it is for. On a numeric field it would
/// silently produce `0` — the name is chosen so that misuse reads wrong at the
/// call site.
pub fn null_as_empty<'de, D, T>(deserializer: D) -> Result<T, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Default + serde::Deserialize<'de>,
{
    let opt = <Option<T> as serde::Deserialize>::deserialize(deserializer)?;
    Ok(opt.unwrap_or_default())
}

/// Parses JSON string map keys into `u8`, rejecting two spellings of one key.
///
/// `str::parse::<u8>` accepts `"01"`, and so do the schema's `propertyNames`
/// patterns (`^0*(...)`) and Go's `strconv.ParseUint` in `validateU8MapKeys`
/// (`internal/config/rules.go:118`). Rejecting leading zeros here alone would
/// make the worker refuse plans the validated Go pipeline accepts, so this
/// stays *at least* as permissive as they are. It is slightly more permissive
/// in one direction that does not matter: `parse::<u8>` accepts a leading `+`,
/// which both the schema pattern and `strconv.ParseUint` reject. The gate
/// refuses those before the worker ever sees them.
///
/// What it will not accept is `"1"` and `"01"` in the same map. Those name one
/// entry, one value silently wins, and on a money path that is a wrong payout.
/// No caller can have meant it, so it is an error rather than a policy.
///
/// This does not cover a literally repeated key, `{"1": 0.05, "1": 0.09}`.
/// Serde's map visitor collapses that to the last value before this function
/// sees it, so the collision is already gone. Go's `encoding/json` does the
/// same, so both sides agree — but do not read the check below as covering it.
fn parse_u8_keys<E>(raw: BTreeMap<String, f64>) -> Result<BTreeMap<u8, f64>, E>
where
    E: serde::de::Error,
{
    let mut out = BTreeMap::new();
    for (k, v) in raw {
        let key = k
            .parse::<u8>()
            .map_err(|e| E::custom(format!("invalid integer key '{k}': {e}")))?;
        if out.insert(key, v).is_some() {
            return Err(E::custom(format!(
                "duplicate integer key {key}: the map spells it more than one way"
            )));
        }
    }
    Ok(out)
}

/// Deserializes a `BTreeMap<u8, f64>` from JSON string keys.
///
/// JSON object keys are always strings. Reading them into `BTreeMap<u8, _>`
/// directly works only on `serde_json`'s native path, which coerces string keys
/// to integers. Serde's `Content` buffer, used whenever an adjacently- or
/// internally-tagged enum meets its content before its tag, does not coerce.
/// Reading into `BTreeMap<String, _>` first sidesteps the coercion path
/// entirely, so both routes parse the same JSON.
///
/// See `config/mod.rs` on `StructureConfig` for why the buffer gets used, and
/// UC-NET-007 in `docs/use-cases/network-engine.md`.
pub(crate) fn u8_keyed_map<'de, D>(deserializer: D) -> Result<BTreeMap<u8, f64>, D::Error>
where
    D: serde::Deserializer<'de>,
{
    let string_keyed = <BTreeMap<String, f64> as serde::Deserialize>::deserialize(deserializer)?;
    parse_u8_keys::<D::Error>(string_keyed)
}

/// Deserializes a rank-keyed table of `u8`-keyed rate maps.
///
/// Same reasoning as [`u8_keyed_map`]. The outer keys are rank names and stay
/// strings; only the inner keys are parsed. This cannot delegate to
/// [`u8_keyed_map`] through `deserialize_with`, because that attribute applies
/// at the outer field.
pub(crate) fn rank_keyed_u8_map<'de, D>(
    deserializer: D,
) -> Result<BTreeMap<String, BTreeMap<u8, f64>>, D::Error>
where
    D: serde::Deserializer<'de>,
{
    let raw =
        <BTreeMap<String, BTreeMap<String, f64>> as serde::Deserialize>::deserialize(deserializer)?;
    raw.into_iter()
        .map(|(rank, rates)| parse_u8_keys::<D::Error>(rates).map(|parsed| (rank, parsed)))
        .collect()
}

/// Deserializes an optional `BTreeMap<u8, f64>` from JSON string keys.
///
/// Same reasoning as [`u8_keyed_map`]. Pair it with `#[serde(default)]` at the
/// call site: `deserialize_with` disables serde's built-in "absent `Option` is
/// `None`" handling, and without `default` the field silently becomes required.
pub(crate) fn optional_u8_keyed_map<'de, D>(
    deserializer: D,
) -> Result<Option<BTreeMap<u8, f64>>, D::Error>
where
    D: serde::Deserializer<'de>,
{
    let raw = <Option<BTreeMap<String, f64>> as serde::Deserialize>::deserialize(deserializer)?;
    raw.map(parse_u8_keys::<D::Error>).transpose()
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde::Deserialize;

    // `Debug` is required: `unwrap_err()` formats the success type.
    #[derive(Debug, Deserialize)]
    struct Flat {
        #[serde(deserialize_with = "u8_keyed_map")]
        rates: BTreeMap<u8, f64>,
    }

    #[derive(Debug, Deserialize)]
    struct Nested {
        #[serde(deserialize_with = "rank_keyed_u8_map")]
        table: BTreeMap<String, BTreeMap<u8, f64>>,
    }

    // `default` is mandatory alongside `deserialize_with` on an Option field.
    // Serde reads a bare `Option<T>` as `None` when absent; adding
    // `deserialize_with` disables that and makes the field required.
    #[derive(Debug, Deserialize)]
    struct Optional {
        #[serde(default, deserialize_with = "optional_u8_keyed_map")]
        rates: Option<BTreeMap<u8, f64>>,
    }

    #[test]
    fn flat_map_parses_string_keys() {
        let v: Flat = serde_json::from_str(r#"{"rates":{"1":0.05,"255":0.01}}"#).unwrap();
        assert_eq!(v.rates[&1], 0.05);
        assert_eq!(v.rates[&255], 0.01);
    }

    #[test]
    fn flat_map_accepts_empty() {
        let v: Flat = serde_json::from_str(r#"{"rates":{}}"#).unwrap();
        assert!(v.rates.is_empty());
    }

    #[test]
    fn flat_map_rejects_out_of_range_key() {
        let e = serde_json::from_str::<Flat>(r#"{"rates":{"256":0.05}}"#).unwrap_err();
        assert!(e.to_string().contains("256"), "got: {e}");
    }

    #[test]
    fn flat_map_rejects_nonnumeric_key() {
        assert!(serde_json::from_str::<Flat>(r#"{"rates":{"gold":0.05}}"#).is_err());
    }

    /// Leading zeros are accepted, matching the schema's `^0*(...)`
    /// `propertyNames` patterns and Go's `strconv.ParseUint` in
    /// `validateU8MapKeys`. Rejecting them in Rust alone would make the worker
    /// refuse plans the validated Go pipeline accepts.
    #[test]
    fn flat_map_accepts_leading_zeros() {
        let v: Flat = serde_json::from_str(r#"{"rates":{"01":0.05}}"#).unwrap();
        assert_eq!(v.rates[&1], 0.05);
    }

    /// Two spellings of one key is the case worth rejecting. Silently keeping
    /// one of them is a wrong payout on a money path, and no caller can have
    /// meant it.
    #[test]
    fn flat_map_rejects_duplicate_parsed_keys() {
        let e = serde_json::from_str::<Flat>(r#"{"rates":{"1":0.05,"01":0.09}}"#).unwrap_err();
        assert!(e.to_string().contains("duplicate"), "got: {e}");
    }

    /// Assert on the message, not just `is_err()`. A bare `is_err()` would also
    /// pass if the helper had rejected `"02"` outright as a bad key, which is
    /// exactly the wrong behavior the Go/schema parity constraint rules out.
    #[test]
    fn nested_map_rejects_duplicate_parsed_inner_keys() {
        let json = r#"{"table":{"silver":{"2":0.05,"02":0.09}}}"#;
        let e = serde_json::from_str::<Nested>(json).unwrap_err();
        assert!(e.to_string().contains("duplicate"), "got: {e}");
    }

    /// The permissiveness guarantee has to hold on the nested path too, not
    /// only the flat one.
    #[test]
    fn nested_map_accepts_leading_zeros() {
        let v: Nested = serde_json::from_str(r#"{"table":{"silver":{"03":0.05}}}"#).unwrap();
        assert_eq!(v.table["silver"][&3], 0.05);
    }

    #[test]
    fn flat_map_accepts_zero_key() {
        let v: Flat = serde_json::from_str(r#"{"rates":{"0":0.05}}"#).unwrap();
        assert_eq!(v.rates[&0], 0.05);
    }

    #[test]
    fn nested_map_parses_inner_keys() {
        let v: Nested =
            serde_json::from_str(r#"{"table":{"silver":{"3":0.05},"gold":{"1":0.1}}}"#).unwrap();
        assert_eq!(v.table["silver"][&3], 0.05);
        assert_eq!(v.table["gold"][&1], 0.1);
    }

    #[test]
    fn nested_map_accepts_empty_outer_and_inner() {
        let v: Nested = serde_json::from_str(r#"{"table":{}}"#).unwrap();
        assert!(v.table.is_empty());

        let v: Nested = serde_json::from_str(r#"{"table":{"silver":{}}}"#).unwrap();
        assert!(v.table["silver"].is_empty());
    }

    #[test]
    fn optional_map_reads_null_as_none() {
        let v: Optional = serde_json::from_str(r#"{"rates":null}"#).unwrap();
        assert!(v.rates.is_none());
    }

    #[test]
    fn optional_map_reads_empty_object_as_some_empty() {
        let v: Optional = serde_json::from_str(r#"{"rates":{}}"#).unwrap();
        assert_eq!(v.rates, Some(BTreeMap::new()));
    }

    /// Absent must stay `None`. A bare `Option<T>` already behaves that way, so
    /// losing it would be a silent breaking change to `InfinityBonusConfig`,
    /// which the schema does not mark required.
    #[test]
    fn optional_map_reads_absent_as_none() {
        let v: Optional = serde_json::from_str(r#"{}"#).unwrap();
        assert!(v.rates.is_none());
    }

    #[test]
    fn optional_map_parses_string_keys() {
        let v: Optional = serde_json::from_str(r#"{"rates":{"2":0.03}}"#).unwrap();
        assert_eq!(v.rates.unwrap()[&2], 0.03);
    }
}
