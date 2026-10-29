//! Generated tests call the pinned C1 Host and its ACTUAL private-FD engine.
use mooring_collab::command_experiment::{
    process::ProcessEngine, Admission, Decision, Host, Prepared,
};
use serde_json::{json, Value};
use std::{
    collections::BTreeMap,
    fs,
    panic::{catch_unwind, AssertUnwindSafe},
    path::PathBuf,
};

type H = Host<ProcessEngine>;
fn actor(name: &str) -> Admission {
    Admission::new(name, name, "2001-02-03T04:05:06Z", 1, true).unwrap()
}
fn raw(
    h: &H,
    op: &str,
    who: &str,
    command: &str,
    block: Option<(usize, usize, usize)>,
    params: Value,
) -> Value {
    json!({"generation":h.generation(), "op_id":format!("{}:{op}",h.generation()),"participant":who,"base_revision":h.revision(),"command":command,"params":params,"target":block.map(|(i,start,end)|json!({"block_id":h.ids()[i],"unit":"utf8_bytes","start":start,"end":end}))})
}
fn prepare(h: &mut H, value: &Value, who: &str) -> Result<Prepared, &'static str> {
    h.prepare(&serde_json::to_vec(value).unwrap(), &actor(who))
}
fn commit(
    h: &mut H,
    op: &str,
    who: &str,
    command: &str,
    target: Option<(usize, usize, usize)>,
    params: Value,
) {
    let value = raw(h, op, who, command, target, params);
    assert!(matches!(
        prepare(h, &value, who).unwrap(),
        Prepared::Pending { .. }
    ));
    assert_eq!(
        h.resolve(
            value["op_id"].as_str().unwrap(),
            Decision::Committed,
            Some(&actor(who))
        )
        .unwrap()["state"],
        "host_committed"
    );
}
fn seed(case: &Value) -> H {
    let binary = PathBuf::from(
        std::env::var_os("MOORING_C1_ENGINE_BINARY").expect("actual C1 helper required"),
    );
    let mut h = Host::new(
        ProcessEngine::spawn(&binary).expect("actual private-FD engine"),
        "document",
        "generation1",
        None,
    )
    .unwrap();
    commit(
        &mut h,
        "seed",
        "Alice",
        "text.insert",
        Some((0, 0, 0)),
        json!({"text":"Alpha"}),
    );
    commit(
        &mut h,
        "paragraph",
        "Alice",
        "text.newParagraph",
        Some((0, 5, 5)),
        json!({}),
    );
    commit(
        &mut h,
        "peer",
        "Bob",
        "text.insert",
        Some((1, 0, 0)),
        json!({"text":"Peer"}),
    );
    for (index, setup) in case["setup"].as_array().unwrap().iter().enumerate() {
        commit(
            &mut h,
            &format!("fixture-setup-{index}"),
            "Alice",
            setup["command"].as_str().unwrap(),
            Some((0, 1, 3)),
            setup["params"].clone(),
        );
    }
    h
}
fn strings(native: &Value) -> Vec<&str> {
    native["body"]
        .as_array()
        .unwrap()
        .iter()
        .map(|p| p["text"].as_str().unwrap())
        .collect()
}
fn props(native: &Value) -> Vec<Value> {
    let mut result = vec![];
    for run in native["body"][0]["runs"].as_array().unwrap() {
        for _ in 0..run["len"].as_u64().unwrap() {
            result.push(run.get("props").cloned().unwrap_or(json!({})));
        }
    }
    result
}
fn assert_expected(actual: &Value, expected: &Value) {
    if expected.is_number() {
        assert_eq!(actual.as_f64(), expected.as_f64());
    } else if let Some(object) = expected.as_object() {
        assert_eq!(
            actual.as_object().unwrap().keys().collect::<Vec<_>>(),
            object.keys().collect::<Vec<_>>()
        );
        for (key, value) in object {
            assert_expected(&actual[key], value);
        }
    } else if let Some(array) = expected.as_array() {
        assert_eq!(actual.as_array().unwrap().len(), array.len());
        for (left, right) in actual.as_array().unwrap().iter().zip(array) {
            assert_expected(left, right);
        }
    } else {
        assert_eq!(actual, expected);
    }
}
fn declarative_effect(case: &Value, before: &Value, after: &Value) {
    let oracle = &case["oracle"];
    let expected = &oracle["expected"];
    let mut wanted = before.clone();
    match oracle["kind"].as_str().expect("missing_effect_oracle") {
        "text" => {
            assert_eq!(strings(before), vec!["Alpha", "Peer"]);
            wanted["body"] = Value::Array(
                expected
                    .as_array()
                    .unwrap()
                    .iter()
                    .map(|text| {
                        let text = text.as_str().unwrap();
                        json!({"kind":"para","text":text,"runs":[{"len":text.len()}]})
                    })
                    .collect(),
            );
        }
        "character" => {
            let old = props(before);
            assert_eq!(old.len(), 5);
            let mut runs: Vec<Value> = vec![];
            for (i, original) in old.iter().enumerate() {
                let mut properties = original.clone();
                if (1..3).contains(&i) {
                    for (field, value) in expected.as_object().unwrap() {
                        properties
                            .as_object_mut()
                            .unwrap()
                            .insert(field.clone(), value.clone());
                    }
                }
                let same = runs.last().is_some_and(|run| {
                    run.get("props").cloned().unwrap_or(json!({})) == properties
                });
                if same {
                    let run = runs.last_mut().unwrap();
                    run["len"] = json!(run["len"].as_u64().unwrap() + 1);
                } else {
                    let mut run = json!({"len":1});
                    if properties != json!({}) {
                        run["props"] = properties;
                    }
                    runs.push(run);
                }
            }
            wanted["body"][0]["runs"] = Value::Array(runs);
        }
        "paragraph" | "list" => {
            let block = wanted["body"][0].as_object_mut().unwrap();
            let properties = block
                .entry("props")
                .or_insert(json!({}))
                .as_object_mut()
                .unwrap();
            for (field, value) in expected.as_object().unwrap() {
                properties.insert(field.clone(), value.clone());
            }
            if oracle["kind"] == "list" {
                assert_eq!(before["numbering"], json!({"abstracts":[],"nums":[]}));
                wanted["numbering"] = oracle["numbering"].clone();
            }
        }
        _ => panic!("missing_effect_oracle"),
    }
    // Compare the entire native document, including all selected properties,
    // runs, objects, peer blocks and complete numbering definitions.
    assert_expected(after, &wanted);
}
fn effect(case: &Value, before: &Value, after: &Value) {
    assert_ne!(before, after, "qualification cannot pass a no-op");
    declarative_effect(case, before, after);
}
fn oracle_integrity(case: &Value, before: &Value, after: &Value) {
    for kind in 0..5 {
        let mut damaged = after.clone();
        match kind {
            0 => damaged["body"][0]["props"]["unreviewed"] = json!(true),
            1 => damaged["body"][0]["runs"][0]["props"]["unreviewed"] = json!(true),
            2 => damaged["body"][0]["text"] = json!("damaged"),
            3 => damaged["numbering"]["unreviewed"] = json!([1]),
            _ => {
                let section = damaged["lastSection"].as_object_mut().unwrap();
                assert_eq!(section.remove("docGrid"), Some(Value::Null));
                section.insert("unreviewedNull".into(), Value::Null);
            }
        }
        assert!(
            std::panic::catch_unwind(|| effect(case, before, &damaged)).is_err(),
            "oracle accepted unrelated native damage"
        );
    }
}

