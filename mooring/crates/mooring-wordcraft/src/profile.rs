//! Application compatibility identity shared by native preparation and WASM.
//! Upstream Cargo 0.10.0 is reused after the release tag; the exact source and
//! qualification artifact hashes are separate from the selected version tuple.
use mooring_protocol::Versions;
use serde_json::{json, Value};

pub const APPLICATION_ID: &str = "wordcraft";
pub const CHECKPOINT_FORMAT: &str = "wordcraft-0.10.0/yrs-0.28.0/v1";
pub const SOURCE_PIN: &str = "668d7ce3a4b1dcf241a9c50a20754a85647a02c4";
pub const SOURCE_DESCRIBE: &str = "v0.10.0-64-g668d7ce3";

pub fn qualified_versions() -> Versions {
    Versions {
        protocol_version: "0.4.0".into(),
        model_version: "0.1.0".into(),
        engine_version: "0.10.0".into(),
    }
}
/// These are the independently qualified upstream tools, NOT the hashes of a
/// future Mooring service, facade or SDK release (those need their own manifest).
pub fn qualification_artifacts() -> Value {
    json!({"native_cli_sha256":"fb7dda6bcd5568e17d7f86c6cfa2ce7a7d21674fc87ec200fee12f5193054d3c","wasm_sha256":"be5fff0a605acb16d6abcefdd7b1000e0dcc1bf987498fae4a0c4b1515a1f182","glue_sha256":"edb496ef37d87549cf70b30617426097a1b9e4bcf184219f55e80c092fc0c4d6"})
}
