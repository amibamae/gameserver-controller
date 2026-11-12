//! Game adapter fixtures. Rules: protocol/adapters/games.md sections 3-4 and
//! protocol/conformance/requirements.md.

use super::{object, same_present, schema_id, string};
use crate::jcs;
use crate::json::Json;
use crate::schemas::Schemas;

fn game_schema(schemas: &Schemas, version: &str, name: &str, value: Option<&Json>) -> bool {
    value.is_some_and(|value| schemas.valid(&schema_id(version, "game", name), value))
}

fn case_id(case: &Json) -> Result<&str, String> {
    case.str("case_id")
        .ok_or_else(|| "case without case_id".to_string())
}

/// A declared checksum is SHA-256 with a lowercase hex digest.
fn sha256_checksum(checksum: Option<&Json>) -> bool {
    checksum.is_some_and(|checksum| {
        checksum.str("algorithm") == Some("sha256")
            && checksum.str("digest").is_some_and(|digest| {
                digest.len() == 64
                    && digest
                        .bytes()
                        .all(|b| matches!(b, b'0'..=b'9' | b'a'..=b'f'))
            })
    })
}

fn unique<'a>(ids: impl Iterator<Item = Option<&'a str>>) -> bool {
    let mut seen = Vec::new();
    for id in ids {
        match id {
            Some(id) if !seen.contains(&id) => seen.push(id),
            _ => return false,
        }
    }
    true
}

/// game.release.describe.integrity-and-identity: the exact requested
/// release, unique artifact IDs, and SHA-256 artifact checksums.
pub fn release_describe(input: &Json, schemas: &Schemas, version: &str) -> Result<Json, String> {
    let mut evaluations = Vec::new();
    for case in input.items("evaluations") {
        let request = case.get("request");
        let response = case.get("response");
        let request_valid = game_schema(schemas, version, "release-describe-input", request);
        let response_valid = game_schema(schemas, version, "release-describe-output", response);
        let bound = request.and_then(|r| r.str("release_id")).is_some()
            && request.and_then(|r| r.str("release_id"))
                == response.and_then(|r| r.str("release_id"));
        let artifacts = response.map(|r| r.items("artifacts")).unwrap_or_default();
        let ids_unique = unique(artifacts.iter().map(|a| a.str("artifact_id")));
        let checksums_valid = artifacts.iter().all(|a| sha256_checksum(a.get("checksum")));
        evaluations.push(object(vec![
            ("case_id", string(case_id(case)?)),
            ("request_schema_valid", Json::Bool(request_valid)),
            ("response_schema_valid", Json::Bool(response_valid)),
            ("release_id_bound", Json::Bool(bound)),
            ("artifact_ids_unique", Json::Bool(ids_unique)),
            ("artifact_checksums_valid", Json::Bool(checksums_valid)),
            (
                "release_description_accepted",
                Json::Bool(
                    request_valid && response_valid && bound && ids_unique && checksums_valid,
                ),
            ),
        ]));
    }
    Ok(object(vec![("evaluations", Json::Array(evaluations))]))
}

/// game.install.release-binding-and-reconciliation.
pub fn install(input: &Json, schemas: &Schemas, version: &str) -> Result<Json, String> {
    let release = input.get("release_description");
    let release_valid = game_schema(schemas, version, "release-describe-output", release);
    let pinned = release.map(|r| r.items("artifacts")).unwrap_or_default();
    let mut evaluations = Vec::new();
    for case in input.items("evaluations") {
        let request = case.get("request");
        let response = case.get("response");
        let request_valid = game_schema(schemas, version, "install-input", request);
        let response_valid = game_schema(schemas, version, "install-output", response);
        let target_bound = same_present(
            request.and_then(|r| r.get("target")),
            response.and_then(|r| r.get("target")),
        );
        let requested_release = request.and_then(|r| r.str("release_id"));
        let release_bound = requested_release.is_some()
            && requested_release == response.and_then(|r| r.str("release_id"))
            && requested_release == release.and_then(|r| r.str("release_id"));
        let results = response
            .map(|r| r.items("artifact_results"))
            .unwrap_or_default();
        // Exactly one result per pinned artifact, and nothing unpinned.
        let complete = results.len() == pinned.len()
            && unique(results.iter().map(|r| r.str("artifact_id")))
            && pinned.iter().all(|artifact| {
                results.iter().any(|r| {
                    r.str("artifact_id").is_some()
                        && r.str("artifact_id") == artifact.str("artifact_id")
                })
            });
        // Verified only with the pinned checksum observed exactly.
        let verified = complete
            && results.iter().all(|result| {
                let pin = pinned
                    .iter()
                    .find(|a| a.str("artifact_id") == result.str("artifact_id"))
                    .and_then(|a| a.get("checksum"));
                result.str("result") == Some("verified")
                    && same_present(pin, result.get("expected_checksum"))
                    && same_present(pin, result.get("observed_checksum"))
            });
        let outcome = response.and_then(|r| r.str("outcome"));
        let started = response.and_then(|r| r.bool("installation_started"));
        let start_status_accurate = match outcome {
            Some("installed") => started == Some(true),
            Some("already_installed") => started == Some(false),
            _ => false,
        };
        let accepted = release_valid
            && request_valid
            && response_valid
            && target_bound
            && release_bound
            && verified
            && response.and_then(|r| r.str("artifact_verification")) == Some("verified")
            && start_status_accurate;
        // Unknown, or anything after installation may have started, that is
        // not an accepted result needs exact-target reconciliation.
        let may_have_changed = matches!(
            outcome,
            Some("outcome_unknown" | "installed" | "already_installed")
        ) || started == Some(true);
        evaluations.push(object(vec![
            ("case_id", string(case_id(case)?)),
            ("release_schema_valid", Json::Bool(release_valid)),
            ("request_schema_valid", Json::Bool(request_valid)),
            ("response_schema_valid", Json::Bool(response_valid)),
            ("target_bound", Json::Bool(target_bound)),
            ("release_bound", Json::Bool(release_bound)),
            ("artifact_set_complete", Json::Bool(complete)),
            ("artifact_checksums_verified", Json::Bool(verified)),
            ("installation_result_accepted", Json::Bool(accepted)),
            (
                "reconciliation_required_before_retry",
                Json::Bool(!accepted && may_have_changed),
            ),
        ]));
    }
    Ok(object(vec![("evaluations", Json::Array(evaluations))]))
}

