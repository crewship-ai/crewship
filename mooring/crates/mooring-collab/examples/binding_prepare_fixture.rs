//! Explicit local input generator for the existing Go/Rust preparation seam.
use mooring_collab::experimental_binding::{BindingAdapter, WriteScope};
use mooring_core::adapter::PrepareAdapter;
use mooring_wordcraft::experimental_binding::Binding;
use serde_json::{json, Value};
fn main() -> Result<(), Box<dyn std::error::Error>> {
    let output =
        std::path::PathBuf::from(std::env::args().nth(1).ok_or("output directory required")?);
    if !output.is_dir() {
        return Err("existing output directory required".into());
    }
    let mut before: Value = serde_json::from_str(include_str!(
        "../../mooring-wordcraft/tests/fixtures/d1-engine-canonical.json"
    ))?;
    before["body"] = json!([{"kind":"para","text":"Ahoj","runs":[{"len":4}]},{"kind":"para","text":"světe","runs":[{"len":6}]}]);
    let cp = Binding::import(before.clone(), vec!["left".into(), "peer".into()], 101)
        .map_err(|_| "import")?
        .checkpoint()
        .map_err(|_| "checkpoint")?;
    let mut client = Binding::restore(&cp, 202).map_err(|_| "restore")?;
    before["body"][0] = json!({"kind":"para","text":"Ahoj!","runs":[{"len":5}]});
    let update = client
        .reconcile(&before, vec!["left".into(), "peer".into()])
        .map_err(|_| "reconcile")?;
    let payload = json!({"kind":"wordcraft.binding.update","update":mooring_protocol::boundary::encode_base64(&update.update,262144).map_err(|_|"encoding")?,"basis_state":update.basis_state,"basis_structure":update.basis_structure,"basis_ids":update.basis_ids});
    let inventory = BindingAdapter::new(202, WriteScope::WholeDocument)
        .map_err(|_| "adapter")?
        .validate_checkpoint(&cp)
        .map_err(|_| "inventory")?;
    let reads: Vec<_> = inventory
        .iter()
        .filter(|t| !t.deleted)
        .map(|t| json!({"target_id":t.target_id,"revision":"0","structure_revision":"0"}))
        .collect();
    for (name, bytes) in [
        ("checkpoint.json", cp),
        ("payload.json", serde_json::to_vec(&payload)?),
        ("reads.json", serde_json::to_vec(&reads)?),
    ] {
        use std::io::Write;
        let mut file = std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(output.join(name))?;
        file.write_all(&bytes)?;
    }
    Ok(())
}