fn exercise(case: &Value, checks: &mut BTreeMap<&str, bool>) {
    let mut h = seed(case);
    let initial = h.native().clone();
    let checkpoint = h.checkpoint().unwrap();
    let rev = h.revision();
    let ids = h.ids().to_vec();
    let history = h.counts().0;
    let command = case["id"].as_str().unwrap();
    let start = case["start"].as_u64().unwrap() as usize;
    let end = case["end"].as_u64().unwrap() as usize;
    let request = raw(
        &h,
        "rejected",
        "Alice",
        command,
        Some((0, start, end)),
        case["params"].clone(),
    );
    assert!(
        matches!(
            prepare(&mut h, &request, "Alice").unwrap(),
            Prepared::Pending { .. }
        ),
        "actual C1 must execute this command before qualification"
    );
    assert_eq!(h.native(), &initial);
    assert_eq!(
        h.checkpoint().unwrap(),
        checkpoint,
        "K must not replace confirmed P"
    );
    assert_eq!(
        h.resolve(request["op_id"].as_str().unwrap(), Decision::Rejected, None)
            .unwrap()["state"],
        "rejected"
    );
    assert_eq!(h.native(), &initial);
    assert_eq!(h.checkpoint().unwrap(), checkpoint);
    assert_eq!(h.ids(), ids);
    assert_eq!(h.revision(), rev);
    assert_eq!(h.counts().0, history);
    let read = raw(
        &h,
        "read-after-reject",
        "Alice",
        "document.read",
        None,
        json!({}),
    );
    let Prepared::Read(read) = prepare(&mut h, &read, "Alice").unwrap() else {
        panic!("actual engine read required")
    };
    assert_eq!(read["result"]["text"], "Alpha\nPeer");
    // A different subsequent edit must equal a fresh engine loaded from P.
    // Its explicit committed undo is cleanup, NOT rollback of rejected K.
    let binary = PathBuf::from(std::env::var_os("MOORING_C1_ENGINE_BINARY").unwrap());
    let mut control = Host::new(
        ProcessEngine::spawn(&binary).unwrap(),
        "control",
        "generation2",
        Some(initial.clone()),
    )
    .unwrap();
    commit(
        &mut h,
        "probe",
        "Bob",
        "text.insert",
        Some((1, 4, 4)),
        json!({"text":"probe"}),
    );
    commit(
        &mut control,
        "probe",
        "Bob",
        "text.insert",
        Some((1, 4, 4)),
        json!({"text":"probe"}),
    );
    assert_eq!(
        h.native(),
        control.native(),
        "post-rejection edit diverges from fresh confirmed P"
    );
    commit(
        &mut h,
        "probe-cleanup",
        "Bob",
        "participant.undo",
        None,
        json!({"op_id":"generation1:probe"}),
    );
    assert_eq!(h.native(), &initial);
    assert_eq!(h.ids(), ids);
    checks.insert("restoreP", true);
    commit(
        &mut h,
        "change",
        "Alice",
        command,
        Some((0, start, end)),
        case["params"].clone(),
    );
    effect(case, &initial, h.native());
    oracle_integrity(case, &initial, h.native());
    checks.insert("oracleIntegrity", true);
    let untouched_peer = h.ids().iter().position(|id| id == &ids[1]).unwrap();
    assert_eq!(
        h.native()["body"][untouched_peer],
        initial["body"][1],
        "A's command changed the peer's native block"
    );
    let mut header_before = initial.clone();
    header_before.as_object_mut().unwrap().remove("body");
    let mut header_after = h.native().clone();
    header_after.as_object_mut().unwrap().remove("body");
    if let Some(allowed) = case["oracle"]["allowedHeaderKeys"].as_array() {
        assert_eq!(case["oracle"]["kind"], "list");
        for key in allowed {
            let key = key.as_str().unwrap();
            assert_eq!(
                key, "numbering",
                "only reviewed list definitions may change header"
            );
            header_before.as_object_mut().unwrap().remove(key);
            header_after.as_object_mut().unwrap().remove(key);
        }
    }
    assert_eq!(
        header_after, header_before,
        "this tested C1 profile must not change unrelated native metadata"
    );
    checks.insert("execution", true);
    let peer_id = ids[1].clone();
    let peer = h.ids().iter().position(|id| id == &peer_id).unwrap();
    commit(
        &mut h,
        "peer-change",
        "Bob",
        "text.insert",
        Some((peer, 4, 4)),
        json!({"text":"!"}),
    );
    let stale = raw(
        &h,
        "stale",
        "Alice",
        command,
        Some((0, start, end)),
        case["params"].clone(),
    );
    let mut stale = stale;
    stale["base_revision"] = json!(rev);
    let prior = h.checkpoint().unwrap();
    assert_eq!(
        prepare(&mut h, &stale, "Alice").unwrap_err(),
        "stale_revision"
    );
    assert_eq!(h.checkpoint().unwrap(), prior);
    // A real block ID becomes stale when Bob splits then joins his own paragraph.
    commit(
        &mut h,
        "temporary-block",
        "Bob",
        "text.newParagraph",
        Some((peer, 5, 5)),
        json!({}),
    );
    let gone = h.ids()[peer + 1].clone();
    commit(
        &mut h,
        "remove-temporary",
        "Bob",
        "text.backspace",
        Some((peer + 1, 0, 0)),
        json!({}),
    );
    let mut absent = raw(
        &h,
        "gone",
        "Alice",
        command,
        Some((0, start, end)),
        case["params"].clone(),
    );
    absent["target"]["block_id"] = json!(gone);
    let prior = h.checkpoint().unwrap();
    assert_eq!(
        prepare(&mut h, &absent, "Alice").unwrap_err(),
        "target_gone"
    );
    assert_eq!(h.checkpoint().unwrap(), prior);
    checks.insert("staleTarget", true);
    // Foreign actor must not undo Alice's command, even when the op_id is known.
    let foreign = raw(
        &h,
        "foreign-undo",
        "Bob",
        "participant.undo",
        None,
        json!({"op_id":"generation1:change"}),
    );
    assert_eq!(
        prepare(&mut h, &foreign, "Bob").unwrap_err(),
        "foreign_undo_denied"
    );
    assert_eq!(h.checkpoint().unwrap(), prior);
    let mut expected = initial.clone();
    expected["body"][1] = h.native()["body"][peer].clone();
    commit(
        &mut h,
        "own-undo",
        "Alice",
        "participant.undo",
        None,
        json!({"op_id":"generation1:change"}),
    );
    assert_eq!(
        h.native(),
        &expected,
        "undo must preserve Bob's exact independent paragraph and metadata"
    );
    assert_eq!(h.ids(), ids);
    checks.insert("ownUndo", true);
}
fn file_sha(path: &std::path::Path) -> String {
    let output = std::process::Command::new("sha256sum")
        .arg(path)
        .output()
        .expect("sha256sum required");
    assert!(
        output.status.success(),
        "could not hash qualification input"
    );
    let text = String::from_utf8(output.stdout).unwrap();
    let value = text.split_whitespace().next().unwrap().to_string();
    assert!(value.len() == 64 && value.bytes().all(|c| c.is_ascii_hexdigit()));
    value
}
fn qualify(id: &str) {
    // Embedded at compilation, not loaded from a potentially newer run at runtime.
    let preparation: Value =
        serde_json::from_str(include_str!("../../../../preparation.json")).unwrap();
    let binary = PathBuf::from(
        std::env::var_os("MOORING_C1_ENGINE_BINARY").expect("actual helper required"),
    );
    let actual_binary = file_sha(&binary);
    assert_eq!(
        preparation["engineBinarySha256"], actual_binary,
        "qualification helper binary drift"
    );
    let cases: Vec<Value> = serde_json::from_str(include_str!("ce_catalog_cases.json")).unwrap();
    let case = cases.iter().find(|case| case["id"] == id).unwrap();
    let mut checks = BTreeMap::from([
        ("execution", false),
        ("oracleIntegrity", false),
        ("staleTarget", false),
        ("restoreP", false),
        ("ownUndo", false),
    ]);
    let result = catch_unwind(AssertUnwindSafe(|| exercise(case, &mut checks)));
    let reason = result.as_ref().err().map(|error| {
        error
            .downcast_ref::<String>()
            .cloned()
            .or_else(|| error.downcast_ref::<&str>().map(|s| s.to_string()))
            .unwrap_or_else(|| "qualification failed".into())
    });
    let directory = PathBuf::from(
        std::env::var_os("WORDCRAFT_CE_QUALIFICATION_OUTPUT")
            .expect("qualification output directory required"),
    );
    fs::create_dir_all(&directory).unwrap();
    let file = directory.join(format!("{}.json", id));
    fs::write(file,serde_json::to_vec_pretty(&json!({"id":id,"group":case["group"],"params":case["params"],"parameterSchemaSha256":case["parameterSchemaSha256"],"test":format!("ce_{}",id.replace('.',"_").to_ascii_lowercase()),"checks":checks,"passed":result.is_ok(),"reason":reason,"executionIdentity":preparation,"actualEngineBinarySha256":actual_binary,"invocationId":std::env::var("WORDCRAFT_CE_INVOCATION_ID").expect("qualification invocation ID required")})).unwrap()).unwrap();
    assert!(
        result.is_ok(),
        "{id}: qualification failed; see structured row (never skipped)"
    );
}
