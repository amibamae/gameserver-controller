//! Velocity player-transfer fixtures. Rules: protocol/adapters/integrations.md
//! section 4 and the privacy rules in protocol/security.md.

use super::proxy::assertion_bound;
use super::{
    contains_probe, current_at, fresh, object, same_present, schema_id, string, strings, timestamp,
};
use crate::json::Json;
use crate::schemas::Schemas;

const OUTCOMES: [&str; 6] = [
    "transferred",
    "already_at_destination",
    "disconnected",
    "rejected",
    "failed",
    "outcome_unknown",
];

fn valid(schemas: &Schemas, version: &str, name: &str, value: Option<&Json>) -> bool {
    value.is_some_and(|value| schemas.valid(&schema_id(version, "integrations", name), value))
}

/// The behavioral transfer checks beyond JSON Schema: both messages valid,
/// the result bound to the request, a current bound readiness assertion,
/// exactly one outcome per requested session, and consistent counts.
fn transfer_valid_and_bound(
    request: &Json,
    result: &Json,
    at: Option<i128>,
    schemas: &Schemas,
    version: &str,
) -> bool {
    if !valid(
        schemas,
        version,
        "velocity-player-transfer-request",
        Some(request),
    ) || !valid(
        schemas,
        version,
        "velocity-player-transfer-result",
        Some(result),
    ) {
        return false;
    }
    let same_field =
        |name: &str| request.str(name).is_some() && request.str(name) == result.str(name);
    let bound = same_field("waiting_cohort_id")
        && same_field("source_backend_id")
        && same_field("destination_backend_id")
        && same_present(
            request.get("expected_instance_ref"),
            result.get("target_instance_ref"),
        );
    let readiness = request.get("readiness_assertion").is_some_and(|assertion| {
        current_at(assertion, at)
            && assertion_bound(
                assertion,
                request.str("server_id"),
                request.str("environment_id"),
                request.get("expected_instance_ref"),
            )
    });
    let requested = strings(request, "player_session_ids");
    let results = result.items("results");
    let mut seen: Vec<&str> = Vec::new();
    for entry in results {
        match entry.str("proxy_session_id") {
            Some(id) if !seen.contains(&id) && requested.contains(&id) => seen.push(id),
            _ => return false,
        }
    }
    let exact_sessions = seen.len() == requested.len();
    let count_matches = result
        .get("requested_count")
        .and_then(Json::as_f64)
        .is_some_and(|count| count == requested.len() as f64);
    let tallies = OUTCOMES.iter().all(|outcome| {
        let actual = results
            .iter()
            .filter(|r| r.str("outcome") == Some(outcome))
            .count();
        result
            .get("outcome_counts")
            .and_then(|counts| counts.get(outcome))
            .and_then(Json::as_f64)
            .is_some_and(|reported| reported == actual as f64)
    });
    bound && readiness && exact_sessions && count_matches && tallies
}

