#![cfg(all(target_os = "linux", feature = "wordcraft-command-experiment"))]
use mooring_collab::command_experiment::{
    process::ProcessEngine, Admission, Engine, Host, Prepared,
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
fn packet<E: Engine>(
    h: &Host<E>,
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
fn key<E: Engine>(h: &Host<E>, local: &str) -> String {
    format!("{}:{local}", h.generation())
}
// Test-fixture decisions simulate the trusted host callback; they are NOT SQL evidence.
enum Decision {
    Committed,
    Rejected,
}
fn resolve<E: Engine>(
    h: &mut Host<E>,
    local: &str,
    decision: Decision,
    current: Option<&Admission>,
) -> Result<Value, &'static str> {
    let op = key(h, local);
    match decision {
        Decision::Rejected => h.reject(&op),
        Decision::Committed => {
            let staged = match current
                .ok_or("commit_admission_denied")
                .and_then(|a| h.stage_commit(&op, a))
            {
                Ok(staged) => staged,
                Err(error) => {
                    h.reject(&op).unwrap();
                    return Err(error);
                }
            };
            h.confirm_committed(&staged, current.unwrap())
        }
    }
}
fn commit<E: Engine>(
    h: &mut Host<E>,
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
fn text<E: Engine>(h: &Host<E>, i: usize) -> String {
    h.native()["body"][i]["text"].as_str().unwrap().into()
}
fn unchanged<E: Engine>(h: &Host<E>) -> (Value, u64, usize, Vec<u8>) {
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

// Observation wrapper around the REAL pinned helper, not an engine mock.
struct CountedEngine {
    actual: ProcessEngine,
    calls: std::sync::Arc<std::sync::atomic::AtomicUsize>,
}
impl Engine for CountedEngine {
    fn call(&mut self, request: Value) -> Result<Value, &'static str> {
        self.calls.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        self.actual.call(request)
    }
}
fn counted_engine() -> (
    CountedEngine,
    std::sync::Arc<std::sync::atomic::AtomicUsize>,
) {
    let calls = std::sync::Arc::new(std::sync::atomic::AtomicUsize::new(0));
    (
        CountedEngine {
            actual: engine(),
            calls: calls.clone(),
        },
        calls,
    )
}
fn call_count(calls: &std::sync::atomic::AtomicUsize) -> usize {
    calls.load(std::sync::atomic::Ordering::SeqCst)
}

#[test]
fn staged_checkpoint_is_immutable_until_trusted_ack_and_ack_is_idempotent() {
    let (e, calls) = counted_engine();
    let mut h = Host::new(e, "document", "generation1", None).unwrap();
    let jana = actor("Jana", 1, true);
    let before = unchanged(&h);
    let raw = packet(
        &h,
        "insert",
        "Jana",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"Jana"}),
    );
    h.prepare(&raw, &jana).unwrap();
    let executed = call_count(&calls);
    let staged = h.stage_commit(&key(&h, "insert"), &jana).unwrap();
    assert_eq!(unchanged(&h), before);
    assert_eq!(h.counts(), (0, 1, 0));
    assert_eq!(
        call_count(&calls),
        executed,
        "staging must not execute engine again"
    );
    let again = h.stage_commit(staged.op_id(), &jana).unwrap();
    assert_eq!(again.checkpoint(), staged.checkpoint());
    assert_eq!(again.candidate_receipt(), staged.candidate_receipt());
    assert_eq!(again.checkpoint_digest(), staged.checkpoint_digest());
    assert_eq!(
        staged.candidate_receipt()["fingerprint"],
        staged.fingerprint()
    );
    assert_eq!(staged.base_revision(), 0);
    assert_eq!(staged.revision(), 1);
    assert_eq!(staged.document(), "document");
    assert_eq!(staged.generation(), "generation1");
    assert_eq!(staged.format(), "wordcraft-c1-v2");
    assert_eq!(staged.engine_identity()["catalog"], "wordcraft-c1-10-v1");
    assert_ne!(staged.base_checkpoint_digest(), staged.checkpoint_digest());
    // This explicitly SIMULATES a trusted SQL-success callback. No SQL proof.
    let result = h.confirm_committed(&staged, &jana).unwrap();
    assert_eq!(result, *staged.candidate_receipt());
    assert_eq!(h.checkpoint().unwrap(), staged.checkpoint());
    assert_eq!(h.revision(), 1);
    assert_eq!(h.counts(), (1, 0, 1));
    assert_eq!(text(&h, 0), "Jana");
    assert_eq!(h.confirm_committed(&staged, &jana).unwrap(), result);
    assert!(matches!(h.prepare(&raw, &jana).unwrap(), Prepared::Known(value) if value == result));
    assert_eq!(
        call_count(&calls),
        executed,
        "ACK and known retries must not execute engine"
    );
}

