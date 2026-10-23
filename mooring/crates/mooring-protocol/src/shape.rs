//! Validator for the exact bundled schema vocabulary, not arbitrary schemas.
//! A valid shape carries no authenticated identity or permission.
use serde_json::Value;
use std::{
    collections::{BTreeMap, BTreeSet},
    sync::LazyLock,
};

const COUNTER_PATTERN: &str = "^(0|[1-9][0-9]{0,18}|1[0-7][0-9]{18}|18[0-3][0-9]{17}|184[0-3][0-9]{16}|1844[0-5][0-9]{15}|18446[0-6][0-9]{14}|184467[0-3][0-9]{13}|1844674[0-3][0-9]{12}|184467440[0-6][0-9]{10}|1844674407[0-2][0-9]{9}|18446744073[0-6][0-9]{8}|1844674407370[0-8][0-9]{6}|18446744073709[0-4][0-9]{5}|184467440737095[0-4][0-9]{4}|18446744073709550[0-9]{3}|18446744073709551[0-5][0-9]{2}|1844674407370955160[0-9]{1}|1844674407370955161[0-4]|18446744073709551615)$";

static SCHEMA: LazyLock<Value> = LazyLock::new(|| {
    let schema = serde_json::from_str(include_str!("../../../protocol/schema.json"))
        .expect("bundled protocol schema must be valid JSON");
    assert!(vocabulary(&schema), "unsupported bundled schema vocabulary");
    assert!(
        local_references(&schema, &schema["$defs"]),
        "invalid bundled reference"
    );
    assert!(
        expanded_depth(
            &schema,
            &schema["$defs"],
            &mut BTreeSet::new(),
            &mut BTreeMap::new(),
            128
        )
        .is_some(),
        "cyclic or over-deep bundled schema graph"
    );
    schema
});

/// Check one bundled definition. Input must already have passed strict JSON
/// parsing and the wire resource profile; this does not authenticate anything.
#[cfg(test)]
pub(crate) fn definition(name: &str, value: &Value) -> bool {
    SCHEMA["$defs"]
        .get(name)
        .is_some_and(|schema| matches(schema, value, 0))
}

pub(crate) fn envelope(value: &Value) -> bool {
    matches(&SCHEMA, value, 0)
}