/// velocity-transfer-outcome-unknown-reconciliation: each outcome_unknown
/// session is reconciled through a fresh, exact session inspection.
pub fn reconciliation(input: &Json, schemas: &Schemas, version: &str) -> Result<Json, String> {
    let request = input
        .get("transfer_request")
        .ok_or("missing transfer_request")?;
    let result = input
        .get("transfer_result")
        .ok_or("missing transfer_result")?;
    let at = input.str("transfer_validation_time").and_then(timestamp);
    let transfer_ok = transfer_valid_and_bound(request, result, at, schemas, version);
    let unknown_sessions: Vec<&str> = result
        .items("results")
        .iter()
        .filter(|r| r.str("outcome") == Some("outcome_unknown"))
        .filter_map(|r| r.str("proxy_session_id"))
        .collect();
    let mut cases = Vec::new();
    for case in input.items("reconciliation_cases") {
        let inspection_request = case.get("inspection_request");
        let inspection = case.get("inspection_result");
        let decided = case.str("decision_time").and_then(timestamp);
        let schema_valid = valid(
            schemas,
            version,
            "proxy-session-inspect-input",
            inspection_request,
        ) && valid(schemas, version, "proxy-session-inspect-output", inspection);
        let session = inspection_request.and_then(|r| r.str("proxy_session_id"));
        let proxy_ref = inspection_request.and_then(|r| r.get("proxy_instance_ref"));
        let bound = transfer_ok
            && session.is_some_and(|id| unknown_sessions.contains(&id))
            && session == inspection.and_then(|r| r.str("proxy_session_id"))
            && same_present(
                proxy_ref,
                inspection.and_then(|r| r.get("proxy_instance_ref")),
            )
            && request.str("environment_id").is_some()
            && proxy_ref.and_then(|r| r.str("environment_id")) == request.str("environment_id");
        let is_fresh = fresh(
            inspection.and_then(|r| r.str("observed_at")),
            decided,
            inspection_request
                .and_then(|r| r.get("maximum_observation_age_seconds"))
                .and_then(Json::as_f64),
        );
        let readiness = case
            .get("retry_readiness_assertion")
            .is_some_and(|assertion| {
                current_at(assertion, decided)
                    && assertion_bound(
                        assertion,
                        request.str("server_id"),
                        request.str("environment_id"),
                        request.get("expected_instance_ref"),
                    )
            });
        let backend = inspection.and_then(|r| r.str("current_backend_id"));
        let state = if !(schema_valid && bound && is_fresh) {
            "unresolved"
        } else {
            match inspection.and_then(|r| r.str("connection_state")) {
                Some("connected")
                    if backend.is_some() && backend == request.str("source_backend_id") =>
                {
                    "still_on_source"
                }
                Some("connected")
                    if backend.is_some() && backend == request.str("destination_backend_id") =>
                {
                    "already_at_destination"
                }
                Some("connected") => "other_backend",
                Some("disconnected") => "disconnected",
                _ => "unknown",
            }
        };
        cases.push(object(vec![
            (
                "case_id",
                string(case.str("case_id").ok_or("case without case_id")?),
            ),
            ("session_inspection_schema_valid", Json::Bool(schema_valid)),
            ("session_inspection_bound", Json::Bool(bound)),
            ("session_inspection_fresh", Json::Bool(is_fresh)),
            ("retry_readiness_current", Json::Bool(readiness)),
            ("reconciliation_state", string(state)),
            (
                "retry_permitted",
                Json::Bool(state == "still_on_source" && readiness),
            ),
        ]));
    }
    Ok(object(vec![
        ("transfer_result_valid_and_bound", Json::Bool(transfer_ok)),
        ("reconciliation_cases", Json::Array(cases)),
    ]))
}

/// velocity-transfer-privacy-canaries: player-data probes never appear in
/// the request, result or logs, and session IDs are redacted from logs. A
/// case may replace the default request or result.
pub fn privacy(input: &Json, schemas: &Schemas, version: &str) -> Result<Json, String> {
    let privacy_probes: Vec<&str> = strings(input, "privacy_probes")
        .into_iter()
        .filter(|p| !p.is_empty())
        .collect();
    let at = input.str("validation_time").and_then(timestamp);
    let mut evaluations = Vec::new();
    for case in input.items("evaluations") {
        let request = case
            .get("request")
            .or(input.get("transfer_request"))
            .ok_or("missing request")?;
        let result = case
            .get("result")
            .or(input.get("transfer_result"))
            .ok_or("missing result")?;
        let logs = Json::Array(case.items("diagnostic_log_lines").to_vec());
        let mut session_probes: Vec<&str> = strings(input, "log_redaction_probes");
        session_probes.extend(strings(request, "player_session_ids"));
        session_probes.retain(|probe| !probe.is_empty());
        let in_request = contains_probe(request, &privacy_probes);
        let in_result = contains_probe(result, &privacy_probes);
        let in_logs = contains_probe(&logs, &privacy_probes);
        let session_in_logs = contains_probe(&logs, &session_probes);
        evaluations.push(object(vec![
            (
                "case_id",
                string(case.str("case_id").ok_or("case without case_id")?),
            ),
            (
                "request_schema_valid",
                Json::Bool(valid(
                    schemas,
                    version,
                    "velocity-player-transfer-request",
                    Some(request),
                )),
            ),
            (
                "result_schema_valid",
                Json::Bool(valid(
                    schemas,
                    version,
                    "velocity-player-transfer-result",
                    Some(result),
                )),
            ),
            (
                "transfer_result_valid_and_bound",
                Json::Bool(transfer_valid_and_bound(
                    request, result, at, schemas, version,
                )),
            ),
            ("request_contains_privacy_probe", Json::Bool(in_request)),
            ("result_contains_privacy_probe", Json::Bool(in_result)),
            ("logs_contain_privacy_probe", Json::Bool(in_logs)),
            ("logs_contain_session_id", Json::Bool(session_in_logs)),
            (
                "privacy_compliant",
                Json::Bool(!(in_request || in_result || in_logs || session_in_logs)),
            ),
        ]));
    }
    Ok(object(vec![("evaluations", Json::Array(evaluations))]))
}
