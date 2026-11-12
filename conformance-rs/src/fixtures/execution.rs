//! Execution adapter fixtures. Rules: protocol/adapters/execution.md and
//! protocol/conformance/requirements.md (systemd lifecycle, logs, health).

use super::{contains_probe, object, same_present, schema_id, string, strings};
use crate::json::Json;
use crate::schemas::Schemas;

fn valid(schemas: &Schemas, version: &str, name: &str, value: Option<&Json>) -> bool {
    value.is_some_and(|value| schemas.valid(&schema_id(version, "execution", name), value))
}

fn unit_bound(request: Option<&Json>, response: Option<&Json>) -> bool {
    same_present(
        request.and_then(|r| r.get("unit_ref")),
        response.and_then(|r| r.get("unit_ref")),
    )
}

fn case_id(case: &Json) -> Result<Json, String> {
    Ok(string(case.str("case_id").ok_or("case without case_id")?))
}

/// The probes a case supplies, ignoring empty strings.
fn probes(case: &Json) -> Vec<&str> {
    strings(case, "redaction_probes")
        .into_iter()
        .filter(|probe| !probe.is_empty())
        .collect()
}

/// execution.unit.start.accepted-not-ready: an execution result never
/// establishes game readiness, whatever its outcome.
pub fn start(input: &Json, schemas: &Schemas, version: &str) -> Result<Json, String> {
    let request = input.get("execution_input");
    let response = input.get("execution_output");
    Ok(object(vec![
        (
            "schemas_valid",
            Json::Bool(
                valid(schemas, version, "systemd-unit-start-input", request)
                    && valid(schemas, version, "systemd-unit-start-output", response),
            ),
        ),
        ("unit_bound", Json::Bool(unit_bound(request, response))),
        (
            "outcome",
            response
                .and_then(|r| r.get("outcome"))
                .cloned()
                .unwrap_or(Json::Null),
        ),
        ("readiness_established", Json::Bool(false)),
        (
            "external_operation_ref_present",
            Json::Bool(
                response
                    .and_then(|r| r.get("external_operation_ref"))
                    .is_some(),
            ),
        ),
    ]))
}

/// execution.unit.graceful-stop.unknown-requires-inspection.
pub fn graceful_stop(input: &Json, schemas: &Schemas, version: &str) -> Result<Json, String> {
    let request = input.get("execution_input");
    let response = input.get("execution_output");
    let schemas_valid = valid(
        schemas,
        version,
        "systemd-unit-graceful-stop-input",
        request,
    ) && valid(
        schemas,
        version,
        "systemd-unit-graceful-stop-output",
        response,
    );
    let bound = unit_bound(request, response);
    let outcome = response.and_then(|r| r.str("outcome"));
    let state = response.and_then(|r| r.str("state"));
    let operation_ref = response.and_then(|r| r.get("external_operation_ref"));
    let unit_environment = response
        .and_then(|r| r.get("unit_ref"))
        .and_then(|u| u.str("environment_id"));
    let terminated =
        schemas_valid && bound && outcome == Some("completed") && state == Some("stopped");
    // Anything short of a confirmed stop of the exact unit leaves the unit's
    // state unknown, so it must be inspected before retry or dependent work.
    let inspect = !terminated;
    Ok(object(vec![
        ("schemas_valid", Json::Bool(schemas_valid)),
        ("unit_bound", Json::Bool(bound)),
        ("outcome", outcome.map(string).unwrap_or(Json::Null)),
        ("state", state.map(string).unwrap_or(Json::Null)),
        (
            "external_operation_ref_present",
            Json::Bool(operation_ref.is_some()),
        ),
        (
            "operation_ref_environment_matches",
            Json::Bool(
                unit_environment.is_some()
                    && operation_ref.and_then(|r| r.str("environment_id")) == unit_environment,
            ),
        ),
        ("readiness_established", Json::Bool(false)),
        ("reconciliation_required", Json::Bool(inspect)),
        ("result_requires_inspection", Json::Bool(inspect)),
        ("retry_without_inspection", Json::Bool(!inspect)),
        // A stop result is never evidence that the unit started.
        ("successful_start_claim_accepted", Json::Bool(false)),
        ("termination_confirmed", Json::Bool(terminated)),
    ]))
}