fn matches(schema: &Value, value: &Value, depth: usize) -> bool {
    if depth > 128 {
        return false;
    }
    if let Some(allowed) = schema.as_bool() {
        return allowed;
    }
    let Some(object) = schema.as_object() else {
        return false;
    };
    if let Some(reference) = object.get("$ref") {
        let Some(target) = reference
            .as_str()
            .and_then(|reference| reference.strip_prefix("#/$defs/"))
            .and_then(|name| SCHEMA["$defs"].get(name))
        else {
            return false;
        };
        if !matches(target, value, depth + 1) {
            return false;
        }
    }
    if object
        .get("const")
        .is_some_and(|constant| value != constant)
        || object.get("enum").is_some_and(|choices| {
            !choices
                .as_array()
                .is_some_and(|choices| choices.contains(value))
        })
    {
        return false;
    }
    if let Some(kind) = object.get("type") {
        let valid = match kind.as_str() {
            Some("object") => value.is_object(),
            Some("array") => value.is_array(),
            Some("string") => value.is_string(),
            Some("boolean") => value.is_boolean(),
            Some("null") => value.is_null(),
            Some("number") => value.is_number(),
            Some("integer") => value.as_i64().is_some() || value.as_u64().is_some(),
            _ => false,
        };
        if !valid {
            return false;
        }
    }
    if let Some(properties) = value.as_object() {
        if object.get("required").is_some_and(|required| {
            !required.as_array().is_some_and(|required| {
                required.iter().all(|name| {
                    name.as_str()
                        .is_some_and(|name| properties.contains_key(name))
                })
            })
        }) {
            return false;
        }
        for (name, item) in properties {
            if let Some(child) = object.get("properties").and_then(|p| p.get(name)) {
                if !matches(child, item, depth + 1) {
                    return false;
                }
            } else if let Some(additional) = object.get("additionalProperties") {
                if !matches(additional, item, depth + 1) {
                    return false;
                }
            }
        }
    }
    if let Some(items) = value.as_array() {
        if !bounds(object, "minItems", "maxItems", items.len()) {
            return false;
        }
        if object.get("uniqueItems") == Some(&Value::Bool(true)) {
            let mut seen = BTreeSet::new();
            for item in items {
                let Ok(key) = serde_json::to_vec(item) else {
                    return false;
                };
                if !seen.insert(key) {
                    return false;
                }
            }
        }
        if let Some(child) = object.get("items") {
            if !items.iter().all(|item| matches(child, item, depth + 1)) {
                return false;
            }
        }
    }
    if let Some(text) = value.as_str() {
        if !bounds(object, "minLength", "maxLength", text.chars().count()) {
            return false;
        }
        if object.get("pattern").is_some_and(|expression| {
            !expression
                .as_str()
                .is_some_and(|expression| pattern(expression, text).unwrap_or(false))
        }) {
            return false;
        }
    }
    for (keyword, mode) in [("allOf", 0), ("anyOf", 1), ("oneOf", 2)] {
        if let Some(alternatives) = object.get(keyword) {
            let Some(alternatives) = alternatives.as_array() else {
                return false;
            };
            let count = alternatives
                .iter()
                .filter(|child| matches(child, value, depth + 1))
                .count();
            if match mode {
                0 => count != alternatives.len(),
                1 => count == 0,
                _ => count != 1,
            } {
                return false;
            }
        }
    }
    if object
        .get("not")
        .is_some_and(|child| matches(child, value, depth + 1))
    {
        return false;
    }
    if let Some(condition) = object.get("if") {
        let branch = if matches(condition, value, depth + 1) {
            "then"
        } else {
            "else"
        };
        if object
            .get(branch)
            .is_some_and(|child| !matches(child, value, depth + 1))
        {
            return false;
        }
    }
    true
}

fn bounds(
    schema: &serde_json::Map<String, Value>,
    minimum: &str,
    maximum: &str,
    length: usize,
) -> bool {
    !schema
        .get(minimum)
        .is_some_and(|bound| bound.as_u64().is_none_or(|bound| (length as u64) < bound))
        && !schema
            .get(maximum)
            .is_some_and(|bound| bound.as_u64().is_none_or(|bound| (length as u64) > bound))
}

pub(crate) fn version(text: &str) -> bool {
    text.len() <= 29
        && text.split('.').count() == 3
        && text.split('.').all(|part| {
            !part.is_empty()
                && part.len() <= 9
                && (part.len() == 1 || !part.starts_with('0'))
                && part.bytes().all(|byte| byte.is_ascii_digit())
        })
}

fn pattern(expression: &str, text: &str) -> Option<bool> {
    Some(match expression {
        "\0" => text.contains('\0'),
        "[^0-9.]" => text
            .bytes()
            .any(|byte| !byte.is_ascii_digit() && byte != b'.'),
        "[^0-9]" => text.bytes().any(|byte| !byte.is_ascii_digit()),
        "[^A-Za-z0-9+/=]" => text
            .bytes()
            .any(|byte| !byte.is_ascii_alphanumeric() && !matches!(byte, b'+' | b'/' | b'=')),
        "^(0|[1-9][0-9]{0,8})\\.(0|[1-9][0-9]{0,8})\\.(0|[1-9][0-9]{0,8})$" => version(text),
        "^sha256:[0-9a-f]{64}$" => text.strip_prefix("sha256:").is_some_and(|digest| {
            digest.len() == 64
                && digest
                    .bytes()
                    .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
        }),
        "^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$" => {
            let body = text.trim_end_matches('=');
            let padding = text.len() - body.len();
            text.len() % 4 == 0
                && padding <= 2
                && body
                    .bytes()
                    .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'+' | b'/'))
                && (padding == 0 || body.len() % 4 == 4 - padding)
        }
        // This exact bounded decimal language is equivalent to Counter's
        // canonical u64 parse. Do not accept an unknown future regex by shape.
        COUNTER_PATTERN => crate::Counter::try_from(text.to_owned()).is_ok(),
        _ => return None,
    })
}

