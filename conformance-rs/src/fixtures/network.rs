use super::{object, schema_id, string};
use crate::json::Json;
use crate::schemas::Schemas;

const TRANSFER: &str = "integration.*.players.transfer";
const DNS: [&str; 3] = [
    "integration.dns.record.inspect",
    "integration.dns.record.upsert",
    "integration.dns.record.delete",
];
const PROXY: [&str; 6] = [
    "integration.proxy.backend.inspect",
    "integration.proxy.backend.register",
    "integration.proxy.backend.drain",
    "integration.proxy.backend.unregister",
    "integration.proxy.cohort.inspect",
    "integration.proxy.session.inspect",
];

/// Integrations section 6: required capabilities by join mode, plus DNS for
/// any hostname backend.
fn requirements_for(profile: &Json) -> Vec<(&'static str, &'static str)> {
    let mut required: Vec<(&str, &str)> = Vec::new();
    match profile.str("join_mode") {
        Some("proxy") => {
            required.extend(PROXY.iter().map(|capability| ("proxy", *capability)));
            required.push(("proxy", TRANSFER));
            if profile
                .items("backends")
                .iter()
                .any(|backend| backend.str("address_mode") == Some("hostname"))
            {
                required.extend(DNS.iter().map(|capability| ("dns", *capability)));
            }
        }
        Some("direct_dns") => required.extend(DNS.iter().map(|capability| ("dns", *capability))),
        _ => {}
    }
    required.sort();
    required
}

fn satisfies(capabilities: &[&str], requirement: &str) -> bool {
    if requirement != TRANSFER {
        return capabilities.contains(&requirement);
    }
    capabilities.iter().any(|name| {
        name.starts_with("integration.")
            && name.ends_with(".players.transfer")
            && name.matches('.').count() == 3
    })
}

fn as_json(requirements: &[(&str, &str)]) -> Json {
    Json::Array(
        requirements
            .iter()
            .map(|(role, capability)| {
                object(vec![
                    ("role", string(role)),
                    ("capability", string(capability)),
                ])
            })
            .collect(),
    )
}

/// integration.network-profile.requirements
pub fn requirements(input: &Json, schemas: &Schemas, version: &str) -> Result<Json, String> {
    let profile_schema = schema_id(version, "integrations", "network-profile");
    let mut evaluations = Vec::new();
    for case in input.items("evaluations") {
        let id = case.str("case_id").ok_or("case without case_id")?;
        let profile = case
            .get("network_profile")
            .ok_or("case without network_profile")?;
        let environment = case.str("environment_id").unwrap_or_default();
        let required = requirements_for(profile);
        let missing: Vec<(&str, &str)> = required
            .iter()
            .copied()
            .filter(|(role, capability)| {
                let adapter_id = match *role {
                    "proxy" => profile.str("proxy_adapter_id"),
                    _ => profile.str("dns_adapter_id"),
                };
                let registered = case.items("registered_adapters").iter().find(|adapter| {
                    adapter.str("adapter_id").is_some() && adapter.str("adapter_id") == adapter_id
                });
                match registered {
                    Some(adapter) if adapter.str("environment_id") == Some(environment) => {
                        let names: Vec<&str> = adapter
                            .items("capabilities")
                            .iter()
                            .filter_map(Json::as_str)
                            .collect();
                        !satisfies(&names, capability)
                    }
                    _ => true,
                }
            })
            .collect();
        evaluations.push(object(vec![
            ("case_id", string(id)),
            (
                "profile_schema_valid",
                Json::Bool(schemas.valid(&profile_schema, profile)),
            ),
            ("required_capabilities", as_json(&required)),
            ("missing_capabilities", as_json(&missing)),
            ("planning_permitted", Json::Bool(missing.is_empty())),
        ]));
    }
    Ok(object(vec![("evaluations", Json::Array(evaluations))]))
}
