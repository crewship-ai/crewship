#![cfg(all(target_os = "linux", feature = "wordcraft-command-experiment"))]
use mooring_collab::command_experiment::{
    process::ProcessEngine, Admission, Decision, Engine, Host, Prepared,
};
use serde_json::{json, Value};
use std::{path::PathBuf, time::Instant};
fn binary() -> PathBuf {
    std::env::var_os("MOORING_C1_ENGINE_BINARY")
        .map(PathBuf::from)
        .expect("actual C1 helper required; no skips or mocks")
}
fn engine() -> ProcessEngine {
    ProcessEngine::spawn(&binary()).expect("actual private engine process")
}
fn host() -> Host<ProcessEngine> {
    Host::new(engine(), "document", "generation1", None).unwrap()
}
fn actor(name: &str, epoch: u64, writer: bool) -> Admission {
    Admission::new(name, name, "2001-02-03T04:05:06Z", epoch, writer).unwrap()
}
fn packet(
    h: &Host<ProcessEngine>,
    op: &str,
    who: &str,
    cmd: &str,
    block: Option<(usize, usize, usize)>,
    mut params: Value,
) -> Vec<u8> {
    if cmd == "participant.undo" {
        if let Some(local) = params.get("op_id").and_then(Value::as_str) {
            if !local.contains(':') {
                params["op_id"] = json!(key(h, local));
            }
        }
    }
    serde_json::to_vec(&json!({"generation":h.generation(),"op_id":key(h,op),"participant":who,"base_revision":h.revision(),"command":cmd,"params":params,"target":block.map(|(i,start,end)|json!({"block_id":h.ids()[i],"unit":"utf8_bytes","start":start,"end":end}))})).unwrap()
}
fn key(h: &Host<ProcessEngine>, local: &str) -> String {
    format!("{}:{local}", h.generation())
}
fn resolve(
    h: &mut Host<ProcessEngine>,
    local: &str,
    decision: Decision,
    current: Option<&Admission>,
) -> Result<Value, &'static str> {
    let op = key(h, local);
    h.resolve(&op, decision, current)
}
fn commit(
    h: &mut Host<ProcessEngine>,
    op: &str,
    who: &str,
    cmd: &str,
    block: Option<(usize, usize, usize)>,
    params: Value,
) {
    let raw = packet(h, op, who, cmd, block, params);
    let a = actor(who, 1, true);
    assert_eq!(
        h.prepare(&raw, &a).unwrap(),
        Prepared::Pending { op_id: key(h, op) }
    );
    assert_eq!(
        resolve(h, op, Decision::Committed, Some(&a)).unwrap()["state"],
        "host_committed"
    )
}
fn text(h: &Host<ProcessEngine>, i: usize) -> String {
    h.native()["body"][i]["text"].as_str().unwrap().into()
}
fn unchanged(h: &Host<ProcessEngine>) -> (Value, u64, usize, Vec<u8>) {
    (
        h.native().clone(),
        h.revision(),
        h.counts().0,
        h.checkpoint().unwrap(),
    )
}

