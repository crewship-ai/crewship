//! Experimental partial wire shapes. Not an authenticated endpoint or a full
//! JSON-schema validator. Numeric, size and cross-field validation is explicit.
use serde::{Deserialize, Serialize};
mod generated;
pub use generated::*;

/// Lossless counter across Go/Rust/TS. The wire representation is a canonical
/// decimal string; the Rust value is validated as u64 on decode.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(try_from = "String", into = "String")]
pub struct Counter(pub u64);
impl TryFrom<String> for Counter {
    type Error = &'static str;
    fn try_from(value: String) -> Result<Self, Self::Error> {
        if value.is_empty()
            || value.len() > 20
            || !value.bytes().all(|b| b.is_ascii_digit())
            || (value.len() > 1 && value.starts_with('0'))
        {
            return Err("counter must be canonical u64 decimal text");
        }
        value
            .parse::<u64>()
            .map(Counter)
            .map_err(|_| "counter exceeds u64")
    }
}
impl From<Counter> for String {
    fn from(value: Counter) -> String {
        value.0.to_string()
    }
}

/// Exact version-tuple negotiation until compatibility between minor/patch
/// versions is demonstrated. No document data is needed to reject old clients.
pub fn negotiate<'a>(
    hello: &Hello,
    supported: &'a [Versions],
) -> Result<&'a Versions, &'static str> {
    if hello.request_id.is_empty()
        || hello.request_id.chars().count() > 200
        || hello.supported.is_empty()
        || hello.supported.len() > 16
    {
        return Err("invalid_request");
    }
    for offered in &hello.supported {
        for version in [
            &offered.protocol_version,
            &offered.model_version,
            &offered.engine_version,
        ] {
            if version.len() > 60
                || version.split('.').count() != 3
                || !version
                    .split('.')
                    .all(|p| !p.is_empty() && p.bytes().all(|b| b.is_ascii_digit()))
            {
                return Err("invalid_request");
            }
        }
    }
    supported
        .iter()
        .find(|candidate| hello.supported.contains(candidate))
        .ok_or("version_mismatch")
}

/// The only valid value for a schema `const: true` marker.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(try_from = "bool", into = "bool")]
pub struct True;
impl TryFrom<bool> for True {
    type Error = &'static str;
    fn try_from(value: bool) -> Result<Self, Self::Error> {
        if value {
            Ok(Self)
        } else {
            Err("expected true")
        }
    }
}
impl From<True> for bool {
    fn from(_: True) -> bool {
        true
    }
}
