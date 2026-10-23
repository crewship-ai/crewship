//! Model-v2 action-scoped property identities. This module grants no authority.
use crate::{AdapterError, CHARACTER_FLAGS, CHARACTER_PREFIX, PROPS_PRESENT};
use mooring_protocol::Counter;
use mooring_text::yrs::{types::Attrs, Any};
use serde_json::{Map, Value};
use std::collections::BTreeMap;

pub(crate) const PREFIX: &str = "wc2:";
pub(crate) const MAX_STAMP: u64 = (1 << 53) - 1;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Model {
    LegacyV1,
    V2,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Key<'a> {
    pub field: &'a str,
    pub stamp: u64,
    pub origin: u64,
}

pub(crate) fn parse_key(raw: &str) -> Result<Key<'_>, AdapterError> {
    if raw.len() > 48 {
        return Err(AdapterError::InvalidCheckpoint);
    }
    let mut parts = raw
        .strip_prefix(PREFIX)
        .ok_or(AdapterError::InvalidCheckpoint)?
        .split(':');
    let field = parts.next().ok_or(AdapterError::InvalidCheckpoint)?;
    if field != "empty" && !CHARACTER_FLAGS.contains(&field) {
        return Err(AdapterError::InvalidCheckpoint);
    }
    let stamp = parts
        .next()
        .and_then(|s| Counter::try_from(s.to_owned()).ok())
        .ok_or(AdapterError::InvalidCheckpoint)?
        .0;
    let origin = parts
        .next()
        .and_then(|s| Counter::try_from(s.to_owned()).ok())
        .ok_or(AdapterError::InvalidCheckpoint)?
        .0;
    if parts.next().is_some()
        || stamp == 0
        || stamp > MAX_STAMP
        || origin == 0
        || origin > MAX_STAMP
    {
        return Err(AdapterError::InvalidCheckpoint);
    }
    Ok(Key {
        field,
        stamp,
        origin,
    })
}

/// Produces exactly native properties, never CRDT keys or clock metadata.
/// Empty props:{} is an independent native intent; nonempty properties derive
/// from boolean flag winners. Inherit differs from explicit false.
pub(crate) fn properties(attrs: &Attrs, model: Model) -> Result<Option<Value>, AdapterError> {
    let mut flags: BTreeMap<&str, bool> = BTreeMap::new();
    let mut winners: BTreeMap<&str, ((u64, u64), &Any)> = BTreeMap::new();
    let mut present = false;
    for (key, value) in attrs.iter() {
        if key.as_ref() == PROPS_PRESENT {
            if !matches!(value, Any::Bool(true)) {
                return Err(AdapterError::InvalidCheckpoint);
            }
            present = true;
        } else if let Some(flag) = key.strip_prefix(CHARACTER_PREFIX) {
            if !CHARACTER_FLAGS.contains(&flag) {
                return Err(AdapterError::InvalidCheckpoint);
            }
            let Any::Bool(value) = value else {
                return Err(AdapterError::InvalidCheckpoint);
            };
            flags.insert(flag, *value);
        } else if model == Model::V2 && key.starts_with(PREFIX) {
            let parsed = parse_key(key)?;
            if parsed.field == "empty" {
                if !matches!(value, Any::Bool(_)) {
                    return Err(AdapterError::InvalidCheckpoint);
                }
            } else if !matches!(value, Any::Bool(_))
                && !matches!(value, Any::String(v) if v.as_ref()=="inherit")
            {
                return Err(AdapterError::InvalidCheckpoint);
            }
            let stamp = (parsed.stamp, parsed.origin);
            let winner = winners.entry(parsed.field).or_insert((stamp, value));
            if stamp > winner.0 {
                *winner = (stamp, value);
            }
        } else {
            return Err(AdapterError::InvalidCheckpoint);
        }
    }
    if !flags.is_empty() && !present {
        return Err(AdapterError::InvalidCheckpoint);
    }
    if model == Model::LegacyV1 {
        return Ok(present.then(|| {
            Value::Object(
                flags
                    .into_iter()
                    .map(|(key, value)| (key.into(), Value::Bool(value)))
                    .collect(),
            )
        }));
    }
    let mut explicit_empty = present && flags.is_empty();
    for (field, (_, value)) in winners {
        if field == "empty" {
            let Any::Bool(value) = value else {
                unreachable!()
            };
            explicit_empty = *value;
        } else {
            match value {
                Any::Bool(value) => {
                    flags.insert(field, *value);
                }
                Any::String(value) if value.as_ref() == "inherit" => {
                    flags.remove(field);
                }
                _ => return Err(AdapterError::InvalidCheckpoint),
            }
        }
    }
    let properties: Map<String, Value> = flags
        .into_iter()
        .map(|(key, value)| (key.into(), Value::Bool(value)))
        .collect();
    Ok((explicit_empty || !properties.is_empty()).then_some(Value::Object(properties)))
}

