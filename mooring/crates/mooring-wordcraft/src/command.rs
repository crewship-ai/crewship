//! Application payload grammar only. Parsing grants no authority, does not
//! qualify read observations, resolve an anchor or mutate any state. The first
//! command path is for confirmed-read agents; human proposals must preserve
//! their actual Yrs updates and authored IDs, not reexecute these commands.
use std::collections::BTreeMap;

use mooring_protocol::Counter;
use serde::Deserialize;
use serde_json::{Map, Value};

#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Association {
    Before,
    After,
}

#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub enum Anchor {
    Start {},
    End {},
    Relative {
        client_id: Counter,
        clock: Counter,
        assoc: Association,
    },
}

/// A complete native inline property object for newly inserted text. Missing
/// optional properties on Insert means inherit the current resolved format;
/// an explicit empty object is distinct from that inherited selection format.
pub type CharacterProperties = BTreeMap<String, bool>;
/// Missing keys are unchanged; false is explicit false; null removes the native
/// override and returns that property to paragraph/style inheritance.
pub type CharacterPatch = BTreeMap<String, Option<bool>>;

#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(tag = "kind", deny_unknown_fields)]
pub enum Command {
    #[serde(rename = "text.insert")]
    Insert {
        target_id: String,
        at: Anchor,
        text: String,
        properties: Option<CharacterProperties>,
    },
    #[serde(rename = "text.delete")]
    Delete {
        target_id: String,
        start: Anchor,
        end: Anchor,
    },
    #[serde(rename = "text.format")]
    Format {
        target_id: String,
        start: Anchor,
        end: Anchor,
        properties: CharacterPatch,
    },
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum CommandError {
    Syntax,
    Target,
    Anchor,
    Properties,
    Size,
    UnsupportedText,
}

/// The caller must supply the binary64-normalized payload covered by the
/// original immutable fingerprint. Raw JSON duplicate checking happens earlier.
pub fn decode(payload: &Map<String, Value>) -> Result<Command, CommandError> {
    let command: Command =
        serde_json::from_value(Value::Object(payload.clone())).map_err(|_| CommandError::Syntax)?;
    validate(&command)?;
    Ok(command)
}

pub(crate) fn validate(command: &Command) -> Result<(), CommandError> {
    match command {
        Command::Insert {
            target_id,
            at,
            text,
            properties,
        } => {
            target(target_id)?;
            anchor(at)?;
            if text.is_empty() || text.len() > 65_536 {
                return Err(CommandError::Size);
            }
            if text.contains('\u{fffc}') {
                return Err(CommandError::UnsupportedText);
            }
            if let Some(properties) = properties {
                property_names(properties.keys())?;
            }
        }
        Command::Delete {
            target_id,
            start,
            end,
        } => {
            target(target_id)?;
            anchor(start)?;
            anchor(end)?;
        }
        Command::Format {
            target_id,
            start,
            end,
            properties,
        } => {
            target(target_id)?;
            anchor(start)?;
            anchor(end)?;
            if properties.is_empty() {
                return Err(CommandError::Properties);
            }
            property_names(properties.keys())?;
        }
    }
    Ok(())
}

fn target(id: &str) -> Result<(), CommandError> {
    super::validate_ids(&[id.to_owned()], 1).map_err(|_| CommandError::Target)
}
fn anchor(value: &Anchor) -> Result<(), CommandError> {
    if let Anchor::Relative {
        client_id, clock, ..
    } = value
    {
        // ClientID::new has a debug assertion for >53 bits, and clocks are
        // u32. Check before ever constructing upstream Yrs identifiers.
        if client_id.0 >= (1u64 << 53) || clock.0 > u32::MAX.into() {
            return Err(CommandError::Anchor);
        }
    }
    Ok(())
}
fn property_names<'a>(names: impl Iterator<Item = &'a String>) -> Result<(), CommandError> {
    if names
        .into_iter()
        .any(|name| !super::CHARACTER_FLAGS.contains(&name.as_str()))
    {
        return Err(CommandError::Properties);
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    fn decode_value(value: Value) -> Result<Command, CommandError> {
        decode(value.as_object().unwrap())
    }
    #[test]
    fn complete_and_patch_properties_preserve_false_null_and_absence() {
        let insert = decode_value(json!({"kind":"text.insert","target_id":"p_stable","at":{"kind":"start"},"text":"Č😀","properties":{"bold":false,"italicCs":true}})).unwrap();
        let Command::Insert {
            properties: Some(properties),
            ..
        } = insert
        else {
            panic!("wrong command")
        };
        assert_eq!(properties.get("bold"), Some(&false));
        assert_eq!(properties.get("italicCs"), Some(&true));
        assert!(!properties.contains_key("italic"));
        let format = decode_value(json!({"kind":"text.format","target_id":"p_stable","start":{"kind":"start"},"end":{"kind":"end"},"properties":{"bold":false,"italic":null}})).unwrap();
        let Command::Format { properties, .. } = format else {
            panic!("wrong command")
        };
        assert_eq!(properties.get("bold"), Some(&Some(false)));
        assert_eq!(properties.get("italic"), Some(&None));
        assert!(!properties.contains_key("boldCs"));
        for value in [json!({}), json!({"bold":false})] {
            let command=decode_value(json!({"kind":"text.insert","target_id":"p_stable","at":{"kind":"end"},"text":"x","properties":value})).unwrap();
            assert!(matches!(
                command,
                Command::Insert {
                    properties: Some(_),
                    ..
                }
            ));
        }
        assert!(matches!(
            decode_value(
                json!({"kind":"text.insert","target_id":"p_stable","at":{"kind":"end"},"text":"x"})
            )
            .unwrap(),
            Command::Insert {
                properties: None,
                ..
            }
        ));
    }
    #[test]
    fn relative_ids_are_lossless_canonical_decimal_and_bounded_before_yrs() {
        let valid = json!({"kind":"text.delete","target_id":"p_stable","start":{"kind":"relative","client_id":"9007199254740991","clock":"4294967295","assoc":"before"},"end":{"kind":"end"}});
        assert!(decode_value(valid.clone()).is_ok());
        for (field, value) in [
            ("client_id", json!("9007199254740992")),
            ("clock", json!("4294967296")),
            ("client_id", json!(1)),
            ("clock", json!("01")),
            ("assoc", json!("arbitrary")),
        ] {
            let mut invalid = valid.clone();
            invalid["start"][field] = value;
            assert!(decode_value(invalid).is_err());
        }
    }
    #[test]
    fn unknown_fields_identity_claims_absolute_positions_and_human_updates_are_refused() {
        let valid =
            json!({"kind":"text.insert","target_id":"p_stable","at":{"kind":"start"},"text":"x"});
        assert!(decode_value(valid.clone()).is_ok());
        for field in [
            "author",
            "participant_ref",
            "workspace_id",
            "read_preconditions",
            "permission_version",
            "offset_utf16",
        ] {
            let mut invalid = valid.clone();
            invalid[field] = json!("forged");
            assert!(decode_value(invalid).is_err());
        }
        let mut invalid = valid;
        invalid["at"]["offset_utf16"] = json!(1);
        assert!(decode_value(invalid).is_err());
        assert!(decode_value(json!({"kind":"crdt.update","update":"AA=="})).is_err());
    }
    #[test]
    fn invalid_targets_properties_and_oversized_insert_are_refused() {
        for target in ["", "wordcraft:document-structure", "../other", "p\u{0000}x"] {
            assert!(decode_value(
                json!({"kind":"text.insert","target_id":target,"at":{"kind":"start"},"text":"x"})
            )
            .is_err());
        }
        for props in [json!({}), json!({"unknown":true}), json!({"bold":"true"})] {
            assert!(decode_value(json!({"kind":"text.format","target_id":"p_stable","start":{"kind":"start"},"end":{"kind":"end"},"properties":props})).is_err());
        }
        for text in [String::new(), "x".repeat(65537), "\u{fffc}".to_string()] {
            assert!(decode_value(json!({"kind":"text.insert","target_id":"p_stable","at":{"kind":"start"},"text":text})).is_err());
        }
    }
}
