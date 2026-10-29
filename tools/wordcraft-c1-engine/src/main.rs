//! Private C1 helper, not a user command endpoint. No TCP/HTTP listener.
use serde::Deserialize;
use serde_json::{Value, json};
use std::{
    io::{self, Read, Write},
    os::{fd::FromRawFd, unix::net::UnixStream},
};
use unicode_segmentation::UnicodeSegmentation;
use wordcraft_doc::{Block, Document};
use wordcraft_engine::{Session, io as native_io};
const FRAME: usize = 4 * 1024 * 1024;
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Target {
    block: usize,
    start: usize,
    end: usize,
    unit: String,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Request {
    command: String,
    params: Value,
    native: Option<Value>,
    target: Option<Target>,
    author: String,
    time: String,
}
fn keys(p: &Value, allowed: &[&str]) -> Result<(), &'static str> {
    let p = p.as_object().ok_or("invalid_params")?;
    if p.keys().any(|k| !allowed.contains(&k.as_str())) {
        return Err("invalid_params");
    }
    Ok(())
}
const CATALOG_JSON: &str = include_str!("capabilities.json");
const CATALOG_PROVENANCE: &str = include_str!("catalog-provenance.json");
// BEGIN shared catalog validation: byte-identical in host and private helper.
fn supported_pattern(pattern: &str, text: &str) -> bool {
    match pattern {
        r"^[0-9A-Fa-f]{6}(?![\s\S])" => {
            text.len() == 6 && text.bytes().all(|b| b.is_ascii_hexdigit())
        }
        r"^[\x20-\x7e]{1,2}(?![\s\S])" => {
            (1..=2).contains(&text.len()) && text.bytes().all(|b| (0x20..=0x7e).contains(&b))
        }
        r"^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?(?![\s\S])" => {
            let data = text.trim_end_matches('=');
            text.len() % 4 == 0
                && text.len() - data.len() <= 2
                && data
                    .bytes()
                    .all(|b| b.is_ascii_alphanumeric() || b == b'+' || b == b'/')
        }
        _ => false,
    }
}
fn schema_accepts(schema: &Value, value: &Value, depth: usize) -> bool {
    if depth > 24 {
        return false;
    }
    let Some(s) = schema.as_object() else {
        return false;
    };
    if let Some(choices) = s.get("anyOf") {
        return s.len() == 1
            && choices.as_array().is_some_and(|items| {
                !items.is_empty()
                    && items.len() <= 32
                    && items
                        .iter()
                        .any(|item| schema_accepts(item, value, depth + 1))
            });
    }
    let Some(kind) = s.get("type").and_then(Value::as_str) else {
        return false;
    };
    let allowed: &[&str] = match kind {
        "object" => &["type", "properties", "required", "additionalProperties"],
        "array" => &["type", "items", "minItems", "maxItems"],
        "string" => &["type", "enum", "minLength", "maxLength", "pattern"],
        "number" | "integer" => &["type", "enum", "minimum", "maximum"],
        "boolean" | "null" => &["type", "enum"],
        _ => return false,
    };
    if s.keys().any(|key| !allowed.contains(&key.as_str())) {
        return false;
    }
    if let Some(choices) = s.get("enum") {
        if !choices
            .as_array()
            .is_some_and(|items| items.contains(value))
        {
            return false;
        }
    }
    match kind {
        "object" => {
            let (Some(object), Some(properties)) = (
                value.as_object(),
                s.get("properties").and_then(Value::as_object),
            ) else {
                return false;
            };
            if s.get("additionalProperties") != Some(&Value::Bool(false)) {
                return false;
            }
            if let Some(required) = s.get("required") {
                if !required.as_array().is_some_and(|keys| {
                    keys.iter().all(|key| {
                        key.as_str().is_some_and(|name| {
                            properties.contains_key(name) && object.contains_key(name)
                        })
                    })
                }) {
                    return false;
                }
            }
            object.iter().all(|(key, item)| {
                properties
                    .get(key)
                    .is_some_and(|rule| schema_accepts(rule, item, depth + 1))
            })
        }
        "array" => value.as_array().is_some_and(|items| {
            ["minItems", "maxItems"].iter().all(|key| {
                !s.contains_key(*key)
                    || s[*key].as_u64().is_some_and(|limit| {
                        if *key == "minItems" {
                            items.len() as u64 >= limit
                        } else {
                            items.len() as u64 <= limit
                        }
                    })
            }) && s.get("items").is_some_and(|rule| {
                items
                    .iter()
                    .all(|item| schema_accepts(rule, item, depth + 1))
            })
        }),
        "string" => value.as_str().is_some_and(|text| {
            let count = text.chars().count() as u64;
            ["minLength", "maxLength"].iter().all(|key| {
                !s.contains_key(*key)
                    || s[*key].as_u64().is_some_and(|limit| {
                        if *key == "minLength" {
                            count >= limit
                        } else {
                            count <= limit
                        }
                    })
            }) && (!s.contains_key("pattern")
                || s["pattern"]
                    .as_str()
                    .is_some_and(|pattern| supported_pattern(pattern, text)))
        }),
        "number" | "integer" => value.as_f64().is_some_and(|n| {
            n.is_finite()
                && (kind != "integer" || n.fract() == 0.0)
                && ["minimum", "maximum"].iter().all(|key| {
                    !s.contains_key(*key)
                        || s[*key].as_f64().is_some_and(|bound| {
                            bound.is_finite()
                                && if *key == "minimum" {
                                    n >= bound
                                } else {
                                    n <= bound
                                }
                        })
                })
        }),
        "boolean" => value.is_boolean(),
        "null" => value.is_null(),
        _ => false,
    }
}
fn catalog() -> &'static Value {
    static CATALOG: std::sync::OnceLock<Value> = std::sync::OnceLock::new();
    CATALOG.get_or_init(|| serde_json::from_str(CATALOG_JSON).expect("generated pinned catalog"))
}
fn catalog_entry(command: &str) -> Option<&'static Value> {
    let catalog = catalog();
    if catalog["engine"]["sha"] != "7ef2633a7e84956522d1ca47b99fe6feef16c1db"
        || catalog["schemaVersion"] != 1
    {
        return None;
    }
    let entries = catalog["commands"].as_array()?;
    let mut matches = entries
        .iter()
        .filter(|entry| entry["id"].as_str() == Some(command));
    let entry = matches.next()?;
    if matches.next().is_some() {
        return None;
    }
    Some(entry)
}
/// Explicit default-off product test profile: only the generated 72 isolated
/// fixture-qualified IDs. Product qualification flags remain unchanged. Current
/// participant authority and every existing stage/commit guard remain mandatory.
fn validate_catalog_candidate(
    command: &str,
    params: &Value,
) -> std::result::Result<(), &'static str> {
    let entry = catalog_entry(command).ok_or("command_denied")?;
    if entry["engineQualification"]["status"] != "qualified"
        || entry["engineQualification"]["scope"] != "isolated-c1-fixture"
        || entry["permanentlyForbidden"] != false
        || entry["schemaStatus"] != "exact"
        || entry["effect"] != "document-candidate"
        || !matches!(
            entry["group"].as_str(),
            Some("text-enter" | "character-format" | "paragraphs" | "lists")
        )
    {
        return Err("command_denied");
    }
    if !schema_accepts(&entry["parameterSchema"], params, 0) {
        return Err("invalid_params");
    }
    // Structural effect guard retained: multi-paragraph insertion has no reviewed
    // stable-ID mapping. Enter remains the explicit structural operation below.
    if command == "text.insert"
        && params["text"].as_str().is_some_and(|text| {
            text.is_empty() || text.len() > 32768 || text.contains(['\n', '\r', '\0', '\u{fffc}'])
        })
    {
        return Err("invalid_text");
    }
    Ok(())
}
fn catalog_identity() -> Value {
    let provenance: Value =
        serde_json::from_str(CATALOG_PROVENANCE).expect("generated catalog provenance");
    json!(format!(
        "wordcraft-c1-qualified72-v5:{}",
        provenance["catalogSha256"].as_str().expect("catalog SHA")
    ))
}
// END shared catalog validation.
fn run(raw: &[u8]) -> Result<Value, &'static str> {
    let r: Request = serde_json::from_slice(raw).map_err(|_| "invalid_request")?;
    let doc = if let Some(native) = r.native {
        native_io::open_bytes(
            "state.json",
            &serde_json::to_vec(&native).map_err(|_| "invalid_document")?,
        )
        .map_err(|_| "invalid_document")?
    } else {
        Document::new()
    };
    let mut s = Session::new(doc);
    let _layout = s.layout(); // Cold M2 load+first layout is deliberately measured.
    let (engine, params) = match r.command.as_str() {
        "init" | "document.read" => {
            keys(&r.params, &[])?;
            if r.target.is_some() {
                return Err("invalid_target");
            }
            ("document.text", json!({}))
        }
        _ => {
            validate_catalog_candidate(&r.command, &r.params)?;
            (r.command.as_str(), r.params.clone())
        }
    };
    if !matches!(r.command.as_str(), "init" | "document.read") {
        let t = r.target.ok_or("invalid_target")?;
        let Some(Block::Para(p)) = s.doc.body.get(t.block).map(|b| &**b) else {
            return Err("invalid_target");
        };
        let boundaries = p
            .text
            .grapheme_indices(true)
            .map(|(n, _)| n)
            .chain(std::iter::once(p.text.len()))
            .collect::<Vec<_>>();
        if t.unit != "utf8_bytes"
            || t.start > t.end
            || !boundaries.contains(&t.start)
            || !boundaries.contains(&t.end)
        {
            return Err("invalid_target");
        }
        s.run_with_host_context("select.range",&json!({"anchor":{"block":t.block,"off":t.start},"focus":{"block":t.block,"off":t.end}}),&r.author,&r.time).map_err(|_|"invalid_clock")?;
    }
    let command_result = s
        .run_with_host_context(engine, &params, &r.author, &r.time)
        .map_err(|_| "engine_refused")?;
    let _layout = s.layout();
    // Commands may return extra metadata (list.define adds num). The receipt
    // carries the actual same-command native selection, never a guessed caret
    // or a relaxed interpretation of an arbitrary command return value.
    let result = if matches!(r.command.as_str(), "init" | "document.read") {
        command_result
    } else {
        serde_json::to_value(&s.sel).map_err(|_| "engine_selection")?
    };
    let native: Value = serde_json::from_slice(
        &native_io::save_bytes("state.json", &s.doc).map_err(|_| "invalid_document")?,
    )
    .map_err(|_| "invalid_document")?;
    let source: Value =
        serde_json::from_str(include_str!("../source.json")).map_err(|_| "source_identity")?;
    Ok(
        json!({"native":native,"result":result,"engine_identity":{"revision":source["rev"],"patch_sha256":source["patch_sha256"],"catalog":catalog_identity()}}),
    )
}
fn main() -> io::Result<()> {
    let fd = std::env::var("MOORING_C1_ENGINE_FD")
        .ok()
        .and_then(|s| s.parse::<i32>().ok())
        .filter(|fd| *fd >= 3)
        .ok_or_else(|| io::Error::other("private engine FD required"))?;
    let mut stream = unsafe { UnixStream::from_raw_fd(fd) };
    stream.peer_addr()?;
    if unsafe { libc::fcntl(fd, libc::F_SETFD, libc::FD_CLOEXEC) } < 0 {
        return Err(io::Error::last_os_error());
    }
    loop {
        let mut head = [0; 4];
        match stream.read_exact(&mut head) {
            Ok(()) => {}
            Err(e) if e.kind() == io::ErrorKind::UnexpectedEof => return Ok(()),
            Err(e) => return Err(e),
        }
        let n = u32::from_be_bytes(head) as usize;
        if n == 0 || n > FRAME {
            return Err(io::Error::other("frame limit"));
        }
        let mut raw = vec![0; n];
        stream.read_exact(&mut raw)?;
        let reply = match run(&raw) {
            Ok(value) => json!({"ok":true,"value":value}),
            Err(reason) => json!({"ok":false,"reason":reason}),
        };
        let raw = serde_json::to_vec(&reply)?;
        if raw.len() > FRAME {
            return Err(io::Error::other("reply limit"));
        }
        stream.write_all(&(raw.len() as u32).to_be_bytes())?;
        stream.write_all(&raw)?;
    }
}

