//! Offline immutable-checkpoint projection, not a host-authenticated read API.
use mooring_wordcraft::{read, Document};
use serde_json::{json, Value};
use std::{env, fs, path::PathBuf};

fn main() {
    let args: Vec<String> = env::args().collect();
    assert_eq!(
        args.len(),
        5,
        "read_pages CHECKPOINT EXISTING_OUTPUT_DIRECTORY MAX_PARAGRAPHS MAX_PAGE_BYTES"
    );
    let bytes = fs::read(&args[1]).unwrap();
    let output = PathBuf::from(&args[2]);
    assert!(output.is_dir());
    let count: usize = args[3].parse().unwrap();
    let budget: usize = args[4].parse().unwrap();
    let document = Document::restore(&bytes).unwrap();
    let mut reconstructed = read::header(&document, budget).unwrap();
    fs::write(
        output.join("header.json"),
        serde_json::to_vec(&reconstructed).unwrap(),
    )
    .unwrap();
    let mut body = Vec::new();
    let mut ids = Vec::new();
    let mut after = None;
    let mut files = Vec::new();
    loop {
        let page = read::page(&document, after.as_deref(), count, budget).unwrap();
        let encoded = serde_json::to_vec(&page).unwrap();
        let name = format!("page-{:04}.json", files.len());
        fs::write(output.join(&name), &encoded).unwrap();
        files.push(json!({"file":name,"bytes":encoded.len(),"paragraphs":page.paragraphs.len()}));
        for paragraph in page.paragraphs {
            ids.push(paragraph.target_id);
            body.push(paragraph.native);
        }
        after = page.next_after;
        if after.is_none() {
            break;
        }
    }
    reconstructed["body"] = Value::Array(body);
    assert_eq!(reconstructed, document.native().unwrap());
    assert_eq!(ids, document.block_ids().unwrap());
    fs::write(
        output.join("summary.json"),
        serde_json::to_vec_pretty(&json!({
            "checkpoint_bytes":bytes.len(),"read_only":document.is_read_only(),
            "paragraphs":ids.len(),"native_and_stable_ids_exact":true,
            "max_paragraphs":count,"max_page_bytes":budget,"pages":files,
            "host_revision_authentication_proven":false
        }))
        .unwrap(),
    )
    .unwrap();
}
