//! Pure host-boundary checks. Transport provenance is supplied by the caller,
//! never inferred from a JSON issuer, actor, or participant field.
use crate::{canonical::CanonicalValue, wire, Counter, Scope};
use serde_json::Value;
use std::collections::{BTreeMap, BTreeSet};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Durability {
    Node,
    Replicated,
}
impl Durability {
    fn wire(self) -> &'static str {
        match self {
            Self::Node => "node",
            Self::Replicated => "replicated",
        }
    }
}

/// Non-deserializable assertion from trusted integration code. This value is
/// not cryptographic evidence: only verified host admission/private transport
/// may construct AuthenticatedHost. No network decoder constructs it.
pub enum Transport<'a> {
    AuthenticatedHost {
        issuer: &'a str,
        durabilities: &'a [Durability],
        required: Durability,
    },
    Participant,
    Runtime,
}

/// Exact immutable membership retained when the request was sent. Batch order
/// is operational; operation order within each atomic change is immutable.
pub struct PendingAtomic {
    pub change_id: String,
    pub op_ids: Vec<String>,
}
pub struct CommitBinding<'a> {
    pub request_id: &'a str,
    pub scope: &'a Scope,
    pub generation: Counter,
    pub changes: &'a [PendingAtomic],
}

#[derive(Debug, PartialEq, Eq)]
pub enum BoundaryError {
    Wire(wire::WireError),
    Forbidden,
    Binding,
    Durability,
    Receipt,
    Encoding,
    Capacity,
    StaleOwner,
    PermissionRevoked,
    VersionMismatch,
    Expired,
    Clock,
}

/// A checked reply from caller-attested host transport. This does not write a
/// ledger, create a receipt, acknowledge new work, or establish a live service.
#[derive(Debug)]
pub struct HostReceipt {
    normalized: CanonicalValue,
    revision: Counter,
}
impl HostReceipt {
    pub fn value(&self) -> &Value {
        self.normalized.value()
    }
    pub fn bytes(&self) -> &[u8] {
        self.normalized.bytes()
    }
    pub fn revision(&self) -> Counter {
        self.revision
    }
}

/// Validate a host CommitResult against the pending request's exact membership.
/// Failed checks return no checked receipt and cannot mutate caller state.
pub fn validate_commit_result(
    raw: &[u8],
    transport: &Transport<'_>,
    expected: &CommitBinding<'_>,
) -> Result<HostReceipt, BoundaryError> {
    let Transport::AuthenticatedHost {
        issuer,
        durabilities,
        required,
    } = transport
    else {
        return Err(BoundaryError::Forbidden);
    };
    let normalized = wire::validate_bounded_envelope(raw).map_err(BoundaryError::Wire)?;
    let value = normalized.value();
    if value["type"] != "commit_result"
        || value["request_id"] != expected.request_id
        || value["scope"]
            != serde_json::to_value(expected.scope).map_err(|_| BoundaryError::Binding)?
        || counter(&value["generation"])? != expected.generation.0
    {
        return Err(BoundaryError::Binding);
    }
    if !wire::valid_identifier(issuer) || value["issuer"] != *issuer {
        return Err(BoundaryError::Forbidden);
    }
    let outer = value["durability"]
        .as_str()
        .ok_or(BoundaryError::Durability)?;
    if !durabilities.iter().any(|mode| mode.wire() == outer) {
        return Err(BoundaryError::Durability);
    }
    let revision = counter(&value["revision"])?;
    let changes = value["changes"].as_array().ok_or(BoundaryError::Receipt)?;
    if expected.changes.is_empty()
        || expected.changes.len() > 256
        || changes.len() != expected.changes.len()
    {
        return Err(BoundaryError::Binding);
    }
    let mut pending = BTreeMap::new();
    let mut expected_ops = BTreeSet::new();
    for change in expected.changes {
        if !wire::valid_identifier(&change.change_id)
            || change.op_ids.is_empty()
            || change.op_ids.len() > 256
            || pending.insert(change.change_id.as_str(), change).is_some()
        {
            return Err(BoundaryError::Binding);
        }
        for op_id in &change.op_ids {
            if !wire::valid_identifier(op_id)
                || !expected_ops.insert(op_id.as_str())
                || expected_ops.len() > 256
            {
                return Err(BoundaryError::Binding);
            }
        }
    }
    let mut seen_changes = BTreeSet::new();
    let mut seen_ops = BTreeSet::new();
    let mut ranges = Vec::new();
    for change in changes {
        let id = change["change_id"].as_str().ok_or(BoundaryError::Receipt)?;
        let membership = pending.get(id).ok_or(BoundaryError::Binding)?;
        if !seen_changes.insert(id) {
            return Err(BoundaryError::Receipt);
        }
        let operations = change["operations"]
            .as_array()
            .ok_or(BoundaryError::Receipt)?;
        if operations.len() != membership.op_ids.len() {
            return Err(BoundaryError::Binding);
        }
        let saved = change["status"] == "saved";
        let first = if saved {
            counter(&change["first_revision"])?
        } else {
            0
        };
        let last = if saved {
            counter(&change["last_revision"])?
        } else {
            0
        };
        if saved {
            let count = operations.len() as u64;
            if first == 0 || first.checked_add(count - 1) != Some(last) || last > revision {
                return Err(BoundaryError::Receipt);
            }
            ranges.push((first, last));
        }
        for (index, operation) in operations.iter().enumerate() {
            let op_id = operation["op_id"].as_str().ok_or(BoundaryError::Receipt)?;
            if op_id != membership.op_ids[index] {
                return Err(BoundaryError::Binding);
            }
            if !seen_ops.insert(op_id) {
                return Err(BoundaryError::Receipt);
            }
            if saved {
                if operation["status"] != "saved"
                    || counter(&operation["revision"])?
                        != first
                            .checked_add(index as u64)
                            .ok_or(BoundaryError::Receipt)?
                {
                    return Err(BoundaryError::Receipt);
                }
                if operation["issuer"] != *issuer {
                    return Err(BoundaryError::Forbidden);
                }
                let mode = operation["durability"]
                    .as_str()
                    .ok_or(BoundaryError::Durability)?;
                if !durabilities.iter().any(|allowed| allowed.wire() == mode)
                    || (outer == "replicated" && mode != "replicated")
                {
                    return Err(BoundaryError::Durability);
                }
            } else {
                rejection(&operation["reason"])?;
            }
        }
    }
    ranges.sort_unstable();
    if !ranges.is_empty() && *required == Durability::Replicated && outer != "replicated" {
        return Err(BoundaryError::Durability);
    }
    if ranges.windows(2).any(|pair| pair[0].1 >= pair[1].0) {
        return Err(BoundaryError::Receipt);
    }
    Ok(HostReceipt {
        normalized,
        revision: Counter(revision),
    })
}