/// execution.unit.inspect.exact-target-state: only a bound, conclusive
/// observation may be treated as running or stopped.
pub fn inspect(input: &Json, schemas: &Schemas, version: &str) -> Result<Json, String> {
    let mut evaluations = Vec::new();
    for case in input.items("evaluations") {
        let request = case.get("request");
        let response = case.get("response");
        let bound = unit_bound(request, response);
        let state = response.and_then(|r| r.str("state"));
        let conclusive = bound && matches!(state, Some("running" | "stopped"));
        evaluations.push(object(vec![
            ("case_id", case_id(case)?),
            (
                "request_schema_valid",
                Json::Bool(valid(
                    schemas,
                    version,
                    "systemd-unit-inspect-input",
                    request,
                )),
            ),
            (
                "response_schema_valid",
                Json::Bool(valid(
                    schemas,
                    version,
                    "systemd-unit-inspect-output",
                    response,
                )),
            ),
            ("unit_bound", Json::Bool(bound)),
            ("observation_conclusive", Json::Bool(conclusive)),
            (
                "observation_unknown",
                Json::Bool(bound && state == Some("unknown")),
            ),
            (
                "safe_to_treat_as_running",
                Json::Bool(conclusive && state == Some("running")),
            ),
            (
                "safe_to_treat_as_stopped",
                Json::Bool(conclusive && state == Some("stopped")),
            ),
        ]));
    }
    Ok(object(vec![("evaluations", Json::Array(evaluations))]))
}

/// execution.unit.logs.byte-bound-and-redaction: UTF-8 line bytes plus one
/// newline between adjacent lines count toward max_bytes, and the redaction
/// flag alone does not prove that probes are absent.
pub fn logs(input: &Json, schemas: &Schemas, version: &str) -> Result<Json, String> {
    let mut evaluations = Vec::new();
    for case in input.items("evaluations") {
        let request = case.get("request");
        let response = case.get("response");
        let schemas_valid = valid(schemas, version, "systemd-unit-logs-read-input", request)
            && valid(schemas, version, "systemd-unit-logs-read-output", response);
        let bound = unit_bound(request, response);
        let lines = response.map(|r| strings(r, "lines")).unwrap_or_default();
        let total =
            lines.iter().map(|line| line.len()).sum::<usize>() + lines.len().saturating_sub(1);
        let within = request
            .and_then(|r| r.get("max_bytes"))
            .and_then(Json::as_f64)
            .is_some_and(|limit| total as f64 <= limit);
        let absent = !response.is_some_and(|r| contains_probe(r, &probes(case)));
        evaluations.push(object(vec![
            ("case_id", case_id(case)?),
            ("schemas_valid", Json::Bool(schemas_valid)),
            ("unit_bound", Json::Bool(bound)),
            ("total_bytes", Json::Number(total.to_string())),
            ("logs_within_bound", Json::Bool(within)),
            ("redaction_probes_absent", Json::Bool(absent)),
            (
                "response_accepted",
                Json::Bool(schemas_valid && bound && within && absent),
            ),
        ]));
    }
    Ok(object(vec![("evaluations", Json::Array(evaluations))]))
}

/// execution.unit.health.diagnostic-redaction-and-binding: unknown health is
/// accepted as unknown and never promoted to healthy.
pub fn health(input: &Json, schemas: &Schemas, version: &str) -> Result<Json, String> {
    let mut evaluations = Vec::new();
    for case in input.items("evaluations") {
        let request = case.get("request");
        let response = case.get("response");
        let schemas_valid = valid(
            schemas,
            version,
            "systemd-unit-health-inspect-input",
            request,
        ) && valid(
            schemas,
            version,
            "systemd-unit-health-inspect-output",
            response,
        );
        let bound = unit_bound(request, response);
        let absent = !response.is_some_and(|r| contains_probe(r, &probes(case)));
        let accepted = schemas_valid && bound && absent;
        let healthy = accepted && response.and_then(|r| r.str("health")) == Some("healthy");
        evaluations.push(object(vec![
            ("case_id", case_id(case)?),
            ("schemas_valid", Json::Bool(schemas_valid)),
            ("unit_bound", Json::Bool(bound)),
            ("redaction_probes_absent", Json::Bool(absent)),
            ("response_accepted", Json::Bool(accepted)),
            ("healthy_confirmed", Json::Bool(healthy)),
        ]));
    }
    Ok(object(vec![("evaluations", Json::Array(evaluations))]))
}