#[test]
fn committed_stage_restores_after_crash_before_ack_without_reexecution() {
    let (e, calls) = counted_engine();
    let mut h = Host::new(e, "document", "generation1", None).unwrap();
    let jana = actor("Jana", 1, true);
    let raw = packet(
        &h,
        "before-crash",
        "Jana",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"Confirmed after SQL"}),
    );
    let unknown = packet(
        &h,
        "unknown-old",
        "Jana",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"MUST NOT REEXECUTE"}),
    );
    h.prepare(&raw, &jana).unwrap();
    let staged = h.stage_commit(&key(&h, "before-crash"), &jana).unwrap();
    let executed = call_count(&calls);
    assert_eq!(h.revision(), 0);
    assert_eq!(call_count(&calls), executed);
    let canonical_bytes = staged.checkpoint().to_vec();
    let canonical_receipt = staged.candidate_receipt().clone();
    // These are the bytes a REAL host must atomically commit with the receipt.
    // The test simulates committed canonical input, not a database transaction.
    drop(h); // Crash BEFORE confirm_committed, after the simulated SQL boundary.
    let (e, restored_calls) = counted_engine();
    let mut restored = Host::restore(e, &canonical_bytes, "generation2").unwrap();
    let initialized = call_count(&restored_calls);
    assert_eq!(restored.revision(), 1);
    assert_eq!(text(&restored, 0), "Confirmed after SQL");
    assert_eq!(restored.counts(), (1, 0, 1));
    assert!(
        matches!(restored.prepare(&raw, &jana).unwrap(), Prepared::Known(value) if value == canonical_receipt)
    );
    assert_eq!(restored.prepare(&unknown, &jana), Err("stale_generation"));
    let mut conflict: Value = serde_json::from_slice(&raw).unwrap();
    conflict["params"]["text"] = json!("changed known identity");
    assert_eq!(
        restored.prepare(&serde_json::to_vec(&conflict).unwrap(), &jana),
        Err("op_id_conflict")
    );
    assert_eq!(
        call_count(&restored_calls),
        initialized,
        "known/stale/conflicting operations must not execute"
    );
}

#[test]
fn denied_ack_unknown_store_outcome_and_rejection_preserve_p_and_participant_undo() {
    let (e, calls) = counted_engine();
    let mut h = Host::new(e, "document", "generation1", None).unwrap();
    commit(
        &mut h,
        "jana",
        "Jana",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"Jana"}),
    );
    let before = unchanged(&h);
    let agent = actor("Agent", 1, true);
    let raw = packet(
        &h,
        "agent",
        "Agent",
        "text.insert",
        Some((0, 4, 4)),
        json!({"text":" rejected candidate"}),
    );
    h.prepare(&raw, &agent).unwrap();
    let staged = h.stage_commit(&key(&h, "agent"), &agent).unwrap();
    let executed = call_count(&calls);
    for denied in [
        actor("Agent", 1, false),
        actor("Agent", 2, true),
        actor("Jana", 1, true),
        Admission::new("Agent", "changed author", "2001-02-03T04:05:06Z", 1, true).unwrap(),
    ] {
        assert_eq!(
            h.confirm_committed(&staged, &denied),
            Err("commit_admission_denied")
        );
        assert_eq!(unchanged(&h), before);
        assert_eq!(h.counts().1, 1, "failed ACK retains unknown candidate");
    }
    // A SQL error/unknown MUST NOT invoke the success callback or discard K.
    // Here no SQL is executed. Exercise the required blocked successor behavior.
    let successor = packet(
        &h,
        "successor",
        "Jana",
        "text.insert",
        Some((0, 4, 4)),
        json!({"text":"never"}),
    );
    assert_eq!(
        h.prepare(&successor, &actor("Jana", 1, true)),
        Err("pending_limit")
    );
    assert!(matches!(
        h.prepare(&raw, &agent).unwrap(),
        Prepared::Pending { .. }
    ));
    assert_eq!(unchanged(&h), before);
    assert_eq!(call_count(&calls), executed);
    // Simulate a DEFINITIVE host rollback/rejection, not an unknown SQL outcome.
    h.reject(staged.op_id()).unwrap();
    assert_eq!(unchanged(&h), before);
    assert_eq!(h.counts().1, 0);
    assert!(
        matches!(h.prepare(&raw,&agent).unwrap(), Prepared::Known(v) if v["state"]=="rejected")
    );
    // M2 rejection did not destroy Jana's confirmed undo record.
    commit(
        &mut h,
        "undo-jana",
        "Jana",
        "participant.undo",
        None,
        json!({"op_id":"jana"}),
    );
    assert_eq!(text(&h, 0), "");
}

