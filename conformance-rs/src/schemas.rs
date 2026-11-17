//! JSON Schema 2020-12 validation against the package's own schemas only.

use std::collections::HashMap;
use std::path::Path;

use jsonschema::{Draft, Registry, Validator};
use serde_json::json;

use crate::json::Json;
use crate::manifest::read_json;

pub struct Schemas {
    validators: HashMap<String, Validator>,
    pub count: usize,
}

impl Schemas {
    /// Loads every indexed schema through the strict parser and compiles one
    /// validator per schema ID. References resolve only to these schemas;
    /// format keywords are asserted.
    pub fn load(root: &Path) -> Result<Schemas, String> {
        let index = read_json(root, "protocol/schemas/index.json")?;
        let mut resources = Vec::new();
        for entry in index.items("schemas") {
            let (Some(path), Some(id)) = (entry.str("path"), entry.str("id")) else {
                return Err("schema index entry needs path and id".into());
            };
            let schema = read_json(root, &format!("protocol/schemas/{path}"))?;
            if schema.str("$id") != Some(id) {
                return Err(format!("{path}: $id does not match the index"));
            }
            resources.push((id.to_string(), schema.to_serde()));
        }
        let registry = Registry::new()
            .extend(resources.iter().map(|(id, schema)| {
                (
                    id.as_str(),
                    jsonschema::Resource::from_contents(schema.clone()),
                )
            }))
            .and_then(|builder| builder.prepare())
            .map_err(|err| format!("schema registry: {err}"))?;
        let mut validators = HashMap::new();
        for (id, _) in &resources {
            let validator = jsonschema::options()
                .with_draft(Draft::Draft202012)
                .should_validate_formats(true)
                .with_registry(&registry)
                .offline()
                .build(&json!({ "$ref": id }))
                .map_err(|err| format!("compile {id}: {err}"))?;
            validators.insert(id.clone(), validator);
        }
        Ok(Schemas {
            count: validators.len(),
            validators,
        })
    }

    /// Whether `value` is valid against schema `id`; an unknown ID is invalid.
    pub fn valid(&self, id: &str, value: &Json) -> bool {
        self.validators
            .get(id)
            .is_some_and(|validator| validator.is_valid(&value.to_serde()))
    }

    /// Validates every example listed in protocol/examples/index.json.
    pub fn check_examples(&self, root: &Path) -> Result<usize, String> {
        let index = read_json(root, "protocol/examples/index.json")?;
        let mut count = 0;
        for entry in index.items("examples") {
            let (Some(path), Some(id)) = (entry.str("path"), entry.str("schema_id")) else {
                return Err("example index entry needs path and schema_id".into());
            };
            let example = read_json(root, &format!("protocol/examples/{path}"))?;
            if !self.valid(id, &example) {
                return Err(format!("example {path} is not valid against {id}"));
            }
            count += 1;
        }
        Ok(count)
    }
}
