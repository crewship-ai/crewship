//! Exercises the existing pure candidate boundary, without claiming host authority.
use mooring_text::yrs::{
    updates::decoder::Decode, ClientID, Doc, Map, OffsetKind, Options, Out, ReadTxn, Text,
    Transact, Update,
};
use mooring_wordcraft::{experimental_human_current as current, Document};

#[test]
fn support_1000_original_unicode_deltas_validate_and_restore() {
    let native = serde_json::from_str(include_str!("fixtures/d1-engine-canonical.json")).unwrap();
    let baseline = Document::import(native, (0..8).map(|i| format!("p{i}")).collect()).unwrap();
    let mut checkpoint = baseline.checkpoint().unwrap();
    let packed: serde_json::Value = serde_json::from_slice(&checkpoint).unwrap();
    let update = mooring_protocol::boundary::decode_base64(
        packed["update_base64"].as_str().unwrap(),
        16 << 20,
    )
    .unwrap();
    let client = Doc::with_options(Options {
        client_id: ClientID::new(42),
        offset_kind: OffsetKind::Utf16,
        skip_gc: true,
        ..Options::default()
    });
    client
        .transact_mut()
        .apply_update(Update::decode_v1(&update).unwrap())
        .unwrap();
    let txn = client.transact();
    let Some(Out::YText(text)) = txn.get_map("wordcraft.texts").unwrap().get(&txn, "p0") else {
        panic!("missing imported paragraph")
    };
    drop(txn);
    let samples = ["a", "ř", "ě", "😀", "\u{301}"];
    let initial_text = baseline.native().unwrap()["body"][0]["text"]
        .as_str()
        .unwrap()
        .to_owned();
    let mut expected = initial_text;
    for index in 0..1000 {
        let frontier = current::checkpoint_frontier(&checkpoint).unwrap();
        let before = client.transact().state_vector();
        let value = samples[index % samples.len()];
        text.push(&mut client.transact_mut(), value);
        expected.push_str(value);
        let original = client.transact().encode_state_as_update_v1(&before);
        let authority =
            current::Authority::Writer(current::HostWriter::current(&checkpoint, 42).unwrap());
        let candidate = current::prepare_current(authority, &frontier, &original, &["p0".into()])
            .unwrap_or_else(|error| panic!("candidate {index}: {error:?}"));
        assert_eq!(
            candidate.native().unwrap()["body"][0]["text"],
            expected,
            "candidate {index}"
        );
        checkpoint = candidate.checkpoint().unwrap();
        if index % 100 == 99 {
            assert_eq!(
                Document::restore(&checkpoint).unwrap().native().unwrap()["body"][0]["text"],
                expected
            );
        }
    }
    eprintln!("wordcraft-candidate: original_deltas=1000 restore_checks=10 checkpoint_bytes={} final_utf8_bytes={}", checkpoint.len(), expected.len());
}