#[test]
fn staged_handle_cannot_confirm_another_candidate_document_or_base() {
    let jana = actor("Jana", 1, true);
    let mut a = host();
    let raw = packet(
        &a,
        "same-op",
        "Jana",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"A"}),
    );
    a.prepare(&raw, &jana).unwrap();
    let staged = a.stage_commit(&key(&a, "same-op"), &jana).unwrap();
    let mut b = host();
    let other = packet(
        &b,
        "same-op",
        "Jana",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"B"}),
    );
    b.prepare(&other, &jana).unwrap();
    let before = unchanged(&b);
    assert_eq!(
        b.confirm_committed(&staged, &jana),
        Err("commit_binding_mismatch")
    );
    assert_eq!(unchanged(&b), before);
    assert_eq!(b.counts().1, 1);
    let mut foreign = Host::new(engine(), "other-document", "generation1", None).unwrap();
    assert_eq!(
        foreign.confirm_committed(&staged, &jana),
        Err("commit_binding_mismatch")
    );
    // A rejected handle cannot later overwrite a different pending original.
    a.reject(staged.op_id()).unwrap();
    let next = packet(
        &a,
        "new-op",
        "Jana",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"new"}),
    );
    a.prepare(&next, &jana).unwrap();
    let before = unchanged(&a);
    assert_eq!(
        a.confirm_committed(&staged, &jana),
        Err("commit_binding_mismatch")
    );
    assert_eq!(unchanged(&a), before);
}

#[test]
fn restored_generation_keeps_exact_sql_base_digest_for_next_commit() {
    let mut h = host();
    let jana = actor("Jana", 1, true);
    let raw = packet(
        &h,
        "first",
        "Jana",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"one"}),
    );
    h.prepare(&raw, &jana).unwrap();
    let first = h.stage_commit(&key(&h, "first"), &jana).unwrap();
    let canonical_bytes = first.checkpoint().to_vec();
    let canonical_digest = first.checkpoint_digest().to_string();
    drop(h); // Simulated SQL-committed checkpoint, before Rust success callback.
    let mut restored = Host::restore(engine(), &canonical_bytes, "generation2").unwrap();
    assert_eq!(restored.canonical_checkpoint_digest(), canonical_digest);
    assert_ne!(
        restored.checkpoint().unwrap(),
        canonical_bytes,
        "runtime generation rotated, SQL bytes did not"
    );
    let next = packet(
        &restored,
        "next",
        "Jana",
        "text.insert",
        Some((0, 3, 3)),
        json!({"text":" two"}),
    );
    restored.prepare(&next, &jana).unwrap();
    let staged = restored
        .stage_commit(&key(&restored, "next"), &jana)
        .unwrap();
    assert_eq!(
        staged.base_checkpoint_digest(),
        canonical_digest,
        "SQL CAS must compare the actual stored original checkpoint"
    );
    assert_eq!(staged.base_revision(), 1);
    let before = unchanged(&restored);
    assert_eq!(
        restored.confirm_committed(&staged, &actor("Jana", 2, true)),
        Err("commit_admission_denied")
    );
    assert_eq!(restored.canonical_checkpoint_digest(), canonical_digest);
    assert_eq!(unchanged(&restored), before);
    // Explicit simulated SQL success callback. No real SQL is executed here.
    restored.confirm_committed(&staged, &jana).unwrap();
    assert_eq!(
        restored.canonical_checkpoint_digest(),
        staged.checkpoint_digest()
    );
    assert_eq!(restored.checkpoint().unwrap(), staged.checkpoint());
    assert_eq!(text(&restored, 0), "one two");
}

#[test]
fn malformed_actual_engine_selection_never_prepares_or_publishes_candidate() {
    struct CorruptSelection {
        inner: ProcessEngine,
        mode: u8,
    }
    impl Engine for CorruptSelection {
        fn call(&mut self, request: Value) -> Result<Value, &'static str> {
            let mutation = request["command"] == "text.insert";
            let mut value = self.inner.call(request)?;
            if mutation {
                match self.mode {
                    0 => {
                        value.as_object_mut().unwrap().remove("result");
                    }
                    1 => value["result"] = json!({}),
                    2 => {
                        value["result"] = json!({"anchor":{"story":"body","path":[0],"off":1},"focus":{"story":"body","path":[0],"off":1}})
                    }
                    _ => {
                        value["result"] = json!({"anchor":{"story":"body","path":[999],"off":0},"focus":{"story":"body","path":[999],"off":0}})
                    }
                }
            }
            Ok(value)
        }
    }
    for mode in 0..4 {
        let mut h = Host::new(
            CorruptSelection {
                inner: engine(),
                mode,
            },
            "document",
            "generation1",
            None,
        )
        .unwrap();
        let before = unchanged(&h);
        let input = packet(
            &h,
            "bad-selection",
            "Alice",
            "text.insert",
            Some((0, 0, 0)),
            json!({"text":"č"}),
        );
        assert!(h.prepare(&input, &actor("Alice", 1, true)).is_err());
        assert_eq!(unchanged(&h), before);
        assert!(h
            .stage_commit(&key(&h, "bad-selection"), &actor("Alice", 1, true))
            .is_err());
    }
}