/// The configuration digest: SHA-256 of the JCS form of an object holding
/// exactly the request's `configuration` and `secret_bindings`.
fn configuration_digest(request: &Json) -> Option<String> {
    let digest_input = object(vec![
        ("configuration", request.get("configuration")?.clone()),
        ("secret_bindings", request.get("secret_bindings")?.clone()),
    ]);
    jcs::sha256_hex(&digest_input).ok()
}

/// The target's environment agrees with its instance reference.
fn target_consistent(target: Option<&Json>) -> bool {
    target.is_some_and(|target| {
        target.str("environment_id").is_some()
            && target.str("environment_id")
                == target
                    .get("instance_ref")
                    .and_then(|r| r.str("environment_id"))
    })
}

/// game.configure.pinned-schema-and-result-binding. Its cases live at the
/// fixture's top level; `input.release` is the pinned release.
pub fn configure(fixture: &Json, schemas: &Schemas, version: &str) -> Result<Json, String> {
    let release = fixture.get("input").and_then(|input| input.get("release"));
    let mut evaluations = Vec::new();
    for case in fixture.items("evaluations") {
        let request = case.get("request").ok_or("case without request")?;
        let output = case.get("output").ok_or("case without output")?;
        let request_valid = game_schema(schemas, version, "configure-input", Some(request));
        let request_consistent = target_consistent(request.get("target"));
        let schema_pin = release.is_some_and(|release| {
            release.str("configuration_schema_uri").is_some()
                && release.str("configuration_schema_uri")
                    == request.str("configuration_schema_uri")
                && same_present(
                    release.get("configuration_schema_checksum"),
                    request.get("configuration_schema_checksum"),
                )
        });
        let release_bound = release.and_then(|r| r.str("release_id")).is_some()
            && release.and_then(|r| r.str("release_id")) == request.str("release_id");
        let dispatch = request_valid && request_consistent && schema_pin && release_bound;
        let result_valid = game_schema(schemas, version, "configure-output", Some(output));
        let result_bound = same_present(request.get("target"), output.get("target"))
            && target_consistent(output.get("target"))
            && request.str("release_id").is_some()
            && request.str("release_id") == output.str("release_id");
        let validation = output.str("validation_result");
        let started = output.bool("application_started") == Some(true);
        // Application may begin only after validation finished as valid.
        let validation_first = !started || validation == Some("valid");
        let reported = output.get("configuration_digest");
        let digest_matches = reported.and_then(|d| d.str("algorithm")) == Some("sha256")
            && configuration_digest(request).is_some()
            && reported.and_then(|d| d.str("digest")) == configuration_digest(request).as_deref();
        let outcome = output.str("outcome");
        let confirmed = dispatch
            && result_valid
            && result_bound
            && validation_first
            && digest_matches
            && validation == Some("valid")
            && matches!(outcome, Some("applied" | "unchanged"));
        // After dispatch, anything unconfirmed that may have applied, or any
        // contradictory or mismatched result, needs reconciliation.
        let contradictory = !result_valid || !result_bound || !validation_first || !digest_matches;
        let reconcile =
            dispatch && !confirmed && (outcome == Some("outcome_unknown") || contradictory);
        evaluations.push(object(vec![
            ("case_id", string(case_id(case)?)),
            ("request_schema_valid", Json::Bool(request_valid)),
            ("request_target_consistent", Json::Bool(request_consistent)),
            ("schema_pin_matches", Json::Bool(schema_pin)),
            ("release_bound", Json::Bool(release_bound)),
            ("dispatch_allowed", Json::Bool(dispatch)),
            ("result_schema_valid", Json::Bool(result_valid)),
            ("result_target_bound", Json::Bool(result_bound)),
            (
                "validation_before_application",
                Json::Bool(validation_first),
            ),
            ("configuration_confirmed", Json::Bool(confirmed)),
            ("must_reconcile_before_retry", Json::Bool(reconcile)),
            ("configuration_digest_matches", Json::Bool(digest_matches)),
        ]));
    }
    Ok(object(vec![("evaluations", Json::Array(evaluations))]))
}
