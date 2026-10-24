//! The actual pinned application model qualifies serialization, never rewrites input.
use serde_json::Value;

pub(crate) fn lossless(native: &Value) -> bool {
    // Review properties can be retained in styles, numbering, or sections even
    // when the top-level revision table is empty. This is a conservative read-only
    // boundary, not an application permission evaluator.
    if !review_free(native, 0, &mut 262_144) {
        return false;
    }
    let Ok(mut typed) = serde_json::from_value::<wordcraft_doc::Document>(native.clone()) else {
        return false;
    };
    if typed.settings.track_changes
        || typed.settings.protection.is_some()
        || typed.settings.protect_formatting
        || !typed.settings.protect_hash.is_empty()
    {
        return false;
    }
    // Match native JSON open for the already-closed paragraph subset. Date/time
    // fields and other inline objects were refused before this predicate.
    // Only this disposable typed value is normalized; original input is kept.
    typed.normalize_runs();
    typed.ensure_nonempty();
    match serde_json::to_value(typed) {
        Ok(roundtrip) => roundtrip == *native,
        Err(_) => false,
    }
}

// Bound work and recursion before cloning/decoding the application model. Null
// does not carry a review value; the full typed roundtrip still qualifies it.
fn review_free(value: &Value, depth: usize, remaining: &mut usize) -> bool {
    if depth > 32 || *remaining == 0 {
        return false;
    }
    *remaining -= 1;
    match value {
        Value::Object(fields) => fields.iter().all(|(key, value)| {
            (!matches!(key.as_str(), "fmtChange" | "numChange" | "ins" | "del") || value.is_null())
                && review_free(value, depth + 1, remaining)
        }),
        Value::Array(values) => values
            .iter()
            .all(|value| review_free(value, depth + 1, remaining)),
        _ => true,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    #[test]
    fn review_scan_is_bounded_and_refuses_ambiguous_nonnull_values() {
        for value in [json!(0), json!(false), json!({}), json!([]), json!("")] {
            assert!(!review_free(
                &json!({"nested": {"ins": value}}),
                0,
                &mut 100
            ));
        }
        assert!(review_free(&json!({"nested": {"ins": null}}), 0, &mut 100));
        assert!(!review_free(&json!([0, 1, 2]), 0, &mut 3));
        let mut nested = Value::Null;
        for _ in 0..34 {
            nested = json!([nested]);
        }
        assert!(!review_free(&nested, 0, &mut 100));
    }
}