fn counter(value: &Value) -> Result<u64, BoundaryError> {
    value
        .as_str()
        .and_then(|text| text.parse().ok())
        .ok_or(BoundaryError::Receipt)
}
fn rejection(reason: &Value) -> Result<(), BoundaryError> {
    if reason["code"] == "dependency_rejected" {
        if reason["root_op_id"] != reason["root_cause"]["op_id"] {
            return Err(BoundaryError::Receipt);
        }
    } else if reason.get("root_op_id").is_some() || reason.get("root_cause").is_some() {
        return Err(BoundaryError::Receipt);
    }
    Ok(())
}

/// Validate standard padded base64 and unused pad bits, and compute decoded
/// size before any decode allocation. Empty data is valid when the caller
/// permits it; application semantics remain separate.
pub fn canonical_base64_decoded_len(text: &str, maximum: usize) -> Result<usize, BoundaryError> {
    if text.len() % 4 != 0 {
        return Err(BoundaryError::Encoding);
    }
    let body = text.trim_end_matches('=');
    let padding = text.len() - body.len();
    if padding > 2 {
        return Err(BoundaryError::Encoding);
    }
    let size = (text.len() / 4)
        .checked_mul(3)
        .and_then(|size| size.checked_sub(padding))
        .ok_or(BoundaryError::Encoding)?;
    if size > maximum {
        return Err(BoundaryError::Capacity);
    }
    let digit = |byte| match byte {
        b'A'..=b'Z' => Some(byte - b'A'),
        b'a'..=b'z' => Some(byte - b'a' + 26),
        b'0'..=b'9' => Some(byte - b'0' + 52),
        b'+' => Some(62),
        b'/' => Some(63),
        _ => None,
    };
    if body.bytes().any(|byte| digit(byte).is_none()) {
        return Err(BoundaryError::Encoding);
    }
    if padding > 0 {
        let last = body
            .as_bytes()
            .last()
            .copied()
            .and_then(digit)
            .ok_or(BoundaryError::Encoding)?;
        if (padding == 2 && last & 15 != 0) || (padding == 1 && last & 3 != 0) {
            return Err(BoundaryError::Encoding);
        }
    }
    Ok(size)
}