#[cfg(test)]
const CATALOG_TEST_VECTORS: &str = include_str!("catalog-validation-vectors.json");

#[cfg(test)]
mod catalog_validation_tests {
    use super::*;
    #[test]
    fn all_reviewed_profile_examples_match_on_both_sides() {
        let vectors: Value = serde_json::from_str(CATALOG_TEST_VECTORS).unwrap();
        assert_eq!(vectors.as_array().unwrap().len(), 44);
        for vector in vectors.as_array().unwrap() {
            let entry = catalog_entry(vector["id"].as_str().unwrap()).unwrap();
            for params in vector["valid"].as_array().unwrap() {
                assert!(
                    schema_accepts(&entry["parameterSchema"], params, 0),
                    "valid {}: {}",
                    vector["id"],
                    params
                );
                let mut unknown = params.clone();
                unknown
                    .as_object_mut()
                    .unwrap()
                    .insert("unexpected".into(), json!(true));
                assert!(!schema_accepts(&entry["parameterSchema"], &unknown, 0));
            }
            for params in vector["invalid"].as_array().unwrap() {
                assert!(
                    !schema_accepts(&entry["parameterSchema"], params, 0),
                    "invalid {}: {}",
                    vector["id"],
                    params
                );
            }
        }
    }
    #[test]
    fn catalog_filters_effects_and_closed_parameters() {
        for command in [
            "file.open",
            "font.add",
            "tools.macros",
            "review.addToDictionary",
            "caret.set",
            "edit.undo",
            "unknown.command",
        ] {
            assert_eq!(
                validate_catalog_candidate(command, &json!({})),
                Err("command_denied")
            );
        }
        assert!(validate_catalog_candidate("para.bullets", &json!({"kind":"bullet"})).is_ok());
        assert!(validate_catalog_candidate("format.italic", &json!({"value":true})).is_ok());
        assert!(
            validate_catalog_candidate(
                "format.italic",
                &json!({"value":true,"command":"file.open"})
            )
            .is_err()
        );
        assert!(validate_catalog_candidate("text.insert", &json!({"text":"a\nb"})).is_err());
        assert!(
            catalog()["commands"]
                .as_array()
                .unwrap()
                .iter()
                .all(|entry| entry["qualification"] == "unqualified")
        );
    }
    #[test]
    fn schema_combinations_and_unknown_rules_fail_closed() {
        assert!(!schema_accepts(
            &json!({"type":"number","enum":[1],"minimum":2}),
            &json!(1),
            0
        ));
        assert!(!schema_accepts(
            &json!({"type":"number","oneOf":[]}),
            &json!(1),
            0
        ));
        assert!(!schema_accepts(
            &json!({"anyOf":[{"type":"boolean"}],"enum":[false]}),
            &json!(true),
            0
        ));
        assert!(!schema_accepts(
            &json!({"type":"string","pattern":"unreviewed"}),
            &json!("unreviewed"),
            0
        ));
        assert!(!schema_accepts(
            &json!({"type":"string","pattern":r"^[0-9A-Fa-f]{6}(?![\s\S])"}),
            &json!("aabbcc\n"),
            0
        ));
        assert!(!schema_accepts(
            &json!({"type":"object","properties":{}}),
            &json!({}),
            0
        ));
    }
    #[test]
    fn exactly_fixture_qualified_ids_are_admitted_without_promoting_product_flags() {
        let mut admitted = 0;
        for entry in catalog()["commands"].as_array().unwrap() {
            let command = entry["id"].as_str().unwrap();
            assert_eq!(entry["qualification"], "unqualified");
            if entry["engineQualification"]["status"] == "qualified" {
                let params = &entry["engineQualification"]["testedParameters"];
                assert!(
                    validate_catalog_candidate(command, params).is_ok(),
                    "{command}: {params}"
                );
                let mut extra = params.clone();
                extra
                    .as_object_mut()
                    .unwrap()
                    .insert("unexpected".into(), json!(true));
                assert!(
                    validate_catalog_candidate(command, &extra).is_err(),
                    "{command}"
                );
                admitted += 1;
            } else {
                assert_eq!(
                    validate_catalog_candidate(command, &json!({})),
                    Err("command_denied"),
                    "{command}"
                );
            }
        }
        assert_eq!(admitted, 72);
        assert_eq!(
            catalog_identity(),
            json!(
                "wordcraft-c1-qualified72-v5:9f1cb61f8ffb8e4890001aab1a9a222e327a8e98f4531ef50a7b66e28cc0f0cb"
            )
        );
    }
    #[test]
    fn actual_list_define_metadata_is_not_the_selection_receipt() {
        let author = "Alice";
        let time = "2026-10-26T12:00:00Z";
        let mut session = Session::new(Document::new());
        session
            .run_with_host_context("text.insert", &json!({"text":"Alpha"}), author, time)
            .unwrap();
        let before: Value =
            serde_json::from_slice(&native_io::save_bytes("state.json", &session.doc).unwrap())
                .unwrap();
        let params =
            catalog_entry("list.define").unwrap()["engineQualification"]["testedParameters"]
                .clone();
        session
            .run_with_host_context(
                "select.range",
                &json!({"anchor":{"block":0,"off":0},"focus":{"block":0,"off":5}}),
                author,
                time,
            )
            .unwrap();
        let original = session
            .run_with_host_context("list.define", &params, author, time)
            .unwrap();
        assert!(
            original["num"].as_u64().is_some_and(|n| n > 0),
            "the actual native command returns num metadata"
        );
        let selected = serde_json::to_value(&session.sel).unwrap();
        let expected: Value =
            serde_json::from_slice(&native_io::save_bytes("state.json", &session.doc).unwrap())
                .unwrap();
        let reply = run(&serde_json::to_vec(&json!({"command":"list.define","params":params,"native":before,"target":{"block":0,"unit":"utf8_bytes","start":0,"end":5},"author":author,"time":time})).unwrap()).unwrap();
        assert_eq!(
            reply["native"], expected,
            "normalization must not change native list definitions or document"
        );
        assert_eq!(
            reply["result"], selected,
            "receipt must use actual same-command Session selection, not metadata or an inferred caret"
        );
        assert_eq!(reply["result"].as_object().unwrap().len(), 2);
    }
}
