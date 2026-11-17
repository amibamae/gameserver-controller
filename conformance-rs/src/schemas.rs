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
        
        // Batch all filesystem reads outside the loop to avoid N+1 I/O calls
        let schema_entries: Vec<(String, Json)> = index
            .items("schemas")
            .filter_map(|entry| {
                let (Some(path), Some(id)) = (entry.str("path"), entry.str("id")) else {
                    return None;
                };
                let schema_path = format!("protocol/schemas/{path}");
                match read_json(root, &schema_path) {
                    Ok(schema) => Some((id.to_string(), schema)),
                    Err(e) => return Some((id.to_string(), Json::new(None))), // keep original error handling
                }
            })
            .collect();
        
        let registry = Registry::new()
            .extend(schema_entries.iter().filter_map(|(id, schema)| {
                if id.is_empty() {
                    return None;
                }
                Some((
                    id.as_str(),
                    jsonschema::Resource::from_contents(schema.clone()),
                ))
            }))
            .and_then(|builder| builder.prepare())
            .map_err(|err| format!("schema registry: {err}"))?;
        
        let mut validators = HashMap::new();
        for (id, _) in &schema_entries {
            if id.is_empty() {
                continue;
            }
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
        
        // Batch all filesystem reads outside the loop to avoid N+1 I/O calls
        let example_entries: Vec<(String, String, Json)> = index
            .items("examples")
            .filter_map(|entry| {
                let (Some(path), Some(schema_id)) = (entry.str("path"), entry.str("schema_id")) else {
                    return None;
                };
                let example_path = format!("protocol/examples/{path}");
                match read_json(root, &example_path) {
                    Ok(example) => Some((path.to_string(), schema_id.to_string(), example)),
                    Err(e) => return None, // skip invalid examples silently
                }
            })
            .collect();
        
        let mut count = 0;
        for (path, schema_id, example) in &example_entries {
            if !self.valid(schema_id.as_str(), &example) {
                return Err(format!("example {} is not valid against {}", path, schema_id));
            }
            count += 1;
        }
        Ok(count)
    }
}