/// Reject schema vocabulary drift rather than silently ignoring new rules.
fn vocabulary(schema: &Value) -> bool {
    if schema.is_boolean() {
        return true;
    }
    let Some(object) = schema.as_object() else {
        return false;
    };
    if object.get("type").is_some_and(|kind| {
        !matches!(
            kind.as_str(),
            Some("object" | "array" | "string" | "boolean" | "null" | "number" | "integer")
        )
    }) || object.get("enum").is_some_and(|choices| {
        !choices
            .as_array()
            .is_some_and(|choices| !choices.is_empty())
    }) || object.get("required").is_some_and(|names| {
        !names
            .as_array()
            .is_some_and(|names| names.iter().all(Value::is_string))
    }) || object
        .get("uniqueItems")
        .is_some_and(|flag| !flag.is_boolean())
        || ["minItems", "maxItems", "minLength", "maxLength"]
            .iter()
            .any(|key| {
                object
                    .get(*key)
                    .is_some_and(|bound| bound.as_u64().is_none())
            })
    {
        return false;
    }
    let allowed: BTreeSet<_> = [
        "$schema",
        "$defs",
        "$ref",
        "title",
        "description",
        "x-schema-version",
        "x-direction",
        "type",
        "const",
        "enum",
        "properties",
        "required",
        "additionalProperties",
        "items",
        "minItems",
        "maxItems",
        "uniqueItems",
        "minLength",
        "maxLength",
        "pattern",
        "allOf",
        "anyOf",
        "oneOf",
        "not",
        "if",
        "then",
        "else",
    ]
    .into_iter()
    .collect();
    if object.get("pattern").is_some_and(|expression| {
        !expression
            .as_str()
            .is_some_and(|expression| pattern(expression, "").is_some())
    }) {
        return false;
    }
    if object.keys().any(|key| !allowed.contains(key.as_str())) {
        return false;
    }
    for key in ["$defs", "properties"] {
        if let Some(children) = object.get(key) {
            if !children
                .as_object()
                .is_some_and(|children| children.values().all(vocabulary))
            {
                return false;
            }
        }
    }
    for key in ["additionalProperties", "items", "not", "if", "then", "else"] {
        if object.get(key).is_some_and(|child| !vocabulary(child)) {
            return false;
        }
    }
    for key in ["allOf", "anyOf", "oneOf"] {
        if object.get(key).is_some_and(|children| {
            !children
                .as_array()
                .is_some_and(|children| !children.is_empty() && children.iter().all(vocabulary))
        }) {
            return false;
        }
    }
    true
}

fn local_references(value: &Value, definitions: &Value) -> bool {
    match value {
        Value::Object(object) => {
            object.get("$ref").is_none_or(|reference| {
                reference
                    .as_str()
                    .and_then(|reference| reference.strip_prefix("#/$defs/"))
                    .is_some_and(|name| definitions.get(name).is_some())
            }) && object
                .values()
                .all(|child| local_references(child, definitions))
        }
        Value::Array(items) => items
            .iter()
            .all(|child| local_references(child, definitions)),
        _ => true,
    }
}

