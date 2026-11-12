//! A strict JSON parser written for this consumer.
//!
//! It applies the protocol's interoperable JSON rules directly to the raw
//! bytes, before any other library sees them: input must be valid UTF-8,
//! object member names must be unique (compared after unescaping), escaped
//! surrogates must pair, numbers must be finite, and an integral number must
//! lie within the safe range ±(2^53 − 1). Member order is preserved.

use std::fmt;

/// The largest integer every protocol implementation can represent exactly.
const MAX_SAFE_INTEGER: &str = "9007199254740991";

#[derive(Clone, Debug, PartialEq)]
pub enum Json {
    Null,
    Bool(bool),
    /// The number's source text; it has already passed the safety checks.
    Number(String),
    String(String),
    Array(Vec<Json>),
    Object(Vec<(String, Json)>),
}

#[derive(Debug, PartialEq)]
pub struct ParseError(pub String);

impl fmt::Display for ParseError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for ParseError {}

/// Parses exactly one JSON value from raw bytes.
pub fn parse(bytes: &[u8]) -> Result<Json, ParseError> {
    let text =
        std::str::from_utf8(bytes).map_err(|_| ParseError("input is not valid UTF-8".into()))?;
    let mut parser = Parser {
        chars: text.as_bytes(),
        pos: 0,
    };
    parser.skip_whitespace();
    let value = parser.value(0)?;
    parser.skip_whitespace();
    if parser.pos != parser.chars.len() {
        return Err(ParseError("trailing data after the JSON value".into()));
    }
    Ok(value)
}

struct Parser<'a> {
    chars: &'a [u8],
    pos: usize,
}

const MAX_DEPTH: usize = 512;