#[test]
fn identities_split_join_undo_reject_and_new_helper_checkpoint() {
    let mut h = host();
    commit(
        &mut h,
        "a1",
        "Alice",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"Same"}),
    );
    commit(
        &mut h,
        "a2",
        "Alice",
        "text.newParagraph",
        Some((0, 4, 4)),
        json!({}),
    );
    commit(
        &mut h,
        "a3",
        "Alice",
        "text.insert",
        Some((1, 0, 0)),
        json!({"text":"Same"}),
    );
    let original = h.ids().to_vec();
    assert_ne!(original[0], original[1]);
    assert_eq!(text(&h, 0), text(&h, 1));
    commit(
        &mut h,
        "split",
        "Alice",
        "text.newParagraph",
        Some((0, 2, 2)),
        json!({}),
    );
    let split = h.ids().to_vec();
    assert_eq!(split[0], original[0]);
    assert_eq!(split[2], original[1]);
    commit(
        &mut h,
        "join",
        "Alice",
        "text.backspace",
        Some((1, 0, 0)),
        json!({}),
    );
    assert_eq!(h.ids(), original);
    commit(
        &mut h,
        "undo-join",
        "Alice",
        "participant.undo",
        None,
        json!({"op_id":"join"}),
    );
    assert_eq!(h.ids(), split);
    let prior = unchanged(&h);
    let a = actor("Alice", 1, true);
    let raw = packet(
        &h,
        "reject-enter",
        "Alice",
        "text.newParagraph",
        Some((0, 0, 0)),
        json!({}),
    );
    // All prior issued IDs are present after undo-join; the next allocation
    // belongs to this rejected split and must never be reissued in this host.
    let issued = h
        .ids()
        .iter()
        .map(|id| id.rsplit(':').next().unwrap().parse::<u64>().unwrap())
        .max()
        .unwrap()
        + 1;
    h.prepare(&raw, &a).unwrap();
    assert_eq!(
        unchanged(&h),
        prior,
        "candidate allocator is outside confirmed P"
    );
    resolve(&mut h, "reject-enter", Decision::Rejected, None).unwrap();
    assert_eq!(unchanged(&h), prior);
    commit(
        &mut h,
        "fresh-enter",
        "Alice",
        "text.newParagraph",
        Some((0, 0, 0)),
        json!({}),
    );
    assert!(!h
        .ids()
        .iter()
        .any(|id| id == &format!("document:generation1:{issued}")));
    let saved = h.checkpoint().unwrap();
    let ids = h.ids().to_vec();
    let native = h.native().clone();
    drop(h);
    let restarted = Host::restore(engine(), &saved, "generation2").unwrap();
    assert_eq!(restarted.ids(), ids);
    assert_eq!(restarted.native(), &native);
    assert!(Host::restore(engine(), &saved, "generation1").is_err());
}
#[test]
fn utf8_only_grapheme_refusal_and_correct_czech_target() {
    let mut h = host();
    commit(
        &mut h,
        "seed",
        "Alice",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"kůň e\u{301} 👩\u{200d}👩\u{200d}👧"}),
    );
    commit(
        &mut h,
        "bold",
        "Alice",
        "format.bold",
        Some((0, 0, "kůň".len())),
        json!({"value":true}),
    );
    let a = actor("Alice", 1, true);
    let prior = unchanged(&h);
    let value = text(&h, 0);
    let inside = [
        value.find("e\u{301}").unwrap() + 1,
        value.find('👩').unwrap() + 4,
        value.find('ů').unwrap() + 1,
    ];
    for (n, offset) in inside.into_iter().enumerate() {
        let raw = packet(
            &h,
            &format!("bad{n}"),
            "Alice",
            "text.newParagraph",
            Some((0, offset, offset)),
            json!({}),
        );
        assert!(h.prepare(&raw, &a).is_err());
        assert_eq!(unchanged(&h), prior);
    }
    let mut raw: Value = serde_json::from_slice(&packet(
        &h,
        "unit",
        "Alice",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"wrong"}),
    ))
    .unwrap();
    raw["target"]["unit"] = json!("utf16");
    assert!(h.prepare(&serde_json::to_vec(&raw).unwrap(), &a).is_err());
    assert_eq!(unchanged(&h), prior);
}
#[test]
fn original_engine_tracking_records_get_trusted_author_and_clock_before_command() {
    let first = host();
    let mut native = first.native().clone();
    drop(first);
    native["settings"]["trackChanges"] = json!(true);
    let mut h = Host::new(engine(), "document", "generation1", Some(native)).unwrap();
    commit(
        &mut h,
        "first",
        "Alice",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"Alice"}),
    );
    let records = h.native()["revisions"]
        .as_array()
        .expect("actual engine tracking record")
        .clone();
    assert!(!records.is_empty());
    assert!(serde_json::to_string(&records)
        .unwrap()
        .contains("2001-02-03T04:05:06Z"));
    assert!(serde_json::to_string(&records).unwrap().contains("Alice"));
    let raw = packet(
        &h,
        "second",
        "Bob",
        "text.insert",
        Some((0, 5, 5)),
        json!({"text":"Bob"}),
    );
    let b = Admission::new("Bob", "Bob", "2002-03-04T05:06:07Z", 1, true).unwrap();
    h.prepare(&raw, &b).unwrap();
    resolve(&mut h, "second", Decision::Committed, Some(&b)).unwrap();
    assert_eq!(
        &h.native()["revisions"].as_array().unwrap()[..records.len()],
        records.as_slice()
    );
    assert!(h.native().to_string().contains("2002-03-04T05:06:07Z"));
    let bad = Admission::new("Alice", "Alice", "2001-99-03T04:05:06Z", 1, true).unwrap();
    let prior = unchanged(&h);
    let raw = packet(
        &h,
        "bad-clock",
        "Alice",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"bad"}),
    );
    assert!(h.prepare(&raw, &bad).is_err());
    assert_eq!(unchanged(&h), prior);
}
#[test]
fn strict_actual_json_and_current_admission_deny_all_reader_mutations() {
    let mut h = host();
    commit(
        &mut h,
        "seed",
        "Alice",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"safe"}),
    );
    let before = unchanged(&h);
    let reader = actor("Alice", 1, false);
    for (n, (cmd, params)) in [
        ("text.insert", json!({"text":"bad"})),
        ("text.delete", json!({})),
        ("text.backspace", json!({})),
        ("text.newParagraph", json!({})),
        ("format.bold", json!({})),
        ("format.font", json!({"name":"Arial"})),
        ("format.size", json!({"size":12})),
        ("para.align", json!({"value":"center"})),
        ("participant.undo", json!({})),
    ]
    .into_iter()
    .enumerate()
    {
        let target = if cmd == "participant.undo" {
            None
        } else {
            Some((0, 0, 1))
        };
        let raw = packet(&h, &format!("reader{n}"), "Alice", cmd, target, params);
        assert!(h.prepare(&raw, &reader).is_err());
        assert_eq!(unchanged(&h), before);
    }
    let raw = packet(&h, "read", "Alice", "document.read", None, json!({}));
    assert!(matches!(
        h.prepare(&raw, &reader).unwrap(),
        Prepared::Read(_)
    ));
    let a = actor("Alice", 1, true);
    let base: Value = serde_json::from_slice(&packet(
        &h,
        "unknown",
        "Alice",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"bad"}),
    ))
    .unwrap();
    for field in ["role", "author", "time", "resolve"] {
        let mut v = base.clone();
        v[field] = json!("forged");
        assert!(h.prepare(&serde_json::to_vec(&v).unwrap(), &a).is_err());
    }
    for (cmd, params) in [
        ("file.open", json!({"path":"not-read"})),
        ("review.ignoreAll", json!({"word":"word"})),
        ("unknown.command", json!({})),
        (
            "text.insert",
            json!({"text":"bad","nested":{"execute":"file.open"}}),
        ),
    ] {
        let raw = packet(&h, "denied", "Alice", cmd, Some((0, 0, 0)), params);
        assert!(h.prepare(&raw, &a).is_err());
    }
    let duplicate = String::from_utf8(serde_json::to_vec(&base).unwrap())
        .unwrap()
        .replace("\"text\":\"bad\"", "\"text\":\"bad\",\"text\":\"hidden\"");
    assert!(h.prepare(duplicate.as_bytes(), &a).is_err());
    let mut foreign = base.clone();
    foreign["participant"] = json!("Bob");
    assert!(h
        .prepare(&serde_json::to_vec(&foreign).unwrap(), &a)
        .is_err());
    let mut stale = base.clone();
    stale["base_revision"] = json!(0);
    assert!(h.prepare(&serde_json::to_vec(&stale).unwrap(), &a).is_err());
    let mut target = base.clone();
    target["target"]["block_id"] = json!("foreign-id");
    assert!(h
        .prepare(&serde_json::to_vec(&target).unwrap(), &a)
        .is_err());
    assert_eq!(unchanged(&h), before);
    for n in 0..3 {
        let raw = packet(
            &h,
            &format!("inflight{n}"),
            "Alice",
            "text.insert",
            Some((0, 0, 0)),
            json!({"text":"uncommitted"}),
        );
        h.prepare(&raw, &a).unwrap();
        let changed = actor("Alice", if n == 1 { 2 } else { 1 }, n != 0);
        let current = if n == 2 { None } else { Some(&changed) };
        assert!(resolve(
            &mut h,
            &format!("inflight{n}"),
            Decision::Committed,
            current
        )
        .is_err());
        assert_eq!(unchanged(&h), before);
    }
}
#[test]
fn duplicate_once_changed_payload_conflict_and_rejected_k_never_pollutes_p() {
    let mut h = host();
    let a = actor("Alice", 1, true);
    let raw = packet(
        &h,
        "one",
        "Alice",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"one"}),
    );
    h.prepare(&raw, &a).unwrap();
    resolve(&mut h, "one", Decision::Committed, Some(&a)).unwrap();
    let before = unchanged(&h);
    assert!(matches!(h.prepare(&raw, &a).unwrap(), Prepared::Known(_)));
    assert_eq!(unchanged(&h), before);
    let checkpoint = h.checkpoint().unwrap();
    drop(h);
    let mut h = Host::restore(engine(), &checkpoint, "restart-once").unwrap();
    let restored = unchanged(&h);
    assert_eq!(
        (&restored.0, restored.1, restored.2),
        (&before.0, before.1, before.2)
    );
    // Restart installs a fresh trusted generation in checkpoint metadata;
    // subsequent no-op/rejected operations must preserve that checkpoint too.
    let before = restored;
    assert!(matches!(h.prepare(&raw, &a).unwrap(), Prepared::Known(_)));
    assert_eq!(unchanged(&h), before);
    let changed = String::from_utf8(raw)
        .unwrap()
        .replace("\"text\":\"one\"", "\"text\":\"other\"");
    assert_eq!(
        h.prepare(changed.as_bytes(), &a).unwrap_err(),
        "op_id_conflict"
    );
    let raw = packet(
        &h,
        "reject",
        "Alice",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"NO"}),
    );
    h.prepare(&raw, &a).unwrap();
    assert_eq!(
        unchanged(&h),
        before,
        "preparing K cannot change confirmed checkpoint"
    );
    let second = packet(
        &h,
        "other",
        "Alice",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"NO"}),
    );
    assert!(h.prepare(&second, &a).is_err());
    resolve(&mut h, "reject", Decision::Rejected, None).unwrap();
    assert_eq!(unchanged(&h), before);
    assert!(matches!(h.prepare(&raw, &a).unwrap(), Prepared::Known(_)));
}
#[test]
fn participant_undo_a1_b1_a2_rejection_preserves_committed_work_and_overlap_conflicts() {
    let mut h = host();
    commit(
        &mut h,
        "A1",
        "Alice",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"A1"}),
    );
    commit(
        &mut h,
        "B1",
        "Bob",
        "text.insert",
        Some((0, 2, 2)),
        json!({"text":"B1"}),
    );
    commit(
        &mut h,
        "A2",
        "Alice",
        "text.insert",
        Some((0, 4, 4)),
        json!({"text":"A2"}),
    );
    let a = actor("Alice", 1, true);
    let k = packet(
        &h,
        "K",
        "Alice",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"REJECTED"}),
    );
    h.prepare(&k, &a).unwrap();
    resolve(&mut h, "K", Decision::Rejected, None).unwrap();
    commit(
        &mut h,
        "undoA2",
        "Alice",
        "participant.undo",
        None,
        json!({"op_id":"A2"}),
    );
    assert_eq!(text(&h, 0), "A1B1");
    let foreign = packet(
        &h,
        "foreignundo",
        "Alice",
        "participant.undo",
        None,
        json!({"op_id":"B1"}),
    );
    let before = unchanged(&h);
    assert_eq!(h.prepare(&foreign, &a).unwrap_err(), "foreign_undo_denied");
    assert_eq!(unchanged(&h), before);
    commit(
        &mut h,
        "A3",
        "Alice",
        "text.insert",
        Some((0, 4, 4)),
        json!({"text":"A3"}),
    );
    commit(
        &mut h,
        "B2",
        "Bob",
        "text.insert",
        Some((0, 4, 6)),
        json!({"text":"B2"}),
    );
    let before = unchanged(&h);
    let undo = packet(
        &h,
        "overlap",
        "Alice",
        "participant.undo",
        None,
        json!({"op_id":"A3"}),
    );
    assert_eq!(h.prepare(&undo, &a).unwrap_err(), "undo_overlap");
    assert_eq!(unchanged(&h), before);
}
fn rss(pid: u32) -> u64 {
    std::fs::read_to_string(format!("/proc/{pid}/status"))
        .unwrap()
        .lines()
        .find(|s| s.starts_with("VmRSS:"))
        .unwrap()
        .split_whitespace()
        .nth(1)
        .unwrap()
        .parse()
        .unwrap()
}
#[test]
fn bounded_retention_pending_and_cold_load_layout_measurements() {
    let e = engine();
    let pid = e.process_id();
    let mut h = Host::new(e, "document", "generation1", None).unwrap();
    let a = actor("Alice", 1, true);
    let mut elapsed = vec![];
    for n in 0..40 {
        let start = Instant::now();
        commit(
            &mut h,
            &format!("op{n}"),
            "Alice",
            "text.insert",
            Some((0, n, n)),
            json!({"text":"x"}),
        );
        elapsed.push(start.elapsed().as_micros());
    }
    assert_eq!(h.counts(), (32, 0, 40));
    let before = unchanged(&h);
    let raw = packet(
        &h,
        "candidate",
        "Alice",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"pending"}),
    );
    h.prepare(&raw, &a).unwrap();
    assert_eq!(h.counts().1, 1);
    resolve(&mut h, "candidate", Decision::Rejected, None).unwrap();
    assert_eq!(unchanged(&h), before);
    assert_eq!(h.counts().0, 32);
    elapsed.sort_unstable();
    println!(
        "C1_METRICS {}",
        json!({"operations":40,"history_limit":32,"history":h.counts().0,"pending_limit":1,"host_rss_kib":rss(std::process::id()),"helper_rss_kib":rss(pid),"cold_prepare_plus_host_resolve_p95_us":elapsed[elapsed.len()*95/100],"includes_load_and_layout":true,"hard_300mb_300ms_assert":false})
    );
}
#[test]
fn helper_requires_private_fd_and_rejects_global_commands() {
    let output = std::process::Command::new(binary())
        .env_clear()
        .output()
        .unwrap();
    assert!(!output.status.success());
    assert!(output.stdout.is_empty());
    let mut e = engine();
    assert!(e.call(json!({"command":"file.open","params":{"path":"not-read"},"native":null,"target":null,"author":"Alice","time":"2001-02-03T04:05:06Z"})).is_err());
    assert!(e.call(json!({"command":"init","params":{},"native":null,"target":null,"author":"Alice","time":"2001-02-03T04:05:06Z"})).is_ok(),"fully framed refusal leaves process usable");
}

