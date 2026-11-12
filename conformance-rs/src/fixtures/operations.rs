use super::{object, schema_id, string};
use crate::json::Json;
use crate::schemas::Schemas;

fn final_state(operation: &Json) -> Option<&str> {
    operation
        .get("result")?
        .get("operation_data")?
        .str("final_lifecycle_state")
}

fn parameter(operation: &Json, name: &str) -> Option<bool> {
    operation
        .get("request_snapshot")?
        .get("parameters")?
        .bool(name)
}

fn parameters(operation: &Json) -> Json {
    operation
        .get("request_snapshot")
        .and_then(|snapshot| snapshot.get("parameters"))
        .cloned()
        .unwrap_or(Json::Null)
}

/// Final state a successful backup must end in, from the core-owned initial
/// state and the caller's resume_after_backup (omitted, false, or true).
/// Missing or unknown initial state has no permitted final state.
fn backup_final_state(initial: Option<&str>, resume: Option<bool>) -> Option<&'static str> {
    match (initial?, resume) {
        ("ready", None | Some(true)) => Some("ready"),
        ("ready", Some(false)) => Some("stopped"),
        // Degraded resumes only when explicitly requested.
        ("degraded", Some(true)) => Some("ready"),
        ("degraded", None | Some(false)) => Some("stopped"),
        ("stopped", _) => Some("stopped"),
        _ => None,
    }
}

fn policy_case(
    case: &Json,
    schemas: &Schemas,
    version: &str,
    parameters_schema: Option<&str>,
    expected_final: &dyn Fn(&Json) -> Option<&'static str>,
) -> Result<Json, String> {
    let id = case.str("case_id").ok_or("case without case_id")?;
    let operation = case.get("operation").ok_or("case without operation")?;
    let mut result = vec![("case_id", string(id))];
    if let Some(name) = parameters_schema {
        result.push((
            "parameters_schema_valid",
            Json::Bool(schemas.valid(
                &schema_id(version, "operations", name),
                &parameters(operation),
            )),
        ));
    }
    result.push((
        "operation_schema_valid",
        Json::Bool(schemas.valid(&schema_id(version, "operations", "operation"), operation)),
    ));
    let matches = operation.str("status") == Some("succeeded")
        && expected_final(operation).is_some_and(|wanted| final_state(operation) == Some(wanted));
    result.push(("final_state_matches_policy", Json::Bool(matches)));
    Ok(object(result))
}

fn policy_fixture(
    input: &Json,
    schemas: &Schemas,
    version: &str,
    parameters_schema: Option<&str>,
    expected_final: &dyn Fn(&Json) -> Option<&'static str>,
) -> Result<Json, String> {
    let evaluations = input
        .items("evaluations")
        .iter()
        .map(|case| policy_case(case, schemas, version, parameters_schema, expected_final))
        .collect::<Result<Vec<_>, _>>()?;
    Ok(object(vec![("evaluations", Json::Array(evaluations))]))
}

/// operations.restore.final-state-policy: a restore ends ready only when the
/// caller explicitly asked to start after restore.
pub fn restore_final_state(input: &Json, schemas: &Schemas, version: &str) -> Result<Json, String> {
    policy_fixture(
        input,
        schemas,
        version,
        Some("restore-parameters"),
        &|operation| {
            Some(
                if parameter(operation, "start_after_restore") == Some(true) {
                    "ready"
                } else {
                    "stopped"
                },
            )
        },
    )
}

/// operations.backup.no-resume-final-state: resume_after_backup false forbids
/// a ready result.
pub fn backup_no_resume(input: &Json, schemas: &Schemas, version: &str) -> Result<Json, String> {
    policy_fixture(
        input,
        schemas,
        version,
        Some("backup-parameters"),
        &|operation| {
            backup_final_state(
                operation.str("backup_initial_lifecycle_state"),
                parameter(operation, "resume_after_backup"),
            )
        },
    )
}

