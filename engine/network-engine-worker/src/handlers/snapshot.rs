use network_engine::board_plan::BoardPlanEngine;
use network_engine::streamline::StreamlineEngine;
use network_engine::tree::binary::BinaryTree;
use network_engine::tree::matrix::MatrixTree;
use network_engine::tree::unilevel::UnilevelTree;

use super::common::{parse_params, parse_params_as};
use crate::protocol::{Request, Response};
use crate::state::{TreeInstance, WorkerState};

/// A snapshot payload: the tree's own JSON under a tag naming its type.
///
/// `data` is already serialized, so the tree does not become a
/// `serde_json::Value` on the way out. That also rules out `json!`, which can
/// only nest a `Value`.
#[derive(serde::Serialize)]
struct Snapshot<'a> {
    tree_type: &'a str,
    data: Box<serde_json::value::RawValue>,
}

/// Serializes a tree or board plan engine for snapshot persistence.
///
/// Params: structure.
///
/// Do not replace the `SERIALIZATION_ERROR` arm with an `expect`.
pub(crate) fn handle_take_snapshot(state: &WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match params.get("structure").and_then(|v| v.as_str()) {
        Some(s) => s,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing structure name",
            );
        }
    };

    let tree = match state.trees.get(structure) {
        Some(t) => t,
        None => {
            return Response::error(
                request.id.clone(),
                "STRUCTURE_NOT_FOUND",
                format!("structure '{}' not found", structure),
            );
        }
    };

    let snapshot = match tree {
        TreeInstance::Unilevel(t) => serde_json::value::to_raw_value(t),
        TreeInstance::Binary(t) => serde_json::value::to_raw_value(t),
        TreeInstance::Matrix(t) => serde_json::value::to_raw_value(t),
        TreeInstance::BoardPlan(e) => serde_json::value::to_raw_value(e),
        TreeInstance::Streamline(e) => serde_json::value::to_raw_value(e),
    };

    match snapshot {
        Ok(data) => {
            let tree_type = match tree {
                TreeInstance::Unilevel(_) => "unilevel",
                TreeInstance::Binary(_) => "binary",
                TreeInstance::Matrix(_) => "matrix",
                TreeInstance::BoardPlan(_) => "board_plan",
                TreeInstance::Streamline(_) => "streamline",
            };
            Response::success(request.id.clone(), Snapshot { tree_type, data })
        }
        Err(e) => Response::error(
            request.id.clone(),
            "SERIALIZATION_ERROR",
            format!("failed to serialize snapshot: {}", e),
        ),
    }
}

/// Appended to a snapshot parse failure so the reported position is not read as
/// an offset into the request line.
const POSITION_NOTE: &str = "(the position counts into the snapshot payload, not the request line)";

/// Top-level fields of a unilevel snapshot's data.
const UNILEVEL_FIELDS: &[&str] = &["arena"];

/// Top-level fields of a binary snapshot's data.
const BINARY_FIELDS: &[&str] = &["arena", "slots"];

/// Top-level fields of a matrix snapshot's data.
const MATRIX_FIELDS: &[&str] = &["arena", "holding_tank", "slots", "spillover", "width"];

/// Returns the top-level fields a `tree_type` snapshot carries, or None outside unilevel, binary and matrix.
fn tree_fields(tree_type: &str) -> Option<&'static [&'static str]> {
    match tree_type {
        "unilevel" => Some(UNILEVEL_FIELDS),
        "binary" => Some(BINARY_FIELDS),
        "matrix" => Some(MATRIX_FIELDS),
        _ => None,
    }
}

/// Names the fields in `data` that another tree type's snapshot carries and a `tree_type` snapshot does not.
fn foreign_tree_fields(tree_type: &str, data: &serde_json::value::RawValue) -> Vec<&'static str> {
    let Some(own) = tree_fields(tree_type) else {
        return Vec::new();
    };
    let Ok(keys) = serde_json::from_str::<std::collections::BTreeMap<String, serde::de::IgnoredAny>>(
        data.get(),
    ) else {
        return Vec::new();
    };
    let mut foreign: Vec<&'static str> = [UNILEVEL_FIELDS, BINARY_FIELDS, MATRIX_FIELDS]
        .concat()
        .into_iter()
        .filter(|field| !own.contains(field) && keys.contains_key(*field))
        .collect();
    foreign.sort_unstable();
    foreign.dedup();
    foreign
}

