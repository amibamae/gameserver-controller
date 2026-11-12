//! Proxy integration fixtures. Rules: protocol/adapters/integrations.md
//! section 3.

use super::{current_at, fresh, object, same_present, schema_id, string, timestamp};
use crate::json::Json;
use crate::schemas::Schemas;

fn valid(schemas: &Schemas, version: &str, name: &str, value: Option<&Json>) -> bool {
    value.is_some_and(|value| schemas.valid(&schema_id(version, "integrations", name), value))
}

/// The backend target's instance belongs to the target's environment.
fn environment_consistent(target: Option<&Json>) -> bool {
    target.is_some_and(|target| {
        target.str("environment_id").is_some()
            && target.str("environment_id")
                == target
                    .get("instance_ref")
                    .and_then(|r| r.str("environment_id"))
    })
}

/// A readiness assertion bound to the exact server, environment and
/// instance of a target.
pub fn assertion_bound(
    assertion: &Json,
    server: Option<&str>,
    environment: Option<&str>,
    instance: Option<&Json>,
) -> bool {
    server.is_some()
        && assertion.str("server_id") == server
        && environment.is_some()
        && assertion.str("environment_id") == environment
        && same_present(assertion.get("instance_ref"), instance)
}

/// integration.proxy.register.accepted-is-not-routable.
pub fn register(input: &Json, schemas: &Schemas, version: &str) -> Result<Json, String> {
    let request = input.get("register_input");
    let response = input.get("register_output");
    let schemas_valid = valid(schemas, version, "proxy-backend-register-input", request)
        && valid(schemas, version, "proxy-backend-register-output", response);
    let target = request.and_then(|r| r.get("target"));
    let target_bound = same_present(target, response.and_then(|r| r.get("target")))
        && environment_consistent(target);
    let at = input.str("validation_time").and_then(timestamp);
    let readiness = request
        .and_then(|r| r.get("readiness_assertion"))
        .is_some_and(|assertion| {
            current_at(assertion, at)
                && assertion_bound(
                    assertion,
                    target.and_then(|t| t.str("server_id")),
                    target.and_then(|t| t.str("environment_id")),
                    target.and_then(|t| t.get("instance_ref")),
                )
        });
    let field = |name: &str| response.and_then(|r| r.str(name));
    let confirmed = schemas_valid
        && target_bound
        && field("request_outcome") == Some("confirmed")
        && field("registration_state") == Some("registered");
    let routing = confirmed && readiness && field("routability") == Some("routable");
    Ok(object(vec![
        ("schemas_valid", Json::Bool(schemas_valid)),
        ("target_bound", Json::Bool(target_bound)),
        ("readiness_current", Json::Bool(readiness)),
        ("registration_confirmed", Json::Bool(confirmed)),
        ("routing_permitted", Json::Bool(routing)),
        (
            "accepted_not_routable",
            Json::Bool(field("request_outcome") == Some("accepted") && !routing),
        ),
    ]))
}

/// integration.proxy.players.inspect.fresh-count-and-target-binding.
pub fn players_inspect(input: &Json, schemas: &Schemas, version: &str) -> Result<Json, String> {
    let mut evaluations = Vec::new();
    for case in input.items("evaluations") {
        let request = case.get("request");
        let response = case.get("response");
        let request_valid = valid(
            schemas,
            version,
            "proxy-backend-players-inspect-input",
            request,
        );
        let response_valid = valid(
            schemas,
            version,
            "proxy-backend-players-inspect-output",
            response,
        );
        let bound = same_present(
            request.and_then(|r| r.get("target")),
            response.and_then(|r| r.get("target")),
        );
        let consistent = environment_consistent(request.and_then(|r| r.get("target")))
            && environment_consistent(response.and_then(|r| r.get("target")));
        let state = response.and_then(|r| r.str("observation_state"));
        let at = case.str("evaluation_time").and_then(timestamp);
        let observed = response.and_then(|r| r.str("observed_at"));
        let time_valid = matches!((observed.and_then(timestamp), at), (Some(o), Some(t)) if o <= t);
        let count_fresh = state == Some("observed")
            && fresh(
                observed,
                at,
                request
                    .and_then(|r| r.get("maximum_age_seconds"))
                    .and_then(Json::as_f64),
            );
        let usable = request_valid
            && response_valid
            && bound
            && consistent
            && count_fresh
            && response
                .and_then(|r| r.get("active_session_count"))
                .is_some();
        evaluations.push(object(vec![
            (
                "case_id",
                string(case.str("case_id").ok_or("case without case_id")?),
            ),
            ("request_schema_valid", Json::Bool(request_valid)),
            ("response_schema_valid", Json::Bool(response_valid)),
            ("target_bound", Json::Bool(bound)),
            ("target_environment_consistent", Json::Bool(consistent)),
            ("observation_state", state.map(string).unwrap_or(Json::Null)),
            ("observation_time_valid", Json::Bool(time_valid)),
            ("count_fresh", Json::Bool(count_fresh)),
            ("count_usable", Json::Bool(usable)),
        ]));
    }
    Ok(object(vec![("evaluations", Json::Array(evaluations))]))
}
