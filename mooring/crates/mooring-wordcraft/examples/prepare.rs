//! Offline candidate mechanics only; supplied context is not host authority.
use mooring_core::adapter::{PrepareAdapter, Prepared, PreparedCandidate};
use mooring_wordcraft::{adapter::WordCraftAdapter, Document};
use std::{env, fs, path::PathBuf};
fn main() {
    let args: Vec<String> = env::args().collect();
    assert_eq!(
        args.len(),
        5,
        "prepare CHECKPOINT NORMALIZED_CHANGE TRUSTED_CONTEXT EXISTING_OUTPUT_DIRECTORY"
    );
    let checkpoint = fs::read(&args[1]).unwrap();
    let change = serde_json::from_slice(&fs::read(&args[2]).unwrap()).unwrap();
    let context = serde_json::from_slice(&fs::read(&args[3]).unwrap()).unwrap();
    let output = PathBuf::from(&args[4]);
    assert!(output.is_dir());
    let Prepared::Candidate(candidate) = WordCraftAdapter
        .prepare_atomic(&checkpoint, &change, &context)
        .unwrap()
    else {
        panic!("candidate rejected")
    };
    let restored = Document::restore(candidate.checkpoint_bytes()).unwrap();
    fs::write(output.join("checkpoint.json"), candidate.checkpoint_bytes()).unwrap();
    fs::write(
        output.join("native.json"),
        serde_json::to_vec(&restored.native().unwrap()).unwrap(),
    )
    .unwrap();
    fs::write(
        output.join("effects.json"),
        serde_json::to_vec(candidate.effects()).unwrap(),
    )
    .unwrap();
    fs::write(
        output.join("inventory.json"),
        serde_json::to_vec(candidate.inventory()).unwrap(),
    )
    .unwrap();
}
