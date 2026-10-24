//! The actual pinned application model qualifies serialization, never rewrites input.
use serde_json::Value;

pub(crate) fn lossless(native: &Value) -> bool {
    let Ok(mut typed) = serde_json::from_value::<wordcraft_doc::Document>(native.clone()) else {
        return false;
    };
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