// Prove every schema path (including unused definitions/negations) fits the
// evaluator bound before matching. Reaching a limit under `not` must never
// become a successful negation. No externally supplied schemas are loaded.
fn expanded_depth<'a>(
    schema: &'a Value,
    definitions: &'a Value,
    active: &mut BTreeSet<&'a str>,
    memo: &mut BTreeMap<&'a str, usize>,
    remaining: usize,
) -> Option<usize> {
    if remaining == 0 {
        return None;
    }
    if schema.is_boolean() {
        return Some(1);
    }
    let object = schema.as_object()?;
    let mut deepest = 0;
    if let Some(reference) = object.get("$ref") {
        let reference = reference.as_str()?;
        if active.contains(reference) {
            return None;
        }
        let depth = if let Some(depth) = memo.get(reference) {
            *depth
        } else {
            let target = definitions.get(reference.strip_prefix("#/$defs/")?)?;
            active.insert(reference);
            let depth = expanded_depth(target, definitions, active, memo, remaining - 1);
            active.remove(reference);
            let depth = depth?;
            memo.insert(reference, depth);
            depth
        };
        deepest = deepest.max(depth);
    }
    let mut children = Vec::new();
    for key in ["$defs", "properties"] {
        if let Some(group) = object.get(key) {
            children.extend(group.as_object()?.values());
        }
    }
    for key in ["items", "if", "then", "else", "not", "additionalProperties"] {
        if let Some(child) = object.get(key) {
            children.push(child);
        }
    }
    for key in ["allOf", "anyOf", "oneOf"] {
        if let Some(group) = object.get(key) {
            children.extend(group.as_array()?);
        }
    }
    for child in children {
        deepest = deepest.max(expanded_depth(
            child,
            definitions,
            active,
            memo,
            remaining - 1,
        )?);
    }
    let depth = deepest.checked_add(1)?;
    (depth <= remaining).then_some(depth)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn every_published_schema_case_and_vocabulary_are_enforced() {
        assert!(vocabulary(&SCHEMA));
        let fixtures: Value =
            serde_json::from_str(include_str!("../../../protocol/conformance-fixtures.json"))
                .unwrap();
        let mut count = 0;
        for case in fixtures["schema_cases"].as_array().unwrap() {
            let reference = case["schema_ref"].as_str().unwrap();
            let actual = if reference == "#" {
                envelope(&case["message"])
            } else {
                definition(
                    reference.strip_prefix("#/$defs/").unwrap(),
                    &case["message"],
                )
            };
            assert_eq!(
                actual,
                case["schema_valid"].as_bool().unwrap(),
                "{}",
                case["id"]
            );
            count += 1;
        }
        assert_eq!(count, 173);
        assert!(!vocabulary(&serde_json::json!({"minProperties": 1})));
        assert!(!vocabulary(
            &serde_json::json!({"not":{"pattern":"unknown-regex"}})
        ));
        assert!(!vocabulary(
            &serde_json::json!({"not":{"type":"unsupported"}})
        ));
        assert!(!local_references(
            &serde_json::json!({"not":{"$ref":"https://external/schema"}}),
            &SCHEMA["$defs"]
        ));
    }
    #[test]
    fn reference_cycles_under_negation_and_unused_definitions_fail_before_matching() {
        for schema in [
            serde_json::json!({"$defs":{"a":{"not":{"$ref":"#/$defs/a"}}}}),
            serde_json::json!({"$defs":{"a":{"$ref":"#/$defs/b"},"b":{"not":{"$ref":"#/$defs/a"}}}}),
        ] {
            assert!(vocabulary(&schema));
            assert!(local_references(&schema, &schema["$defs"]));
            assert!(expanded_depth(
                &schema,
                &schema["$defs"],
                &mut BTreeSet::new(),
                &mut BTreeMap::new(),
                128
            )
            .is_none());
        }
        assert!(expanded_depth(
            &SCHEMA,
            &SCHEMA["$defs"],
            &mut BTreeSet::new(),
            &mut BTreeMap::new(),
            128
        )
        .is_some());
        let mut schema = serde_json::json!({});
        for _ in 0..129 {
            schema = serde_json::json!({"not":schema});
        }
        assert!(expanded_depth(
            &schema,
            &Value::Null,
            &mut BTreeSet::new(),
            &mut BTreeMap::new(),
            128
        )
        .is_none());
    }
}
