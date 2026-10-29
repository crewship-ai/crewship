//! Bounded JCS input normalization and semantic identity. No host authority.
use crate::{AtomicChange, Scope};
use serde::{
    de::{self, MapAccess, SeqAccess, Visitor},
    Deserialize, Deserializer,
};
use serde_json::{json, Map, Number, Value};
use sha2::{Digest, Sha256};
use std::{collections::BTreeSet, fmt};

pub const MAX_CANONICAL_BYTES: usize = 262_144;
#[derive(Debug, PartialEq, Eq)]
pub enum CanonicalError {
    Size,
    InvalidJson,
    Canonicalization,
    InvalidAtomic,
}

/// Both views describe the same binary64-normalized value. Adapters must use
/// `value()` (or deserialize `bytes()`), never the original incoming JSON.
#[derive(Debug)]
pub struct CanonicalValue {
    bytes: Vec<u8>,
    value: Value,
}
impl CanonicalValue {
    pub fn bytes(&self) -> &[u8] {
        &self.bytes
    }
    pub fn value(&self) -> &Value {
        &self.value
    }
    pub fn digest(&self) -> String {
        digest(&self.bytes)
    }
}
pub(crate) fn digest(bytes: &[u8]) -> String {
    format!("sha256:{:x}", Sha256::digest(bytes))
}

// serde_json::Value alone silently overwrites duplicate properties. Decode
// recursively ourselves before any canonicalization or adapter validation.
struct Strict(Value);
impl<'de> Deserialize<'de> for Strict {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct StrictVisitor;
        impl<'de> Visitor<'de> for StrictVisitor {
            type Value = Strict;
            fn expecting(&self, formatter: &mut fmt::Formatter) -> fmt::Result {
                formatter.write_str("unambiguous finite JSON")
            }
            fn visit_unit<E: de::Error>(self) -> Result<Strict, E> {
                Ok(Strict(Value::Null))
            }
            fn visit_bool<E: de::Error>(self, value: bool) -> Result<Strict, E> {
                Ok(Strict(Value::Bool(value)))
            }
            fn visit_str<E: de::Error>(self, value: &str) -> Result<Strict, E> {
                Ok(Strict(Value::String(value.into())))
            }
            fn visit_string<E: de::Error>(self, value: String) -> Result<Strict, E> {
                Ok(Strict(Value::String(value)))
            }
            fn visit_i64<E: de::Error>(self, value: i64) -> Result<Strict, E> {
                Ok(Strict(Value::Number(value.into())))
            }
            fn visit_u64<E: de::Error>(self, value: u64) -> Result<Strict, E> {
                Ok(Strict(Value::Number(value.into())))
            }
            fn visit_f64<E: de::Error>(self, value: f64) -> Result<Strict, E> {
                Number::from_f64(value)
                    .map(|n| Strict(Value::Number(n)))
                    .ok_or_else(|| E::custom("nonfinite number"))
            }
            fn visit_seq<A: SeqAccess<'de>>(self, mut seq: A) -> Result<Strict, A::Error> {
                let mut values = Vec::new();
                while let Some(Strict(value)) = seq.next_element()? {
                    values.push(value);
                }
                Ok(Strict(Value::Array(values)))
            }
            fn visit_map<A: MapAccess<'de>>(self, mut map: A) -> Result<Strict, A::Error> {
                let mut values = Map::new();
                while let Some(key) = map.next_key::<String>()? {
                    if values.contains_key(&key) {
                        return Err(de::Error::custom("duplicate property"));
                    }
                    let Strict(value) = map.next_value()?;
                    values.insert(key, value);
                }
                Ok(Strict(Value::Object(values)))
            }
        }
        deserializer.deserialize_any(StrictVisitor)
    }
}

/// Strict raw JSON -> JCS bytes -> reparsed normalized adapter value. Retains
/// serde_json's nesting bound and rejects duplicate keys, invalid UTF-8/lone
/// surrogates, trailing data and nonfinite/out-of-range binary64 numbers.
pub fn normalize(raw: &[u8]) -> Result<CanonicalValue, CanonicalError> {
    normalize_with_limit(raw, MAX_CANONICAL_BYTES)
}

