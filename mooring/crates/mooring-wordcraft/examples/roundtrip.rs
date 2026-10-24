//! Offline qualification only. IDs are supplied explicitly, never inferred from
//! paragraph indexes. This executable has no host credentials or Saved authority.
use std::{error::Error, fs, path::PathBuf};

use mooring_wordcraft::Document;
use serde_json::{json, Value};

fn main() -> Result<(), Box<dyn Error>> {
    let args = std::env::args().skip(1).collect::<Vec<_>>();
    if args.len() != 3 {
        return Err(
            "usage: roundtrip NATIVE_JSON STABLE_IDS_JSON EXISTING_OUTPUT_DIRECTORY; or roundtrip --checkpoint CHECKPOINT EXISTING_OUTPUT_DIRECTORY".into(),
        );
    }
    let out = PathBuf::from(&args[2]);
    if !out.is_dir() {
        return Err("output directory must already exist".into());
    }
    let (document, native, ids) = if args[0] == "--checkpoint" {
        let document = Document::restore(&fs::read(&args[1])?)?;
        let native = document.native()?;
        let ids = document.block_ids()?;
        (document, native, ids)
    } else {
        let native: Value = serde_json::from_slice(&fs::read(&args[0])?)?;
        let ids: Vec<String> = serde_json::from_slice(&fs::read(&args[1])?)?;
        let document = Document::import(native.clone(), ids.clone())?;
        (document, native, ids)
    };
    let projection = document.native()?;
    let checkpoint = document.checkpoint()?;
    drop(document);
    let restored = Document::restore(&checkpoint)?;
    if projection != native || restored.native()? != native || restored.block_ids()? != ids {
        return Err("native data or stable IDs changed across storage-only roundtrip".into());
    }
    fs::write(
        out.join("native-projection.json"),
        serde_json::to_vec_pretty(&projection)?,
    )?;
    fs::write(out.join("checkpoint.json"), checkpoint)?;
    fs::write(
        out.join("native-restored.json"),
        serde_json::to_vec_pretty(&restored.native()?)?,
    )?;
    println!(
        "{}",
        json!({"read_only": restored.is_read_only(), "block_count":ids.len(), "native_equal":true, "restored_native_equal":true, "stable_ids_equal":true})
    );
    Ok(())
}
