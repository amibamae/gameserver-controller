use super::{object, string};
use crate::json::{self, Json};

/// protocol.json.raw-wire-interoperability: decode the hexadecimal bytes and
/// hand them directly to the strict parser, never reserialized first.
pub fn raw_wire(input: &Json) -> Result<Json, String> {
    let mut evaluations = Vec::new();
    for case in input.items("evaluations") {
        let id = case.str("case_id").ok_or("case without case_id")?;
        let bytes = decode_hex(case.str("wire_hex").ok_or("case without wire_hex")?)?;
        let mut result = vec![("case_id", string(id))];
        match json::parse(&bytes) {
            Ok(value) => {
                result.push(("accepted", Json::Bool(true)));
                if let Json::String(text) = value {
                    result.push(("parsed_string", Json::String(text)));
                }
            }
            Err(_) => result.push(("accepted", Json::Bool(false))),
        }
        evaluations.push(object(result));
    }
    Ok(object(vec![("evaluations", Json::Array(evaluations))]))
}

fn decode_hex(text: &str) -> Result<Vec<u8>, String> {
    if !text.len().is_multiple_of(2) {
        return Err("odd-length wire_hex".into());
    }
    (0..text.len())
        .step_by(2)
        .map(|i| {
            u8::from_str_radix(&text[i..i + 2], 16).map_err(|_| "invalid wire_hex".to_string())
        })
        .collect()
}