// Crate-private so a payload cannot choose arbitrary limits. Only a separately
// negotiated inherited-control profile may use a larger bound than public JCS.
pub(crate) fn normalize_with_limit(
    raw: &[u8],
    maximum: usize,
) -> Result<CanonicalValue, CanonicalError> {
    if raw.is_empty() || raw.len() > maximum {
        return Err(CanonicalError::Size);
    }
    let Strict(value) =
        serde_json::from_slice::<Strict>(raw).map_err(|_| CanonicalError::InvalidJson)?;
    let bytes =
        serde_json_canonicalizer::to_vec(&value).map_err(|_| CanonicalError::Canonicalization)?;
    if bytes.len() > maximum {
        return Err(CanonicalError::Size);
    }
    let value = serde_json::from_slice(&bytes).map_err(|_| CanonicalError::Canonicalization)?;
    Ok(CanonicalValue { bytes, value })
}

#[derive(Debug)]
pub struct FingerprintedChange {
    normalized: AtomicChange,
    membership_hash: String,
    operation_fingerprints: Vec<String>,
}
impl FingerprintedChange {
    /// Adapter input contains normalized numbers, including nested payloads.
    pub fn change(&self) -> &AtomicChange {
        &self.normalized
    }
    pub fn membership_hash(&self) -> &str {
        &self.membership_hash
    }
    pub fn operation_fingerprints(&self) -> &[String] {
        &self.operation_fingerprints
    }
    pub fn matches_wire(&self) -> bool {
        self.normalized.membership_hash == self.membership_hash
            && self
                .normalized
                .operations
                .iter()
                .zip(&self.operation_fingerprints)
                .all(|(op, expected)| op.fingerprint == *expected)
    }
    /// Client construction only. A host must use matches_wire(), and must bind
    /// every participant_ref/scope to authenticated authority before applying.
    pub fn with_fingerprints(mut self) -> AtomicChange {
        self.normalized.membership_hash = self.membership_hash;
        for (operation, fingerprint) in self
            .normalized
            .operations
            .iter_mut()
            .zip(self.operation_fingerprints)
        {
            operation.fingerprint = fingerprint;
        }
        self.normalized
    }
}

/// Full ordered membership, task and authorship affect stable operation IDs;
/// transport request/batch IDs, owner generations and delivery gates do not.
/// This is identity computation, not authentication or full schema validation.
pub fn fingerprint_atomic(
    scope: &Scope,
    raw_change: &[u8],
) -> Result<FingerprintedChange, CanonicalError> {
    let value = normalize(raw_change)?;
    let normalized: AtomicChange =
        serde_json::from_value(value.value).map_err(|_| CanonicalError::InvalidAtomic)?;
    let valid_id = crate::wire::valid_identifier;
    if !valid_id(&scope.application_id)
        || !valid_id(&scope.workspace_id)
        || !valid_id(&scope.artifact_id)
        || !valid_id(&normalized.change_id)
        || normalized.task_id.as_ref().is_some_and(|id| !valid_id(id))
        || normalized.operations.is_empty()
        || normalized.operations.len() > 256
    {
        return Err(CanonicalError::InvalidAtomic);
    }
    let mut ids = BTreeSet::new();
    let mut operations = Vec::new();
    for operation in &normalized.operations {
        if !valid_id(&operation.op_id) || !ids.insert(&operation.op_id) {
            return Err(CanonicalError::InvalidAtomic);
        }
        let mut value =
            serde_json::to_value(operation).map_err(|_| CanonicalError::InvalidAtomic)?;
        value
            .as_object_mut()
            .ok_or(CanonicalError::InvalidAtomic)?
            .remove("fingerprint");
        operations.push(value);
    }
    let mut preimage = json!({"domain":"mooring.atomic.v0.4","scope":scope,"change_id":normalized.change_id,"operations_without_fingerprints":operations});
    if let Some(task) = &normalized.task_id {
        preimage["task_id"] = json!(task);
    }
    let preimage = serde_json::to_vec(&preimage).map_err(|_| CanonicalError::InvalidAtomic)?;
    let membership_hash = normalize(&preimage)?.digest();
    let mut operation_fingerprints = Vec::with_capacity(normalized.operations.len());
    for operation in &normalized.operations {
        let preimage=serde_json::to_vec(&json!({"domain":"mooring.operation.v0.4","membership_hash":membership_hash,"op_id":operation.op_id})).map_err(|_|CanonicalError::InvalidAtomic)?;
        operation_fingerprints.push(normalize(&preimage)?.digest());
    }
    Ok(FingerprintedChange {
        normalized,
        membership_hash,
        operation_fingerprints,
    })
}
