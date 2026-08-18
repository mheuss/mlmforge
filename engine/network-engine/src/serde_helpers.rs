//! Serde helpers shared by the plan config types and the worker's request
//! params.

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
/// Do **not** reach for Go's `omitempty` on a required field. It makes a
/// dropped field indistinguishable from an empty one, and on a money path that
/// pays zero instead of complaining.
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