impl Parser<'_> {
    fn fail<T>(&self, message: &str) -> Result<T, ParseError> {
        Err(ParseError(format!("{message} at byte {}", self.pos)))
    }

    fn peek(&self) -> Option<u8> {
        self.chars.get(self.pos).copied()
    }

    fn skip_whitespace(&mut self) {
        while matches!(self.peek(), Some(b' ' | b'\t' | b'\n' | b'\r')) {
            self.pos += 1;
        }
    }

    fn expect(&mut self, literal: &str) -> Result<(), ParseError> {
        if self.chars[self.pos..].starts_with(literal.as_bytes()) {
            self.pos += literal.len();
            Ok(())
        } else {
            self.fail("invalid literal")
        }
    }

    fn value(&mut self, depth: usize) -> Result<Json, ParseError> {
        if depth > MAX_DEPTH {
            return self.fail("nesting is too deep");
        }
        match self.peek() {
            Some(b'{') => self.object(depth),
            Some(b'[') => self.array(depth),
            Some(b'"') => self.string().map(Json::String),
            Some(b't') => self.expect("true").map(|_| Json::Bool(true)),
            Some(b'f') => self.expect("false").map(|_| Json::Bool(false)),
            Some(b'n') => self.expect("null").map(|_| Json::Null),
            Some(b'-' | b'0'..=b'9') => self.number(),
            _ => self.fail("expected a JSON value"),
        }
    }

    fn object(&mut self, depth: usize) -> Result<Json, ParseError> {
        self.pos += 1;
        let mut members: Vec<(String, Json)> = Vec::new();
        self.skip_whitespace();
        if self.peek() == Some(b'}') {
            self.pos += 1;
            return Ok(Json::Object(members));
        }
        loop {
            self.skip_whitespace();
            if self.peek() != Some(b'"') {
                return self.fail("expected an object member name");
            }
            let name = self.string()?;
            if members.iter().any(|(existing, _)| existing == &name) {
                return self.fail("duplicate object member name");
            }
            self.skip_whitespace();
            if self.peek() != Some(b':') {
                return self.fail("expected ':'");
            }
            self.pos += 1;
            self.skip_whitespace();
            let value = self.value(depth + 1)?;
            members.push((name, value));
            self.skip_whitespace();
            match self.peek() {
                Some(b',') => self.pos += 1,
                Some(b'}') => {
                    self.pos += 1;
                    return Ok(Json::Object(members));
                }
                _ => return self.fail("expected ',' or '}'"),
            }
        }
    }

    fn array(&mut self, depth: usize) -> Result<Json, ParseError> {
        self.pos += 1;
        let mut items = Vec::new();
        self.skip_whitespace();
        if self.peek() == Some(b']') {
            self.pos += 1;
            return Ok(Json::Array(items));
        }
        loop {
            self.skip_whitespace();
            items.push(self.value(depth + 1)?);
            self.skip_whitespace();
            match self.peek() {
                Some(b',') => self.pos += 1,
                Some(b']') => {
                    self.pos += 1;
                    return Ok(Json::Array(items));
                }
                _ => return self.fail("expected ',' or ']'"),
            }
        }
    }

    fn hex4(&mut self) -> Result<u32, ParseError> {
        let digits = self
            .chars
            .get(self.pos..self.pos + 4)
            .ok_or_else(|| ParseError("truncated \\u escape".into()))?;
        let text =
            std::str::from_utf8(digits).map_err(|_| ParseError("invalid \\u escape".into()))?;
        let value =
            u32::from_str_radix(text, 16).map_err(|_| ParseError("invalid \\u escape".into()))?;
        self.pos += 4;
        Ok(value)
    }

    fn string(&mut self) -> Result<String, ParseError> {
        self.pos += 1;
        let mut out = String::new();
        loop {
            let start = self.pos;
            while let Some(byte) = self.peek() {
                if byte == b'"' || byte == b'\\' || byte < 0x20 {
                    break;
                }
                self.pos += 1;
            }
            // Input is already valid UTF-8 and the stop bytes are ASCII, so
            // this slice is on character boundaries.
            out.push_str(
                std::str::from_utf8(&self.chars[start..self.pos]).expect("valid UTF-8 slice"),
            );
            match self.peek() {
                Some(b'"') => {
                    self.pos += 1;
                    return Ok(out);
                }
                Some(b'\\') => {
                    self.pos += 1;
                    let escape = self
                        .peek()
                        .ok_or_else(|| ParseError("truncated escape".into()))?;
                    self.pos += 1;
                    match escape {
                        b'"' => out.push('"'),
                        b'\\' => out.push('\\'),
                        b'/' => out.push('/'),
                        b'b' => out.push('\u{8}'),
                        b'f' => out.push('\u{c}'),
                        b'n' => out.push('\n'),
                        b'r' => out.push('\r'),
                        b't' => out.push('\t'),
                        b'u' => {
                            let first = self.hex4()?;
                            let code = match first {
                                0xD800..=0xDBFF => {
                                    if !self.chars[self.pos..].starts_with(b"\\u") {
                                        return self.fail("unpaired high surrogate");
                                    }
                                    self.pos += 2;
                                    let second = self.hex4()?;
                                    if !(0xDC00..=0xDFFF).contains(&second) {
                                        return self.fail("unpaired high surrogate");
                                    }
                                    0x10000 + ((first - 0xD800) << 10) + (second - 0xDC00)
                                }
                                0xDC00..=0xDFFF => return self.fail("unpaired low surrogate"),
                                other => other,
                            };
                            out.push(
                                char::from_u32(code)
                                    .ok_or_else(|| ParseError("invalid code point".into()))?,
                            );
                        }
                        _ => return self.fail("invalid escape"),
                    }
                }
                _ => return self.fail("unterminated string or control character"),
            }
        }
    }

    fn number(&mut self) -> Result<Json, ParseError> {
        let start = self.pos;
        let negative = self.peek() == Some(b'-');
        if negative {
            self.pos += 1;
        }
        let int_start = self.pos;
        match self.peek() {
            Some(b'0') => self.pos += 1,
            Some(b'1'..=b'9') => {
                while matches!(self.peek(), Some(b'0'..=b'9')) {
                    self.pos += 1;
                }
            }
            _ => return self.fail("invalid number"),
        }
        let integer_digits = &self.chars[int_start..self.pos];
        let mut fraction_digits: &[u8] = &[];
        if self.peek() == Some(b'.') {
            self.pos += 1;
            let fraction_start = self.pos;
            while matches!(self.peek(), Some(b'0'..=b'9')) {
                self.pos += 1;
            }
            if self.pos == fraction_start {
                return self.fail("invalid number");
            }
            fraction_digits = &self.chars[fraction_start..self.pos];
        }
        let mut exponent: i64 = 0;
        if matches!(self.peek(), Some(b'e' | b'E')) {
            self.pos += 1;
            let mut sign = 1;
            match self.peek() {
                Some(b'+') => self.pos += 1,
                Some(b'-') => {
                    sign = -1;
                    self.pos += 1;
                }
                _ => {}
            }
            let exponent_start = self.pos;
            while matches!(self.peek(), Some(b'0'..=b'9')) {
                self.pos += 1;
            }
            if self.pos == exponent_start {
                return self.fail("invalid number");
            }
            let text =
                std::str::from_utf8(&self.chars[exponent_start..self.pos]).expect("ASCII digits");
            // Saturate absurd exponents; the range checks below reject them.
            exponent = sign
                * text
                    .parse::<i64>()
                    .unwrap_or(i64::MAX / 2)
                    .min(i64::MAX / 2);
        }
        let source = std::str::from_utf8(&self.chars[start..self.pos])
            .expect("ASCII number")
            .to_string();
        check_number(integer_digits, fraction_digits, exponent, &source)
            .map_err(|message| ParseError(format!("{message} at byte {start}")))?;
        Ok(Json::Number(source))
    }
}