/// Restores a structure from a snapshot under a name not already in use.
///
/// Params: structure, tree_type, data.
pub(crate) fn handle_restore_snapshot(state: &mut WorkerState, request: &Request) -> Response {
    #[derive(serde::Deserialize)]
    struct Params {
        structure: String,
        tree_type: String,
        data: Box<serde_json::value::RawValue>,
    }

    let params: Params = match parse_params_as(&request.params) {
        Ok(p) => p,
        Err(e) => {
            return Response::error(
                request.id.clone(),
                "INVALID_PARAMS",
                format!("{e} (the position counts into the params value)"),
            );
        }
    };

    if state.trees.contains_key(&params.structure) {
        return Response::error(
            request.id.clone(),
            "TREE_EXISTS",
            format!(
                "structure '{}' already exists; drop it with drop_structure before restoring",
                params.structure
            ),
        );
    }

    let foreign = foreign_tree_fields(&params.tree_type, &params.data);
    if !foreign.is_empty() {
        return Response::error(
            request.id.clone(),
            "INVALID_PARAMS",
            format!(
                "snapshot data carries fields a {} snapshot does not have: {}",
                params.tree_type,
                foreign.join(", ")
            ),
        );
    }

    let instance = match params.tree_type.as_str() {
        "unilevel" => match serde_json::from_str::<UnilevelTree>(params.data.get()) {
            Ok(t) => match t.validate_restored() {
                Ok(()) => TreeInstance::Unilevel(t),
                Err(err) => {
                    return Response::error(
                        request.id.clone(),
                        "INCONSISTENT_SNAPSHOT",
                        err.to_string(),
                    );
                }
            },
            Err(e) => {
                return Response::error(
                    request.id.clone(),
                    "INVALID_PARAMS",
                    format!("failed to deserialize unilevel snapshot: {e} {POSITION_NOTE}"),
                );
            }
        },
        "binary" => match serde_json::from_str::<BinaryTree>(params.data.get()) {
            Ok(t) => match t.validate_restored() {
                Ok(()) => TreeInstance::Binary(t),
                Err(err) => {
                    return Response::error(
                        request.id.clone(),
                        "INCONSISTENT_SNAPSHOT",
                        err.to_string(),
                    );
                }
            },
            Err(e) => {
                return Response::error(
                    request.id.clone(),
                    "INVALID_PARAMS",
                    format!("failed to deserialize binary snapshot: {e} {POSITION_NOTE}"),
                );
            }
        },
        "matrix" => match serde_json::from_str::<MatrixTree>(params.data.get()) {
            Ok(t) => match t.validate_restored() {
                Ok(()) => TreeInstance::Matrix(t),
                Err(err) => {
                    return Response::error(
                        request.id.clone(),
                        "INCONSISTENT_SNAPSHOT",
                        err.to_string(),
                    );
                }
            },
            Err(e) => {
                return Response::error(
                    request.id.clone(),
                    "INVALID_PARAMS",
                    format!("failed to deserialize matrix snapshot: {e} {POSITION_NOTE}"),
                );
            }
        },
        "board_plan" => match serde_json::from_str::<BoardPlanEngine>(params.data.get()) {
            Ok(e) => match e.validate_restored() {
                Ok(()) => TreeInstance::BoardPlan(e),
                Err(err) => {
                    return Response::error(
                        request.id.clone(),
                        "INCONSISTENT_SNAPSHOT",
                        err.to_string(),
                    );
                }
            },
            Err(e) => {
                return Response::error(
                    request.id.clone(),
                    "INVALID_PARAMS",
                    format!("failed to deserialize board plan snapshot: {e} {POSITION_NOTE}"),
                );
            }
        },
        "streamline" => match serde_json::from_str::<StreamlineEngine>(params.data.get()) {
            Ok(e) => match e.validate_restored() {
                Ok(()) => TreeInstance::Streamline(e),
                Err(err) => {
                    return Response::error(
                        request.id.clone(),
                        "INCONSISTENT_SNAPSHOT",
                        err.to_string(),
                    );
                }
            },
            Err(e) => {
                return Response::error(
                    request.id.clone(),
                    "INVALID_PARAMS",
                    format!("failed to deserialize streamline snapshot: {e} {POSITION_NOTE}"),
                );
            }
        },
        other => {
            return Response::error(
                request.id.clone(),
                "INVALID_PARAMS",
                format!("unknown tree_type: {}", other),
            );
        }
    };

    state.trees.insert(params.structure, instance);
    Response::success(request.id.clone(), serde_json::json!({"restored": true}))
}

#[cfg(test)]
mod tests {
    use super::*;
    use network_engine::config::matrix::SpilloverDirection;

    /// Returns the sorted top-level keys of `tree`'s serialized form.
    fn top_level_keys(tree: &impl serde::Serialize) -> Vec<String> {
        let value = serde_json::to_value(tree).expect("a tree serializes to JSON");
        let mut keys: Vec<String> = value
            .as_object()
            .expect("a tree serializes as a JSON object")
            .keys()
            .cloned()
            .collect();
        keys.sort();
        keys
    }

    /// Returns `fields` as sorted owned strings.
    fn sorted(fields: &[&str]) -> Vec<String> {
        let mut out: Vec<String> = fields.iter().map(|f| f.to_string()).collect();
        out.sort();
        out
    }

    #[test]
    fn tree_field_lists_match_each_tree_types_snapshot() {
        assert_eq!(
            top_level_keys(&UnilevelTree::new()),
            sorted(UNILEVEL_FIELDS),
            "unilevel"
        );
        assert_eq!(
            top_level_keys(&BinaryTree::new()),
            sorted(BINARY_FIELDS),
            "binary"
        );
        let matrix = MatrixTree::new(2, SpilloverDirection::BreadthFirst)
            .expect("a width-2 breadth-first matrix builds");
        assert_eq!(top_level_keys(&matrix), sorted(MATRIX_FIELDS), "matrix");
    }
}