#[test]
fn writer_catalog_changes_actual_native_state_without_changing_other_block() {
    let mut h = host();
    commit(
        &mut h,
        "seed",
        "Alice",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"hello"}),
    );
    for (op, cmd, params) in [
        ("bold", "format.bold", json!({"value":true})),
        ("font", "format.font", json!({"name":"Arial"})),
        ("size", "format.size", json!({"size":18})),
        ("align", "para.align", json!({"value":"center"})),
    ] {
        let before = h.native().clone();
        commit(&mut h, op, "Alice", cmd, Some((0, 0, 5)), params);
        assert_ne!(h.native(), &before);
        assert_eq!(text(&h, 0), "hello");
    }
    commit(
        &mut h,
        "delete",
        "Alice",
        "text.delete",
        Some((0, 1, 2)),
        json!({}),
    );
    assert_eq!(text(&h, 0), "hllo");
    commit(
        &mut h,
        "backspace",
        "Alice",
        "text.backspace",
        Some((0, 1, 1)),
        json!({}),
    );
    assert_eq!(text(&h, 0), "llo");
    let read = packet(&h, "read", "Reader", "document.read", None, json!({}));
    assert!(matches!(
        h.prepare(&read, &actor("Reader", 1, false)).unwrap(),
        Prepared::Read(_)
    ));
}
#[test]
fn killed_actual_helper_cannot_change_confirmed_state() {
    let e = engine();
    let pid = e.process_id();
    let mut h = Host::new(e, "document", "g1", None).unwrap();
    let before = unchanged(&h);
    assert_eq!(unsafe { libc::kill(pid as i32, libc::SIGKILL) }, 0);
    let raw = packet(
        &h,
        "afterdeath",
        "Alice",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"NO"}),
    );
    assert!(h.prepare(&raw, &actor("Alice", 1, true)).is_err());
    assert_eq!(unchanged(&h), before);
    assert!(h.prepare(&raw, &actor("Alice", 1, true)).is_err());
    assert_eq!(unchanged(&h), before);
    assert!(!std::path::Path::new(&format!("/proc/{pid}")).exists());
}

