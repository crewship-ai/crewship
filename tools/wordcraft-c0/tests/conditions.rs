//! Runtime gap probes, not a production host or a replacement engine.
//! Every test runs separately: even process-global negative controls are local
//! to the disposable test process. No file/network application commands run.
use serde_json::{Value, json};
use std::{fs, time::Instant};
use unicode_segmentation::UnicodeSegmentation;
use wordcraft_doc::{Block, Document};
use wordcraft_engine::{Session, io};

const PIN: &str = "7ef2633a7e84956522d1ca47b99fe6feef16c1db";
fn evidence(case: &str, value: Value) {
    eprintln!(
        "C0_EVIDENCE {}",
        json!({"case":case,"engine_pin":PIN,"value":value})
    );
}
fn command(s: &mut Session, id: &str, params: Value) -> Value {
    s.run(id, &params)
        .unwrap_or_else(|e| panic!("positive engine command {id} failed: {e}"))
}
fn select(s: &mut Session, block: usize, a: usize, b: usize) {
    command(
        s,
        "select.range",
        json!({"anchor":{"block":block,"off":a},"focus":{"block":block,"off":b}}),
    );
}
fn text(s: &Session, block: usize) -> String {
    let Block::Para(p) = &*s.doc.body[block] else {
        panic!("expected paragraph")
    };
    p.text.clone()
}
fn checkpoint(s: &Session) -> Vec<u8> {
    io::save_bytes("state.json", &s.doc).unwrap()
}
fn reload(bytes: &[u8]) -> Session {
    Session::new(io::open_bytes("state.json", bytes).unwrap())
}
fn seeded(value: &str) -> Session {
    let mut s = Session::new(Document::new());
    command(&mut s, "text.insert", json!({"text":value}));
    assert_eq!(text(&s, 0), value, "actual engine positive control");
    s.reset_history();
    s
}
// C0's proposed adapter seam: obtain persistent native block identity. No ID
// is synthesized from index, pointer, or content: those all failed Gate B.
// C1 must bind this seam to its reviewed identity representation, including
// any companion checkpoint metadata, rather than relabeling these probes green.
fn identities(s: &Session) -> Vec<Option<Value>> {
    s.doc
        .body
        .iter()
        .map(|b| serde_json::to_value(&**b).unwrap().get("id").cloned())
        .collect()
}

#[test]
fn c0_1_block_identity_survives_split_join_undo_restart() {
    if let Ok(input) = std::env::var("C0_RELOAD_INPUT") {
        let restored = reload(&fs::read(input).unwrap());
        fs::write(
            std::env::var("C0_RELOAD_DOCUMENT").unwrap(),
            checkpoint(&restored),
        )
        .unwrap();
        fs::write(
            std::env::var("C0_RELOAD_IDENTITIES").unwrap(),
            serde_json::to_vec(&identities(&restored)).unwrap(),
        )
        .unwrap();
        return;
    }
    let mut s = seeded("same same");
    command(&mut s, "text.newParagraph", json!({}));
    command(&mut s, "text.insert", json!({"text":"same same"}));
    s.reset_history();
    let original = identities(&s);
    select(&mut s, 0, 4, 4);
    command(&mut s, "text.newParagraph", json!({}));
    assert_eq!(s.doc.body.len(), 3, "split really ran");
    let split = identities(&s);
    select(&mut s, 1, 0, 0);
    command(&mut s, "text.backspace", json!({}));
    assert_eq!(s.doc.body.len(), 2, "join really ran");
    let joined = identities(&s);
    command(&mut s, "edit.undo", json!({}));
    assert_eq!(s.doc.body.len(), 3, "undo really ran");
    let undone = identities(&s);
    let bytes = checkpoint(&s);
    let root = std::env::var_os("C0_OUTPUT_DIR")
        .map(std::path::PathBuf::from)
        .unwrap_or_else(|| {
            std::env::temp_dir().join(format!("wordcraft-c0-{}", std::process::id()))
        });
    fs::create_dir_all(&root).unwrap();
    let input = root.join("identity-confirmed.json");
    let output = root.join("identity-restarted.json");
    let ids = root.join("identity-restarted-ids.json");
    fs::write(&input, &bytes).unwrap();
    // Restart means a new OS process using the same real pinned test binary;
    // the child loads only native checkpoint bytes, no retained Arc/session.
    let child = std::process::Command::new(std::env::current_exe().unwrap())
        .args([
            "--exact",
            "c0_1_block_identity_survives_split_join_undo_restart",
            "--nocapture",
            "--test-threads=1",
        ])
        .env("C0_RELOAD_INPUT", &input)
        .env("C0_RELOAD_DOCUMENT", &output)
        .env("C0_RELOAD_IDENTITIES", &ids)
        .output()
        .unwrap();
    assert!(
        child.status.success(),
        "native restart child failed: {}",
        String::from_utf8_lossy(&child.stderr)
    );
    assert_eq!(
        fs::read(output).unwrap(),
        bytes,
        "separate-process native restart content control"
    );
    let restarted: Vec<Option<Value>> = serde_json::from_slice(&fs::read(ids).unwrap()).unwrap();
    let present = original.iter().all(Option::is_some) && split.iter().all(Option::is_some);
    let unique = split.iter().collect::<std::collections::HashSet<_>>().len() == split.len();
    evidence(
        "identity",
        json!({"original":original,"split":split,"joined":joined,"undone":undone,"restart":restarted,"duplicate_paragraphs":true,"separate_os_process_restart":true}),
    );
    assert!(
        present
            && unique
            && split[0] == original[0]
            && split[2] == original[1]
            && joined == original
            && undone == split
            && restarted == undone,
        "persistent block identities absent/changed across actual split/join/undo/native restart; pointers and duplicate-content hashes are not IDs"
    );
}