/// Rejects non-finite numbers and integral numbers outside the safe range,
/// using exact decimal arithmetic on the digits.
fn check_number(
    integer_digits: &[u8],
    fraction_digits: &[u8],
    exponent: i64,
    source: &str,
) -> Result<(), &'static str> {
    if !source.parse::<f64>().map(f64::is_finite).unwrap_or(false) {
        return Err("number is not finite");
    }
    // value = digits × 10^(exponent − fraction length), digits without leading zeros
    let mut digits: Vec<u8> = integer_digits
        .iter()
        .chain(fraction_digits.iter())
        .copied()
        .collect();
    let mut scale = exponent - fraction_digits.len() as i64;
    while digits.len() > 1 && digits[0] == b'0' {
        digits.remove(0);
    }
    if digits == b"0" {
        return Ok(());
    }
    while scale < 0 && digits.last() == Some(&b'0') {
        digits.pop();
        scale += 1;
    }
    if scale < 0 {
        return Ok(()); // not an integer
    }
    // Integral: compare digits followed by `scale` zeros with the safe limit.
    let length = digits.len() as i64 + scale;
    let limit = MAX_SAFE_INTEGER.len() as i64;
    if length > limit {
        return Err("integer is outside the safe range");
    }
    if length == limit {
        let mut full = digits.clone();
        full.extend(std::iter::repeat_n(b'0', scale as usize));
        if full.as_slice() > MAX_SAFE_INTEGER.as_bytes() {
            return Err("integer is outside the safe range");
        }
    }
    Ok(())
}

impl Json {
    pub fn get(&self, key: &str) -> Option<&Json> {
        match self {
            Json::Object(members) => members
                .iter()
                .find(|(name, _)| name == key)
                .map(|(_, value)| value),
            _ => None,
        }
    }

    pub fn str(&self, key: &str) -> Option<&str> {
        match self.get(key) {
            Some(Json::String(value)) => Some(value),
            _ => None,
        }
    }

    pub fn bool(&self, key: &str) -> Option<bool> {
        match self.get(key) {
            Some(Json::Bool(value)) => Some(*value),
            _ => None,
        }
    }

    pub fn items(&self, key: &str) -> &[Json] {
        match self.get(key) {
            Some(Json::Array(items)) => items,
            _ => &[],
        }
    }

    pub fn as_str(&self) -> Option<&str> {
        match self {
            Json::String(value) => Some(value),
            _ => None,
        }
    }