#[test]
fn restart_fences_rejected_and_denied_operations_without_reexecuting_them() {
    for denied_commit in [false, true] {
        let mut h = host();
        let a = actor("Alice", 1, true);
        let known = packet(
            &h,
            "confirmed",
            "Alice",
            "text.insert",
            Some((0, 0, 0)),
            json!({"text":"CONFIRMED"}),
        );
        h.prepare(&known, &a).unwrap();
        resolve(&mut h, "confirmed", Decision::Committed, Some(&a)).unwrap();
        let before = unchanged(&h);
        let old = packet(
            &h,
            "unconfirmed",
            "Alice",
            "text.insert",
            Some((0, 0, 0)),
            json!({"text":"NEVER"}),
        );
        h.prepare(&old, &a).unwrap();
        if denied_commit {
            assert_eq!(
                resolve(
                    &mut h,
                    "unconfirmed",
                    Decision::Committed,
                    Some(&actor("Alice", 1, false))
                )
                .unwrap_err(),
                "commit_admission_denied"
            );
        } else {
            resolve(&mut h, "unconfirmed", Decision::Rejected, None).unwrap();
        }
        assert_eq!(unchanged(&h), before);
        let checkpoint = h.checkpoint().unwrap();
        drop(h);
        let mut h = Host::restore(engine(), &checkpoint, "generation2").unwrap();
        let before = unchanged(&h);
        assert!(matches!(h.prepare(&known, &a).unwrap(), Prepared::Known(_)));
        let mut changed: Value = serde_json::from_slice(&known).unwrap();
        changed["params"]["text"] = json!("CHANGED");
        assert_eq!(
            h.prepare(&serde_json::to_vec(&changed).unwrap(), &a)
                .unwrap_err(),
            "op_id_conflict"
        );
        let mut changed: Value = serde_json::from_slice(&old).unwrap();
        changed["params"]["text"] = json!("CHANGED");
        for raw in [&old, &serde_json::to_vec(&changed).unwrap()] {
            assert_eq!(h.prepare(raw, &a).unwrap_err(), "stale_generation");
            assert_eq!(unchanged(&h), before);
            assert_eq!(h.counts().1, 0);
        }
        let mut forged: Value = serde_json::from_slice(&old).unwrap();
        forged["generation"] = json!(h.generation());
        assert_eq!(
            h.prepare(&serde_json::to_vec(&forged).unwrap(), &a)
                .unwrap_err(),
            "operation_generation_mismatch"
        );
        assert_eq!(unchanged(&h), before);
        assert_eq!(h.counts().1, 0);
        commit(
            &mut h,
            "explicit-new-operation",
            "Alice",
            "text.insert",
            Some((0, 0, 0)),
            json!({"text":"NEW"}),
        );
        assert_eq!(text(&h, 0), "NEWCONFIRMED");
    }
}
