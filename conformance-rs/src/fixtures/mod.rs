//! Fixture evaluators. Each one computes its own result from a fixture's
//! `input`, independently of the Go evaluator, and the runner compares that
//! result with the fixture's published `expected` object.

mod execution;
mod game;
mod network;
mod operations;
mod proxy;
mod velocity;
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
        "game.release.describe.integrity-and-identity" => {
            game::release_describe(input, schemas, version)
        }
        "game.install.release-binding-and-reconciliation" => game::install(input, schemas, version),
        "game.configure.pinned-schema-and-result-binding" => {
            game::configure(fixture, schemas, version)
        }
        "execution.unit.start.accepted-not-ready" => execution::start(input, schemas, version),
        "execution.unit.graceful-stop.unknown-requires-inspection" => {
            execution::graceful_stop(input, schemas, version)
        }
        "execution.unit.inspect.exact-target-state" => execution::inspect(input, schemas, version),
        "execution.unit.logs.byte-bound-and-redaction" => execution::logs(input, schemas, version),
        "execution.unit.health.diagnostic-redaction-and-binding" => {
            execution::health(input, schemas, version)
        }
        "integration.proxy.register.accepted-is-not-routable" => {
            proxy::register(input, schemas, version)
        }
        "integration.proxy.players.inspect.fresh-count-and-target-binding" => {
            proxy::players_inspect(input, schemas, version)
        }
        "velocity-transfer-outcome-unknown-reconciliation" => {
            velocity::reconciliation(input, schemas, version)
        }
        "velocity-transfer-privacy-canaries" => velocity::privacy(input, schemas, version),
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

/// Parses an RFC 3339 timestamp with an explicit offset into nanoseconds
/// since the Unix epoch. Returns None for anything malformed.
pub fn timestamp(text: &str) -> Option<i128> {
    let bytes = text.as_bytes();
    if bytes.len() < 20
        || bytes[4] != b'-'
        || bytes[7] != b'-'
        || !matches!(bytes[10], b'T' | b't')
        || bytes[13] != b':'
        || bytes[16] != b':'
    {
        return None;
    }
    let digits = |range: std::ops::Range<usize>| -> Option<i64> {
        let part = text.get(range)?;
        part.bytes()
            .all(|b| b.is_ascii_digit())
            .then(|| part.parse().ok())?
    };
    let (year, month, day) = (digits(0..4)?, digits(5..7)?, digits(8..10)?);
    let (hour, minute, second) = (digits(11..13)?, digits(14..16)?, digits(17..19)?);
    let mut rest = &text[19..];
    let mut nanos: i128 = 0;
    if let Some(fraction) = rest.strip_prefix('.') {
        let end = fraction
            .find(|c: char| !c.is_ascii_digit())
            .unwrap_or(fraction.len());
        if end == 0 || end > 9 {
            return None;
        }
        let padded = format!("{:0<9}", &fraction[..end]);
        nanos = padded.parse().ok()?;
        rest = &fraction[end..];
    }
    let offset_seconds: i64 = match rest {
        "Z" | "z" => 0,
        _ => {
            let sign = match rest.as_bytes().first()? {
                b'+' => 1,
                b'-' => -1,
                _ => return None,
            };
            if rest.len() != 6 || rest.as_bytes()[3] != b':' {
                return None;
            }
            let hours: i64 = rest[1..3].parse().ok()?;
            let minutes: i64 = rest[4..6].parse().ok()?;
            if hours > 23 || minutes > 59 {
                return None;
            }
            sign * (hours * 3600 + minutes * 60)
        }
    };
    let leap = (year % 4 == 0 && year % 100 != 0) || year % 400 == 0;
    let month_days = [
        31,
        if leap { 29 } else { 28 },
        31,
        30,
        31,
        30,
        31,
        31,
        30,
        31,
        30,
        31,
    ];
    if !(1..=12).contains(&month)
        || day < 1
        || day > month_days[(month - 1) as usize]
        || hour > 23
        || minute > 59
        || second > 60
    {
        return None;
    }
    // Days from civil (Howard Hinnant's algorithm).
    let y = if month <= 2 { year - 1 } else { year };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = (month + 9) % 12;
    let doy = (153 * mp + 2) / 5 + day - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    let days = era * 146_097 + doe - 719_468;
    let seconds = days * 86_400 + hour * 3600 + minute * 60 + second - offset_seconds;
    Some(seconds as i128 * 1_000_000_000 + nanos)
}

/// Reports whether `at` lies in [observed_at, expires_at) of an assertion.
pub fn current_at(assertion: &Json, at: Option<i128>) -> bool {
    let observed = assertion.str("observed_at").and_then(timestamp);
    let expires = assertion.str("expires_at").and_then(timestamp);
    matches!((observed, expires, at), (Some(o), Some(e), Some(t)) if o <= t && t < e)
}

/// Reports whether an observation at `observed` is usable at `at`: not in
/// the future and no older than `maximum_age_seconds` (inclusive).
pub fn fresh(observed: Option<&str>, at: Option<i128>, maximum_age_seconds: Option<f64>) -> bool {
    match (observed.and_then(timestamp), at, maximum_age_seconds) {
        (Some(o), Some(t), Some(age)) => o <= t && (t - o) as f64 <= age * 1e9,
        _ => false,
    }
}

/// Reports whether any string in `value`, member names included, contains
/// any of `probes`.
pub fn contains_probe(value: &Json, probes: &[&str]) -> bool {
    match value {
        Json::String(text) => probes.iter().any(|probe| text.contains(probe)),
        Json::Array(items) => items.iter().any(|item| contains_probe(item, probes)),
        Json::Object(members) => members.iter().any(|(name, member)| {
            probes.iter().any(|probe| name.contains(probe)) || contains_probe(member, probes)
        }),
        _ => false,
    }
}

/// The string items of an array member.
pub fn strings<'a>(value: &'a Json, key: &str) -> Vec<&'a str> {
    value.items(key).iter().filter_map(Json::as_str).collect()
}

/// Reports whether both values are present and equal as JSON data.
pub fn same_present(left: Option<&Json>, right: Option<&Json>) -> bool {
    matches!((left, right), (Some(a), Some(b)) if crate::jcs::same(a, b))
}