    /// Converts to a serde_json value for the schema validator. Integral
    /// numbers become integers; others become f64.
    pub fn to_serde(&self) -> serde_json::Value {
        match self {
            Json::Null => serde_json::Value::Null,
            Json::Bool(value) => serde_json::Value::Bool(*value),
            Json::Number(text) => {
                if let Ok(integer) = text.parse::<i64>() {
                    serde_json::Value::from(integer)
                } else {
                    let float: f64 = text.parse().expect("validated number");
                    if float.fract() == 0.0 && float.abs() <= 9_007_199_254_740_991.0 {
                        serde_json::Value::from(float as i64)
                    } else {
                        serde_json::Number::from_f64(float)
                            .map(serde_json::Value::Number)
                            .unwrap_or(serde_json::Value::Null)
                    }
                }
            }
            Json::String(value) => serde_json::Value::String(value.clone()),
            Json::Array(items) => {
                serde_json::Value::Array(items.iter().map(Json::to_serde).collect())
            }
            Json::Object(members) => serde_json::Value::Object(
                members
                    .iter()
                    .map(|(name, value)| (name.clone(), value.to_serde()))
                    .collect(),
            ),
        }
    }

    /// The numeric value, for comparisons.
    pub fn as_f64(&self) -> Option<f64> {
        match self {
            Json::Number(text) => text.parse().ok(),
            _ => None,
        }
    }
}

/// Reports whether `actual` contains everything in `expected`: every expected
/// object member must exist and match, arrays must match element by element,
/// and scalars must be equal (numbers by value). Extra actual members are
/// allowed.
pub fn subset_matches(expected: &Json, actual: &Json) -> bool {
    match (expected, actual) {
        (Json::Object(members), Json::Object(_)) => members.iter().all(|(name, value)| {
            actual
                .get(name)
                .is_some_and(|other| subset_matches(value, other))
        }),
        (Json::Array(left), Json::Array(right)) => {
            left.len() == right.len() && left.iter().zip(right).all(|(a, b)| subset_matches(a, b))
        }
        (Json::Number(_), Json::Number(_)) => expected.as_f64() == actual.as_f64(),
        _ => expected == actual,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn accepts(input: &[u8]) -> bool {
        parse(input).is_ok()
    }

    #[test]
    fn rejects_duplicates_including_escaped_and_nested() {
        assert!(!accepts(br#"{"id":1,"id":2}"#));
        assert!(!accepts(br#"{"id":1,"id":2}"#));
        assert!(!accepts(br#"{"x":[{"id":1,"id":2}]}"#));
        assert!(accepts(br#"{"id":1,"idx":2}"#));
    }

    #[test]
    fn enforces_the_safe_integer_range_exactly() {
        for ok in [
            "9007199254740991",
            "-9007199254740991",
            "9007199254740991.0",
            "9.007199254740991e15",
            "1.5",
            "1e-400",
            "0.0e999",
            "12.5e1",
        ] {
            assert!(accepts(ok.as_bytes()), "{ok}");
        }
        for bad in [
            "9007199254740992",
            "-9007199254740992",
            "9007199254740992.0",
            "9.007199254740992e15",
            "1e999",
            "1e16",
            "NaN",
            "Infinity",
            "01",
            "1.",
            "-",
        ] {
            assert!(!accepts(bad.as_bytes()), "{bad}");
        }
    }

    #[test]
    fn requires_paired_surrogates_and_valid_utf8() {
        assert_eq!(
            parse(b"\"\\uD83D\\uDE00\""),
            Ok(Json::String("\u{1F600}".into()))
        );
        assert_eq!(parse(br#""\\uD800""#), Ok(Json::String("\\uD800".into())));
        for bad in [
            br#""\uD800""#.as_slice(),
            br#""\uDC00""#,
            br#""\uDC00\uD800""#,
            br#""\uD800A""#,
            br#"{"\uD800":1}"#,
            b"\"\xff\"",
            b"\"\xc0\xaf\"",
            b"\"\xed\xa0\x80\"",
            b"\"\xf0\x9f\"",
        ] {
            assert!(!accepts(bad), "{bad:?}");
        }
    }

    #[test]
    fn subset_matching_allows_extra_members_only() {
        let actual = parse(br#"{"a":1,"b":[true,{"c":"x","d":2}],"extra":null}"#).unwrap();
        assert!(subset_matches(
            &parse(br#"{"a":1.0,"b":[true,{"c":"x"}]}"#).unwrap(),
            &actual
        ));
        assert!(!subset_matches(&parse(br#"{"a":2}"#).unwrap(), &actual));
        assert!(!subset_matches(
            &parse(br#"{"b":[true]}"#).unwrap(),
            &actual
        ));
    }
}
