//! conformance-rs: an independent Rust consumer of the gameserver-controller
//! protocol package. It reads the package files directly, never imports or
//! runs Go code, and writes an evidence report in the shape described in
//! docs/PROTOCOL_INTEROPERABILITY_PLAN.md ("Evidence to retain").

mod fixtures;
mod jcs;
mod json;
mod manifest;
mod schemas;

use std::path::PathBuf;
use std::process::ExitCode;

use fixtures::{object, string};
use json::Json;

/// The initial interoperability profile's required fixture IDs.
const INITIAL_PROFILE: [&str; 17] = [
    "protocol.json.raw-wire-interoperability",
    "game.release.describe.integrity-and-identity",
    "game.configure.pinned-schema-and-result-binding",
    "game.install.release-binding-and-reconciliation",
    "execution.unit.start.accepted-not-ready",
    "execution.unit.graceful-stop.unknown-requires-inspection",
    "execution.unit.inspect.exact-target-state",
    "execution.unit.logs.byte-bound-and-redaction",
    "execution.unit.health.diagnostic-redaction-and-binding",
    "integration.proxy.register.accepted-is-not-routable",
    "integration.proxy.players.inspect.fresh-count-and-target-binding",
    "velocity-transfer-outcome-unknown-reconciliation",
    "velocity-transfer-privacy-canaries",
    "operations.type-binding",
    "operations.restore.final-state-policy",
    "operations.backup.no-resume-final-state",
    "operations.backup.initial-state-resume-policy",
];

fn usage() -> ExitCode {
    eprintln!("usage: conformance-rs --package <repository root> [--report <path>]");
    ExitCode::from(2)
}

fn main() -> ExitCode {
    let mut args = std::env::args().skip(1);
    let (mut root, mut report_path) = (None, None);
    while let Some(arg) = args.next() {
        match arg.as_str() {
            "--package" => root = args.next().map(PathBuf::from),
            "--report" => report_path = args.next().map(PathBuf::from),
            _ => return usage(),
        }
    }
    let Some(root) = root else { return usage() };
    match run(&root) {
        Ok((report, passed)) => {
            let text = render(&report);
            match report_path {
                Some(path) => {
                    if let Err(err) = std::fs::write(&path, text + "\n") {
                        eprintln!("write report: {err}");
                        return ExitCode::FAILURE;
                    }
                }
                None => println!("{text}"),
            }
            if passed {
                ExitCode::SUCCESS
            } else {
                eprintln!("conformance-rs: one or more included fixtures failed");
                ExitCode::FAILURE
            }
        }
        Err(err) => {
            eprintln!("conformance-rs: {err}");
            ExitCode::FAILURE
        }
    }
}

/// Runs every fixture this consumer can evaluate and builds the report.
fn run(root: &std::path::Path) -> Result<(Json, bool), String> {
    let identity = manifest::identity(root)?;
    let schemas = schemas::Schemas::load(root)?;
    let examples = schemas.check_examples(root)?;
    let mut passed = identity.matches_manifest;
    let mut results = Vec::new();
    let mut evaluated = Vec::new();
    for path in manifest::fixture_paths(root)? {
        let fixture = manifest::read_json(root, &path)?;
        let Some(outcome) = fixtures::evaluate(&fixture, &schemas) else {
            continue;
        };
        let id = fixture.str("fixture_id").unwrap_or_default().to_string();
        let expected = fixture.get("expected").cloned().unwrap_or(Json::Null);
        let cases = expected.items("evaluations").len();
        let (status, detail) = match outcome {
            Ok(actual) if json::subset_matches(&expected, &actual) => ("pass", None),
            Ok(_) => (
                "fail",
                Some("computed result does not match the published expectation".to_string()),
            ),
            Err(err) => ("fail", Some(err)),
        };
        passed &= status == "pass";
        let mut entry = vec![
            ("fixture_id", string(&id)),
            ("path", string(&path)),
            ("status", string(status)),
            ("cases", Json::Number(cases.to_string())),
        ];
        if let Some(detail) = detail {
            entry.push(("detail", Json::String(detail)));
        }
        results.push(object(entry));
        evaluated.push(id);
    }
    let missing: Vec<Json> = INITIAL_PROFILE
        .iter()
        .filter(|id| !evaluated.iter().any(|done| done == *id))
        .map(|id| string(id))
        .collect();
    let profile = if missing.is_empty() {
        "initial-interoperability-profile"
    } else {
        "exploratory-subset-of-initial-profile"
    };
    let report = object(vec![
        ("package", object(vec![
            ("protocol_version", string(&identity.protocol_version)),
            ("commit", std::env::var("GITHUB_SHA").map(Json::String).unwrap_or(Json::Null)),
            ("digest_algorithm", string(manifest::ALGORITHM)),
            ("schema_registry_digest", string(&identity.schema_registry_digest)),
            ("fixture_suite_digest", string(&identity.fixture_suite_digest)),
            ("digests_match_manifest", Json::Bool(identity.matches_manifest)),
            ("schema_count", Json::Number(identity.schema_count.to_string())),
            ("fixture_count", Json::Number(identity.fixture_count.to_string())),
        ])),
        ("claimed_profile", string(profile)),
        ("profile_fixtures_not_covered", Json::Array(missing)),
        ("implementation", object(vec![
            ("name", string(env!("CARGO_PKG_NAME"))),
            ("version", string(env!("CARGO_PKG_VERSION"))),
            ("language", string("Rust")),
            ("schema_validator", string("jsonschema crate (draft 2020-12, formats asserted, offline)")),
            ("json_parser", string("conformance-rs strict parser")),
        ])),
        ("schemas_compiled", Json::Number(schemas.count.to_string())),
        ("examples_validated", Json::Number(examples.to_string())),
        ("fixtures", Json::Array(results)),
        ("limitations", Json::Array(vec![
            string("Offline message vectors only; they do not prove that an adapter avoids side effects."),
            string("Fixtures without an evaluator here are listed in profile_fixtures_not_covered and are not claimed."),
        ])),
    ]);
    Ok((report, passed))
}

/// Serializes the report with two-space indentation.
fn render(value: &Json) -> String {
    serde_json::to_string_pretty(&value.to_serde()).unwrap_or_default()
}