/// Authoritative-current AGENT helper only. Human proposals require a separate
/// qualified causal witness; global frontier clocks must not add dependencies
/// on unrelated optimistic human operations.
pub(crate) fn current_agent_stamp(
    doc: &mooring_text::yrs::Doc,
) -> Result<(u64, u64), AdapterError> {
    use mooring_text::yrs::{ReadTxn, Transact};
    let stamp = doc
        .transact()
        .state_vector()
        .iter()
        .try_fold(1_u64, |sum, (_, clock)| sum.checked_add(*clock as u64))
        .ok_or(AdapterError::Limit)?;
    if stamp > MAX_STAMP {
        return Err(AdapterError::Limit);
    }
    Ok((stamp, doc.client_id().get()))
}
fn key(field: &str, stamp: (u64, u64)) -> std::sync::Arc<str> {
    format!("{PREFIX}{field}:{}:{}", stamp.0, stamp.1).into()
}
pub(crate) fn insertion_attributes(
    properties: &crate::command::CharacterProperties,
    explicit_empty: bool,
    stamp: (u64, u64),
) -> Attrs {
    let mut attrs = Attrs::new();
    attrs.insert(key("empty", stamp), Any::Bool(explicit_empty));
    for field in CHARACTER_FLAGS {
        attrs.insert(
            key(field, stamp),
            properties
                .get(*field)
                .copied()
                .map(Any::Bool)
                .unwrap_or(Any::String("inherit".into())),
        );
    }
    attrs
}
pub(crate) fn format_attributes(
    patch: &crate::command::CharacterPatch,
    stamp: (u64, u64),
) -> Attrs {
    patch
        .iter()
        .map(|(field, value)| {
            (
                key(field, stamp),
                value
                    .map(Any::Bool)
                    .unwrap_or(Any::String("inherit".into())),
            )
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn action_keys_are_closed_canonical_and_bounded() {
        let key = parse_key("wc2:bold:7:9007199254740991").unwrap();
        assert_eq!(
            key,
            Key {
                field: "bold",
                stamp: 7,
                origin: 9_007_199_254_740_991
            }
        );
        for raw in [
            "wc2:unknown:1:17",
            "wc2:bold:01:17",
            "wc2:bold:0:17",
            "wc2:bold:1:0",
            "wc2:bold:1:9007199254740992",
            "wc2:bold:9007199254740992:17",
            "wc2:bold:1:17:extra",
            "wc2:bold:+1:17",
        ] {
            assert!(parse_key(raw).is_err(), "accepted {raw}");
        }
    }
    #[test]
    fn latest_known_action_wins_without_conflating_false_inherit_or_empty() {
        let mut attrs = Attrs::from([
            ("wordcraft.props".into(), Any::Bool(true)),
            ("wordcraft.chr.bold".into(), Any::Bool(true)),
        ]);
        attrs.insert("wc2:bold:1:17".into(), Any::Bool(false));
        attrs.insert("wc2:bold:2:18".into(), Any::String("inherit".into()));
        assert_eq!(properties(&attrs, Model::V2).unwrap(), None);
        attrs.remove("wc2:bold:2:18");
        assert_eq!(
            properties(&attrs, Model::V2).unwrap(),
            Some(serde_json::json!({"bold":false}))
        );
        attrs.insert("wc2:bold:3:17".into(), Any::String("inherit".into()));
        attrs.insert("wc2:empty:3:17".into(), Any::Bool(true));
        assert_eq!(
            properties(&attrs, Model::V2).unwrap(),
            Some(serde_json::json!({}))
        );
        assert!(properties(&attrs, Model::LegacyV1).is_err());
        attrs.insert("wc2:italic:4:17".into(), Any::String("invented".into()));
        assert!(properties(&attrs, Model::V2).is_err());
    }
}
