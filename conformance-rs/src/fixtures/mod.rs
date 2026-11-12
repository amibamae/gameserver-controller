//! Fixture evaluators. Each one computes its own result from a fixture's
//! `input`, independently of the Go evaluator, and the runner compares that
//! result with the fixture's published `expected` object.

mod network;
mod operations;
mod wire;

use crate::json::Json;
use crate::schemas::Schemas;

/// Evaluates one fixture, or returns None when this consumer has no
/// evaluator for its ID.
pub fn evaluate(fixture: &Json, schemas: &Schemas) -> Option<Result<Json, String>> {
    let version = fixture.str("protocol_version").unwrap_or_default();
    let input = fixture.get("input")?;
    Some(match fixture.str("fixture_id")? {
        "protocol.json.raw-wire-interoperability" => wire::raw_wire(input),
        "operations.type-binding" => operations::type_binding(input, schemas, version),
        "operations.restore.final-state-policy" => {
            operations::restore_final_state(input, schemas, version)
        }
        "operations.backup.no-resume-final-state" => {
            operations::backup_no_resume(input, schemas, version)
        }
        "operations.backup.initial-state-resume-policy" => {
            operations::backup_initial_state(input, schemas, version)
        }
        "integration.network-profile.requirements" => {
            network::requirements(input, schemas, version)
        }
        _ => return None,
    })
}

/// Builds an object from (name, value) pairs.
pub fn object(members: Vec<(&str, Json)>) -> Json {
    Json::Object(
        members
            .into_iter()
            .map(|(name, value)| (name.to_string(), value))
            .collect(),
    )
}

pub fn string(value: &str) -> Json {
    Json::String(value.to_string())
}

pub fn schema_id(version: &str, area: &str, name: &str) -> String {
    format!("urn:gameserver-controller:{version}:{area}:{name}")
}
