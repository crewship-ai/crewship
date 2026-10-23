//! Bounded E3 implementation profile. No admission, auth, or durable receipt.
use crate::canonical::{self, CanonicalValue};

pub const MAX_FRAME_BYTES: usize = 262_144;
pub const MAX_JSON_DEPTH: usize = 64;
pub const MAX_STRUCTURAL_TOKENS: usize = 32_768;

#[derive(Debug, PartialEq, Eq)]
pub enum WireError {
    ResourceLimit,
    InvalidJson,
    InvalidShape,
}

/// Parse unambiguous JSON, normalize binary64 numbers, and enforce the exact
/// bundled envelope schema under the small-frame implementation profile.
/// This cannot grant authority: callers still authenticate the sender, bind
/// scope/session/participant, and validate application and causal semantics.
/// Generic schema maxima for larger checkpoints are not supported here; an
/// oversized input must remain an unsaved draft or use another negotiated path.
pub fn validate_bounded_envelope(raw: &[u8]) -> Result<CanonicalValue, WireError> {
    let normalized = normalize_bounded(raw)?;
    if !crate::shape::envelope(normalized.value()) {
        return Err(WireError::InvalidShape);
    }
    Ok(normalized)
}

/// Resource-bounded strict JSON normalization for opaque application values.
/// Unlike envelope validation this does not inspect application field names.
pub fn normalize_bounded(raw: &[u8]) -> Result<CanonicalValue, WireError> {
    preflight(raw)?;
    canonical::normalize(raw).map_err(|error| match error {
        canonical::CanonicalError::Size => WireError::ResourceLimit,
        _ => WireError::InvalidJson,
    })
}

/// Apply only to schema Identifier fields, never opaque payload keys/text.
pub fn valid_identifier(value: &str) -> bool {
    !value.is_empty() && !value.contains('\0') && value.chars().count() <= 200
}

/// Allocation-free resource preflight, not a JSON parser. Structural tokens
/// are { [ , : outside quoted strings; the outer container has depth one.
/// Strict parsing afterwards rejects bad syntax, escapes, duplicate keys,
/// invalid Unicode, nonfinite numbers and trailing content.
pub(crate) fn preflight(raw: &[u8]) -> Result<(), WireError> {
    if raw.is_empty() || raw.len() > MAX_FRAME_BYTES {
        return Err(WireError::ResourceLimit);
    }
    let (mut quoted, mut escaped) = (false, false);
    let (mut depth, mut tokens) = (0usize, 0usize);
    for &byte in raw {
        if quoted {
            if escaped {
                escaped = false;
            } else if byte == b'\\' {
                escaped = true;
            } else if byte == b'"' {
                quoted = false;
            }
            continue;
        }
        match byte {
            b'"' => quoted = true,
            b'{' | b'[' => {
                depth += 1;
                tokens += 1;
                if depth > MAX_JSON_DEPTH {
                    return Err(WireError::ResourceLimit);
                }
            }
            b',' | b':' => tokens += 1,
            b'}' | b']' => {
                depth = depth.checked_sub(1).ok_or(WireError::InvalidJson)?;
            }
            _ => {}
        }
        if tokens > MAX_STRUCTURAL_TOKENS {
            return Err(WireError::ResourceLimit);
        }
    }
    if quoted || depth != 0 {
        return Err(WireError::InvalidJson);
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn boundaries_and_quoted_structures_are_counted_before_allocation() {
        for depth in [64usize, 65] {
            let json = format!("{}0{}", "[".repeat(depth), "]".repeat(depth));
            assert_eq!(preflight(json.as_bytes()).is_ok(), depth == 64);
        }
        // Opening array counts once; n elements have n-1 commas.
        for tokens in [32_768usize, 32_769] {
            let json = format!("[{}]", vec!["0"; tokens].join(","));
            assert_eq!(preflight(json.as_bytes()).is_ok(), tokens == 32_768);
        }
        let quoted = serde_json::to_vec(&"\\\"{[,:".repeat(8_000)).unwrap();
        assert!(preflight(&quoted).is_ok());
        assert!(preflight(&vec![b' '; MAX_FRAME_BYTES]).is_ok());
        assert_eq!(
            preflight(&vec![b' '; MAX_FRAME_BYTES + 1]),
            Err(WireError::ResourceLimit)
        );
        assert_eq!(preflight(b"[}"), Ok(())); // Syntax belongs to strict parsing.
        assert_eq!(
            validate_bounded_envelope(b"[}").unwrap_err(),
            WireError::InvalidJson
        );
    }
    #[test]
    fn every_positive_envelope_passes_and_unambiguous_parse_is_required() {
        let fixtures: serde_json::Value =
            serde_json::from_str(include_str!("../../../protocol/conformance-fixtures.json"))
                .unwrap();
        let mut count = 0;
        for case in fixtures["schema_cases"].as_array().unwrap() {
            if case["schema_ref"] == "#" && case["schema_valid"] == true {
                let raw = serde_json::to_vec(&case["message"]).unwrap();
                validate_bounded_envelope(&raw)
                    .unwrap_or_else(|error| panic!("{}: {error:?}", case["id"]));
                count += 1;
            }
        }
        assert_eq!(count, 54); // Some message shapes have multiple valid vectors.
        for raw in [
            br#"{"type":"hello","type":"hello"}"#.as_slice(),
            br#"{"type":"hello","\u0074ype":"hello"}"#,
            br#"{"type":"hello","request_id":"\ud800","supported":[]}"#,
        ] {
            assert_eq!(
                validate_bounded_envelope(raw).unwrap_err(),
                WireError::InvalidJson
            );
        }
    }
}