#[test]
fn c0_2_utf8_contract_rejects_inside_grapheme_without_snapping() {
    let value = "Příliš kůň e\u{301} 👩\u{200d}👩\u{200d}👧";
    let mut s = seeded(value);
    let start = value.find("kůň").unwrap();
    select(&mut s, 0, start, start + "kůň".len());
    assert_eq!(
        &text(&s, 0)[s.sel.anchor.off..s.sel.focus.off],
        "kůň",
        "UTF8 positive target"
    );
    let boundaries: Vec<_> = value.grapheme_indices(true).map(|(i, _)| i).collect();
    let combining = value.find("e\u{301}").unwrap() + 1;
    let zwj = value.find('👩').unwrap() + "👩".len();
    assert!(!boundaries.contains(&combining) && !boundaries.contains(&zwj));
    let mut accepted = vec![];
    for offset in [combining, zwj] {
        let prior = serde_json::to_value(&s.sel).unwrap();
        let result = s.run(
            "select.range",
            &json!({"anchor":{"block":0,"off":offset},"focus":{"block":0,"off":offset}}),
        );
        accepted.push(json!({"byte":offset,"accepted":result.is_ok(),"selection_unchanged":serde_json::to_value(&s.sel).unwrap()==prior}));
    }
    let before = checkpoint(&s);
    let split = if accepted.last().unwrap()["accepted"] == true {
        Some(s.run("text.newParagraph", &json!({})))
    } else {
        None
    };
    let wrong_unit = value[..start].encode_utf16().count();
    evidence(
        "unicode",
        json!({"unit":"utf8_bytes","utf8_target":start,"utf16_is_different":wrong_unit!=start,"inside":accepted,"inside_enter_accepted":split.as_ref().is_some_and(Result::is_ok),"document_unchanged":before==checkpoint(&s)}),
    );
    assert!(
        accepted
            .iter()
            .all(|v| v["accepted"] == false && v["selection_unchanged"] == true),
        "engine accepted agent target inside combining/ZWJ grapheme instead of refusal; unit conversion alone does not qualify it"
    );
}

#[test]
fn c0_3_author_and_authoritative_time_reach_engine_before_command() {
    let mut s = seeded("Comment target");
    select(&mut s, 0, 0, 7);
    s.author = "Jana-original-envelope".into();
    let desired = "2001-02-03T04:05:06Z";
    // The baseline public engine has an author input but no clock injection.
    // Capture its ORIGINAL output, before any host restamping can hide the gap.
    let first = command(
        &mut s,
        "review.newComment",
        json!({"text":"first original"}),
    );
    let id = first["id"].as_u64().unwrap() as u32;
    let first_record = serde_json::to_value(&s.doc.comments[&id]).unwrap();
    select(&mut s, 0, 8, 14);
    s.author = "Petr-original-envelope".into();
    command(
        &mut s,
        "review.newComment",
        json!({"text":"second original"}),
    );
    assert_eq!(
        serde_json::to_value(&s.doc.comments[&id]).unwrap(),
        first_record,
        "older original comment preserved"
    );
    assert!(
        first_record.to_string().contains("Jana-original-envelope"),
        "author positive control"
    );
    evidence(
        "clock",
        json!({"requested_before_command":desired,"original_engine_record":first_record,"old_record_preserved":true,"posthoc_rewrite":false}),
    );
    assert!(
        first_record.to_string().contains(desired),
        "original engine comment uses process clock; authoritative host time was not supplied before command"
    );
}