/// operations.backup.initial-state-resume-policy: the nine state/request
/// cells plus missing, unknown, and contradictory context.
pub fn backup_initial_state(
    input: &Json,
    schemas: &Schemas,
    version: &str,
) -> Result<Json, String> {
    policy_fixture(input, schemas, version, None, &|operation| {
        backup_final_state(
            operation.str("backup_initial_lifecycle_state"),
            parameter(operation, "resume_after_backup"),
        )
    })
}

/// operations.type-binding: the operation, its request snapshot, and any
/// result carry the same operation_type; partial failures and cancellations
/// are kept only when that holds and the record is schema-valid.
pub fn type_binding(input: &Json, schemas: &Schemas, version: &str) -> Result<Json, String> {
    let operation_schema = schema_id(version, "operations", "operation");
    let mut evaluations = Vec::new();
    let (mut all_schemas, mut all_snapshots, mut all_results, mut all_bindings) =
        (true, true, true, true);
    let (mut snapshot_mismatch, mut result_mismatch, mut missing_result_type) =
        (false, false, false);
    let (mut failure_kept, mut cancellation_kept, mut without_data_kept) = (false, false, false);
    for operation in input.items("operations") {
        let id = operation
            .str("operation_id")
            .ok_or("operation without operation_id")?;
        let kind = operation.str("operation_type");
        let schema_valid = schemas.valid(&operation_schema, operation);
        let snapshot_matches = operation
            .get("request_snapshot")
            .and_then(|snapshot| snapshot.str("operation_type"))
            == kind;
        let result = operation
            .get("result")
            .filter(|value| **value != Json::Null);
        let result_type = result.and_then(|value| value.str("operation_type"));
        let result_matches = match result {
            None => true,
            Some(_) => result_type.is_some() && result_type == kind,
        };
        let bindings_match = snapshot_matches && result_matches;
        let partial = result.and_then(|value| value.bool("partial")) == Some(true);
        let status = operation.str("status").unwrap_or_default();
        let accepted = schema_valid
            && bindings_match
            && partial
            && (status == "failed" || status == "cancelled");

        all_schemas &= schema_valid;
        all_snapshots &= snapshot_matches;
        all_results &= result_matches;
        all_bindings &= bindings_match;
        snapshot_mismatch |= !snapshot_matches;
        result_mismatch |= result_type.is_some() && result_type != kind;
        missing_result_type |= result.is_some() && result_type.is_none();
        failure_kept |= accepted && status == "failed";
        cancellation_kept |= accepted && status == "cancelled";
        without_data_kept |= accepted
            && result
                .and_then(|value| value.get("operation_data"))
                .is_none();
        evaluations.push(object(vec![
            ("operation_id", string(id)),
            ("schema_valid", Json::Bool(schema_valid)),
            ("type_bindings_match", Json::Bool(bindings_match)),
            ("partial_result_accepted", Json::Bool(accepted)),
        ]));
    }
    Ok(object(vec![
        ("schemas_valid", Json::Bool(all_schemas)),
        ("request_snapshot_types_match", Json::Bool(all_snapshots)),
        ("result_types_match", Json::Bool(all_results)),
        ("snapshot_mismatch_detected", Json::Bool(snapshot_mismatch)),
        ("result_mismatch_detected", Json::Bool(result_mismatch)),
        ("matching_partial_failure_kept", Json::Bool(failure_kept)),
        (
            "matching_partial_cancellation_kept",
            Json::Bool(cancellation_kept),
        ),
        (
            "all_operation_type_bindings_match",
            Json::Bool(all_bindings),
        ),
        (
            "missing_result_type_detected",
            Json::Bool(missing_result_type),
        ),
        (
            "matching_partial_without_operation_data_kept",
            Json::Bool(without_data_kept),
        ),
        ("evaluations", Json::Array(evaluations)),
    ]))
}
