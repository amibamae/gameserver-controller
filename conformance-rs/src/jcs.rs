//! RFC 8785 JSON Canonicalization Scheme (JCS), written from the RFC.
//!
//! Object members are sorted by the UTF-16 code units of their names,
//! strings use the minimal JSON escapes, and numbers use the ECMAScript
//! `Number.prototype.toString` form of the nearest IEEE 754 double.

use sha2::{Digest, Sha256};

use crate::json::Json;

/// The canonical UTF-8 bytes of `value`.
pub fn canonicalize(value: &Json) -> Result<String, String> {
    let mut out = String::new();
    write(value, &mut out)?;
    Ok(out)
}

/// Lowercase hex SHA-256 of the canonical bytes.
pub fn sha256_hex(value: &Json) -> Result<String, String> {
    let digest = Sha256::digest(canonicalize(value)?.as_bytes());
    Ok(crate::manifest::hex(&digest))
}

/// Reports whether two values are equal as JSON data, ignoring member order.
pub fn same(left: &Json, right: &Json) -> bool {
    matches!((canonicalize(left), canonicalize(right)), (Ok(a), Ok(b)) if a == b)
}

fn write(value: &Json, out: &mut String) -> Result<(), String> {
    match value {
        Json::Null => out.push_str("null"),
        Json::Bool(true) => out.push_str("true"),
        Json::Bool(false) => out.push_str("false"),
        Json::Number(text) => {
            let number: f64 = text.parse().map_err(|_| format!("invalid number {text}"))?;
            out.push_str(&number_to_string(number)?);
        }
        Json::String(text) => write_string(text, out),
        Json::Array(items) => {
            out.push('[');
            for (index, item) in items.iter().enumerate() {
                if index > 0 {
                    out.push(',');
                }
                write(item, out)?;
            }
            out.push(']');
        }
        Json::Object(members) => {
            let mut sorted: Vec<&(String, Json)> = members.iter().collect();
            sorted.sort_by(|a, b| a.0.encode_utf16().cmp(b.0.encode_utf16()));
            out.push('{');
            for (index, (name, member)) in sorted.into_iter().enumerate() {
                if index > 0 {
                    out.push(',');
                }
                write_string(name, out);
                out.push(':');
                write(member, out)?;
            }
            out.push('}');
        }
    }
    Ok(())
}

fn write_string(text: &str, out: &mut String) {
    out.push('"');
    for c in text.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{8}' => out.push_str("\\b"),
            '\t' => out.push_str("\\t"),
            '\n' => out.push_str("\\n"),
            '\u{c}' => out.push_str("\\f"),
            '\r' => out.push_str("\\r"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
}

/// ECMAScript Number.prototype.toString (ECMA-262 Number::toString, radix
/// 10), via ryu-js. Rust's own shortest formatting is not enough: it breaks
/// exact ties differently from ECMAScript (for example 1424953923781206.25).
pub fn number_to_string(value: f64) -> Result<String, String> {
    if !value.is_finite() {
        return Err("non-finite numbers have no JSON form".into());
    }
    if value == 0.0 {
        return Ok("0".into());
    }
    Ok(ryu_js::Buffer::new().format_finite(value).to_string())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::json::parse;

    #[test]
    fn numbers_follow_ecmascript() {
        // RFC 8785 appendix B sample values (IEEE 754 bit patterns).
        for (bits, want) in [
            (0x0000000000000000u64, "0"),
            (0x8000000000000000, "0"),
            (0x0000000000000001, "5e-324"),
            (0x8000000000000001, "-5e-324"),
            (0x7fefffffffffffff, "1.7976931348623157e+308"),
            (0xffefffffffffffff, "-1.7976931348623157e+308"),
            (0x4340000000000000, "9007199254740992"),
            (0xc340000000000000, "-9007199254740992"),
            (0x4430000000000000, "295147905179352830000"),
            (0x44b52d02c7e14af5, "9.999999999999997e+22"),
            (0x44b52d02c7e14af6, "1e+23"),
            (0x44b52d02c7e14af7, "1.0000000000000001e+23"),
            (0x444b1ae4d6e2ef4e, "999999999999999700000"),
            (0x444b1ae4d6e2ef4f, "999999999999999900000"),
            (0x444b1ae4d6e2ef50, "1e+21"),
            (0x3eb0c6f7a0b5ed8c, "9.999999999999997e-7"),
            (0x3eb0c6f7a0b5ed8d, "0.000001"),
            (0x41b3de4355555553, "333333333.3333332"),
            (0x41b3de4355555554, "333333333.33333325"),
            (0x41b3de4355555555, "333333333.3333333"),
            (0x41b3de4355555556, "333333333.3333334"),
            (0x41b3de4355555557, "333333333.33333343"),
            (0xbecbf647612f3696, "-0.0000033333333333333333"),
            (0x43143ff3c1cb0959, "1424953923781206.2"),
            (0x46293e5939a08cea, "1e+30"),
        ] {
            assert_eq!(
                number_to_string(f64::from_bits(bits)).unwrap(),
                want,
                "{bits:x}"
            );
        }
    }

    #[test]
    fn rfc_example_canonicalizes() {
        // RFC 8785 section 3.2.2 input and 3.2.3 output.
        let input = br#"{
          "numbers": [333333333.33333329, 4.50, 2e-3, 0.000000000000000000000000001],
          "string": "\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/",
          "literals": [null, true, false]
        }"#;
        let want = "{\"literals\":[null,true,false],\"numbers\":[333333333.3333333,4.5,0.002,1e-27],\"string\":\"\u{20ac}$\\u000f\\nA'B\\\"\\\\\\\\\\\"/\"}";
        assert_eq!(canonicalize(&parse(input).unwrap()).unwrap(), want);
    }

    #[test]
    fn members_sort_by_utf16_code_units() {
        // RFC 8785 section 3.2.3 sorting example.
        let input = "{\"\u{20ac}\":1,\"\\r\":2,\"\u{fb33}\":3,\"1\":4,\"\u{1f600}\":5,\"\u{80}\":6,\"\u{f6}\":7}";
        let want = "{\"\\r\":2,\"1\":4,\"\u{80}\":6,\"\u{f6}\":7,\"\u{20ac}\":1,\"\u{1f600}\":5,\"\u{fb33}\":3}";
        assert_eq!(
            canonicalize(&parse(input.as_bytes()).unwrap()).unwrap(),
            want
        );
    }
}