#[test]
fn c0_4_allowlist_rejects_unknown_fields_and_process_globals() {
    let mut s = seeded("allowed");
    let before = checkpoint(&s);
    let unknown = s.run(
        "text.insert",
        &json!({"text":"MUST-NOT-APPEAR","unknown_c0_field":true}),
    );
    let changed = checkpoint(&s) != before;
    // Only in-memory Ignore All; no dictionary/font/file/macro/network actions.
    let global = s.run(
        "review.ignoreAll",
        &json!({"word":"c0-isolated-nonce-word"}),
    );
    let absent = s.run("not.a.registered.command", &json!({}));
    evidence(
        "catalog",
        json!({"allowed_command_executed":true,"unknown_field_accepted":unknown.is_ok(),"unknown_field_mutated":changed,"process_global_accepted":global.is_ok(),"unknown_id_refused":absent.is_err()}),
    );
    assert!(
        unknown.is_err() && !changed && global.is_err() && absent.is_err(),
        "native dispatcher is not an allowlisting host: unknown fields/global-state command accepted"
    );
}

fn rss_kib() -> Option<u64> {
    fs::read_to_string("/proc/self/status")
        .ok()?
        .lines()
        .find(|l| l.starts_with("VmRSS:"))?
        .split_whitespace()
        .nth(1)?
        .parse()
        .ok()
}
#[test]
fn c0_5_bounded_history_pending_metrics_and_m2_rollback_retention() {
    let mut s = seeded(&"word ".repeat(200));
    let start = Instant::now();
    let mut durations = vec![];
    // Exercise the engine's actual documented/source MAX_UNDO=500 policy.
    for n in 0..550 {
        select(&mut s, 0, 0, 4);
        let tick = Instant::now();
        command(
            &mut s,
            if n % 2 == 0 {
                "format.bold"
            } else {
                "format.italic"
            },
            json!({}),
        );
        durations.push(tick.elapsed().as_micros());
    }
    let retained = s.undo_labels().len();
    assert!(
        retained > 0 && retained <= 500,
        "baseline engine history must be genuinely populated and bounded"
    );
    let saved = checkpoint(&s);
    let rss_before = rss_kib();
    let mut pending = vec![];
    for n in 0..32 {
        select(&mut s, 0, 0, 0);
        command(
            &mut s,
            "text.insert",
            json!({"text":format!("candidate{n} ")}),
        );
        pending.push(s.edit_snapshot());
    }
    let rss_pending = rss_kib();
    let pending_count = pending.len();
    drop(pending);
    drop(s); // M2 discards the entire candidate engine, not just pending handles.
    let rss_after_discard = rss_kib();
    let restore_tick = Instant::now();
    let restored = reload(&saved); // M2: rejected candidate discarded, confirmed bytes restored.
    let restore_us = restore_tick.elapsed().as_micros();
    assert_eq!(checkpoint(&restored), saved, "M2 document control");
    durations.sort_unstable();
    evidence(
        "retention",
        json!({"undo_policy":500,"history_before":retained,"history_after_m2":restored.undo_labels().len(),"pending_count":pending_count,"rss_kib_before":rss_before,"rss_kib_pending":rss_pending,"rss_kib_after_pending_and_k_drop":rss_after_discard,"rss_kib_after_m2":rss_kib(),"command_p95_us":durations[durations.len()*95/100],"m2_restore_us":restore_us,"total_ms":start.elapsed().as_millis(),"hard_300mb_or_300ms_assert":false}),
    );
    assert_eq!(
        restored.undo_labels().len(),
        retained,
        "M2 rollback loses retained committed undo: native bound alone is not independent host/participant undo retention"
    );
}

#[test]
fn c0_6_participant_undo_must_preserve_other_authors_work() {
    let mut s = seeded("");
    s.author = "Jana".into();
    command(&mut s, "text.insert", json!({"text":"JANA"}));
    command(&mut s, "caret.right", json!({})); // Close native typing coalescing group.
    s.author = "Petr".into();
    command(&mut s, "text.insert", json!({"text":"PETR"}));
    command(&mut s, "caret.left", json!({}));
    assert!(
        text(&s, 0).contains("JANA") && text(&s, 0).contains("PETR"),
        "both actual author edits applied"
    );
    let before = text(&s, 0);
    s.author = "Jana".into();
    command(&mut s, "edit.undo", json!({}));
    let after = text(&s, 0);
    evidence(
        "participant_undo",
        json!({"before":before,"undo_requester":"Jana","after":after,"native_undo_depth":s.undo_labels().len()}),
    );
    assert!(
        after.contains("PETR") && !after.contains("JANA"),
        "Jana undo reverted Petr's latest command instead of only Jana's work; global native undo is not participant undo"
    );
}
