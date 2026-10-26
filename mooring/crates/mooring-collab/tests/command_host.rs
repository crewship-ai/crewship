#![cfg(all(target_os = "linux", feature = "wordcraft-command-experiment"))]
//! Actual daemon -> inherited private FD -> pinned native engine. SQL success
//! callbacks below are simulations, not SQL transaction durability evidence.
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::{
    io::{Read, Write},
    os::{
        fd::AsRawFd,
        unix::{net::UnixStream, process::CommandExt},
    },
    path::PathBuf,
    process::{Child, Command, Stdio},
    time::Duration,
};
struct Peer {
    child: Child,
    stream: Option<UnixStream>,
    sequence: u64,
}
impl Peer {
    fn spawn(pin: Option<&str>) -> Self {
        let engine = PathBuf::from(
            std::env::var_os("MOORING_C1_ENGINE_BINARY")
                .expect("actual pinned helper required; no skips"),
        );
        let hash = format!(
            "sha256:{:x}",
            Sha256::digest(std::fs::read(&engine).unwrap())
        );
        let (stream, child_socket) = UnixStream::pair().unwrap();
        let fd = child_socket.as_raw_fd();
        stream
            .set_read_timeout(Some(Duration::from_secs(15)))
            .unwrap();
        let mut c = Command::new(env!("CARGO_BIN_EXE_mooring-wordcraft-command-host"));
        c.env_clear()
            .env("MOORING_C1_HOST_FD", "3")
            .env("LANG", "C.UTF-8")
            .args([
                "--engine",
                engine.to_str().unwrap(),
                "--engine-sha256",
                pin.unwrap_or(&hash),
                "--runtime-ref",
                "test-native-runtime",
            ])
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::piped());
        unsafe {
            c.pre_exec(move || {
                if fd != 3 && libc::dup2(fd, 3) < 0 || libc::fcntl(3, libc::F_SETFD, 0) < 0 {
                    return Err(std::io::Error::last_os_error());
                }
                Ok(())
            });
        }
        let child = c.spawn().unwrap();
        drop(child_socket);
        Self {
            child,
            stream: Some(stream),
            sequence: 0,
        }
    }
    fn rpc(&mut self, mut request: Value) -> Value {
        self.sequence += 1;
        request["protocol"] = json!(1);
        request["request_id"] = json!(format!("request-{}", self.sequence));
        let raw = serde_json::to_vec(&request).unwrap();
        self.send(&raw);
        let reply = self.read();
        assert_eq!(reply["protocol"], 1);
        assert_eq!(reply["request_id"], request["request_id"]);
        reply
    }
    fn send(&mut self, raw: &[u8]) {
        let s = self.stream.as_mut().unwrap();
        s.write_all(&(raw.len() as u32).to_be_bytes()).unwrap();
        s.write_all(raw).unwrap();
    }
    fn read(&mut self) -> Value {
        let s = self.stream.as_mut().unwrap();
        let mut header = [0; 4];
        s.read_exact(&mut header).unwrap();
        let n = u32::from_be_bytes(header) as usize;
        assert!(n > 0 && n <= 32 * 1024 * 1024);
        let mut raw = vec![0; n];
        s.read_exact(&mut raw).unwrap();
        serde_json::from_slice(&raw).unwrap()
    }
    fn good(&mut self, request: Value) -> Value {
        let r = self.rpc(request);
        assert_eq!(r["ok"], true, "fixed reply: {r}");
        assert_eq!(r.as_object().unwrap().len(), 4);
        r["value"].clone()
    }
    fn init(&mut self) -> Value {
        self.good(json!({"kind":"init","document":"document","generation":"generation1"}))
    }
    fn projection(&mut self) -> Value {
        self.good(json!({"kind":"projection"}))
    }
    fn close(mut self) {
        drop(self.stream.take());
        assert!(self.child.wait().unwrap().success());
    }
}
impl Drop for Peer {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}
fn admission(writer: bool) -> Value {
    json!({"participant":"jana","author":"Jana","time":"2001-02-03T04:05:06Z","epoch":1,"writer":writer})
}
fn stage_request(meta: &Value, projection: &Value, op: &str, text: &str) -> Value {
    let checkpoint = meta["checkpoint_json"].as_str().unwrap();
    let base_digest = format!("sha256:{:x}", Sha256::digest(checkpoint.as_bytes()));
    let input = json!({"generation":projection["generation"],"op_id":format!("{}:{op}",projection["generation"].as_str().unwrap()),"participant":"jana","base_revision":projection["revision"],"target":{"block_id":projection["ids"][0],"unit":"utf8_bytes","start":0,"end":0},"command":"text.insert","params":{"text":text}});
    json!({"kind":"stage","base_revision":projection["revision"],"base_digest":base_digest,"input_json":serde_json::to_string(&input).unwrap(),"admission":admission(true)})
}
fn confirm(stage: &Value) -> Value {
    json!({"kind":"confirm","stage_ref":stage["stage_ref"],"admission":admission(true)})
}
fn status(stage: &Value) -> Value {
    json!({"kind":"status","op_id":stage["op_id"],"expected_fingerprint":stage["fingerprint"]})
}
#[test]
fn actual_private_host_stages_confirms_rejects_and_keeps_status_pure() {
    let mut p = Peer::spawn(None);
    let m = p.init();
    let before = p.projection();
    assert_eq!(m["format"], "wordcraft-c1-v2");
    assert_eq!(m["catalog"], "wordcraft-c1-10-v1");
    let request = stage_request(&m, &before, "insert", "Ahoj");
    let stage = p.good(request.clone());
    assert_eq!(stage["state"], "staged");
    assert_eq!(stage["base_revision"], 0);
    assert_eq!(stage["revision"], 1);
    assert_eq!(
        stage["digest"],
        format!(
            "sha256:{:x}",
            Sha256::digest(stage["checkpoint_json"].as_str().unwrap().as_bytes())
        )
    );
    assert_eq!(p.projection(), before, "K leaked into confirmed projection");
    assert_eq!(
        p.good(request.clone()),
        stage,
        "exact pending must retain same opaque handle"
    );
    assert_eq!(p.good(status(&stage))["state"], "pending");
    assert_eq!(p.projection(), before);
    let wrong = p.rpc(json!({"kind":"confirm","stage_ref":"foreign","admission":admission(true)}));
    assert_eq!(wrong["error"], "unknown_stage");
    assert_eq!(wrong["no_candidate"], false);
    assert_eq!(p.projection(), before);
    let mut denied = confirm(&stage);
    denied["admission"] = admission(false);
    assert_eq!(p.rpc(denied)["ok"], false);
    assert_eq!(p.good(status(&stage))["state"], "pending");
    for (field, value) in [
        ("participant", json!("other")),
        ("author", json!("Other")),
        ("epoch", json!(2)),
    ] {
        let mut denied = confirm(&stage);
        denied["admission"][field] = value;
        assert_eq!(p.rpc(denied)["error"], "admission_denied");
        assert_eq!(p.projection(), before);
        assert_eq!(p.good(status(&stage))["state"], "pending");
    }
    // Simulated trusted SQL-success ACK. No SQL durability claim in this test.
    let saved = p.good(confirm(&stage));
    assert_eq!(saved["receipt_json"], stage["receipt_json"]);
    assert_eq!(p.good(confirm(&stage)), saved);
    assert_eq!(p.good(status(&stage))["state"], "committed");
    let after = p.projection();
    assert_eq!(after["revision"], 1);
    assert_ne!(after["native_json"], before["native_json"]);
    let mut next_meta = m.clone();
    next_meta["checkpoint_json"] = stage["checkpoint_json"].clone();
    let next = p.good(stage_request(&next_meta, &after, "second", "B"));
    assert_eq!(
        p.rpc(json!({"kind":"reject","stage_ref":"foreign"}))["error"],
        "unknown_stage"
    );
    assert_eq!(p.good(status(&next))["state"], "pending");
    assert_eq!(
        p.good(json!({"kind":"reject","stage_ref":next["stage_ref"]}))["state"],
        "rejected"
    );
    assert_eq!(p.good(status(&next))["state"], "rejected");
    assert_eq!(p.projection(), after);
    p.close();
}
#[test]
fn actual_private_host_restore_sql_committed_candidate_without_reexecution() {
    let mut p = Peer::spawn(None);
    let m = p.init();
    let before = p.projection();
    let original = stage_request(&m, &before, "durable", "SQL committed, ACK lost");
    let stage = p.good(original.clone());
    // The canonical host would atomically store these exact two strings in SQL.
    // Crash here before ACK; the next daemon restores only that canonical blob.
    p.close();
    let mut p = Peer::spawn(None);
    let restored=p.good(json!({"kind":"restore","revision":1,"checkpoint_json":stage["checkpoint_json"],"generation":"generation2"}));
    assert_eq!(restored["generation"], "generation2");
    assert_eq!(restored["revision"], 1);
    assert_eq!(
        p.good(status(&stage))["receipt_json"],
        stage["receipt_json"]
    );
    let projection = p.projection();
    let mut unknown_old = original.clone();
    unknown_old["base_revision"] = json!(1);
    unknown_old["base_digest"] = stage["digest"].clone();
    let mut unknown_input: Value =
        serde_json::from_str(unknown_old["input_json"].as_str().unwrap()).unwrap();
    unknown_input["op_id"] = json!("generation1:unknown");
    unknown_old["input_json"] = json!(serde_json::to_string(&unknown_input).unwrap());
    let denied = p.rpc(unknown_old);
    assert_eq!(denied["error"], "validation_refused");
    assert_eq!(denied["no_candidate"], true);
    assert_eq!(p.projection(), projection);
    let mut known = original.clone();
    known["base_revision"] = json!(1);
    known["base_digest"] = stage["digest"].clone();
    let r = p.rpc(known);
    assert_eq!(r["error"], "known_operation");
    assert_eq!(r["no_candidate"], false);
    assert_eq!(p.projection(), projection);
    // Fresh generation rotation is RAM only: SQL CAS identity stays exact blob.
    let mut next = stage_request(&restored, &projection, "fresh", "X");
    next["base_digest"] = stage["digest"].clone();
    let staged = p.good(next);
    assert_eq!(staged["base_digest"], stage["digest"]);
    let r=p.rpc(json!({"kind":"restore","revision":1,"checkpoint_json":stage["checkpoint_json"],"generation":"generation2"}));
    assert_eq!(r["ok"], false);
    assert_eq!(p.good(status(&staged))["state"], "pending");
    // Failed restore neither replaces P nor clears K; successful restore does.
    assert_eq!(p.rpc(json!({"kind":"restore","revision":99,"checkpoint_json":stage["checkpoint_json"],"generation":"generation3"}))["ok"],false);
    assert_eq!(p.good(status(&staged))["state"], "pending");
    p.good(json!({"kind":"restore","revision":1,"checkpoint_json":stage["checkpoint_json"],"generation":"generation3"}));
    assert_eq!(p.good(status(&staged))["state"], "unknown");
    p.close();
}
#[test]
fn actual_private_host_refuses_catalog_expansion_and_distinguishes_unknown_outcome() {
    let mut p = Peer::spawn(None);
    let m = p.init();
    let before = p.projection();
    let mut request = stage_request(&m, &before, "unsupported", "X");
    let mut input: Value = serde_json::from_str(request["input_json"].as_str().unwrap()).unwrap();
    input["command"] = json!("file.open");
    request["input_json"] = json!(serde_json::to_string(&input).unwrap());
    let r = p.rpc(request);
    assert_eq!(r["error"], "validation_refused");
    assert_eq!(r["no_candidate"], true);
    assert_eq!(p.projection(), before);
    let stage = p.good(stage_request(&m, &before, "pending", "K"));
    let r = p.rpc(stage_request(&m, &before, "successor", "B"));
    assert_eq!(r["error"], "pending");
    assert_eq!(r["no_candidate"], false);
    assert_eq!(p.good(status(&stage))["state"], "pending");
    assert_eq!(p.projection(), before);
    p.close();
}
#[test]
fn actual_private_host_checks_helper_elf_pin() {
    let mut p = Peer::spawn(Some(&format!("sha256:{}", "0".repeat(64))));
    let r = p.rpc(json!({"kind":"init","document":"document","generation":"generation1"}));
    assert_eq!(r["error"], "engine_failed");
    assert_eq!(r["no_candidate"], false);
    p.close();
}
#[test]
fn actual_private_host_unknown_fields_and_malformed_frames_poison_channel() {
    for request in [
        json!({"kind":"projection","protocol":1,"request_id":"x","extra":"private-payload-must-not-leak"}),
        json!({"kind":"file.open","protocol":1,"request_id":"x"}),
        json!({"kind":"projection","protocol":2,"request_id":"x"}),
        json!({"kind":"projection","protocol":1,"request_id":""}),
    ] {
        let mut p = Peer::spawn(None);
        p.send(&serde_json::to_vec(&request).unwrap());
        drop(p.stream.take());
        assert!(!p.child.wait().unwrap().success());
        let mut stderr = String::new();
        p.child
            .stderr
            .as_mut()
            .unwrap()
            .read_to_string(&mut stderr)
            .unwrap();
        assert!(!stderr.contains("private-payload-must-not-leak"));
        assert_eq!(stderr, "mooring command host stopped\n");
    }
    for header in [0u32, 32 * 1024 * 1024 + 1] {
        let mut p = Peer::spawn(None);
        p.stream
            .as_mut()
            .unwrap()
            .write_all(&header.to_be_bytes())
            .unwrap();
        drop(p.stream.take());
        assert!(!p.child.wait().unwrap().success());
    }
}

#[test]
fn actual_private_host_partial_frame_deadline_poisons_channel() {
    let mut p = Peer::spawn(None);
    p.stream.as_mut().unwrap().write_all(&[0]).unwrap();
    let mut byte = [0];
    assert_eq!(p.stream.as_mut().unwrap().read(&mut byte).unwrap(), 0);
    assert!(!p.child.wait().unwrap().success());
}

#[test]
fn actual_private_host_handles_cannot_cross_processes_with_reused_runtime_ref() {
    let mut first = Peer::spawn(None);
    let fm = first.init();
    let fp = first.projection();
    let f = first.good(stage_request(&fm, &fp, "same", "X"));
    let mut second = Peer::spawn(None);
    let sm = second.init();
    let sp = second.projection();
    let s = second.good(stage_request(&sm, &sp, "same", "X"));
    assert_ne!(f["stage_ref"], s["stage_ref"]);
    assert_eq!(second.rpc(confirm(&f))["error"], "unknown_stage");
    assert_eq!(second.good(status(&s))["state"], "pending");
    assert_eq!(second.projection(), sp);
    first.close();
    second.close();
}

#[test]
fn actual_private_host_bounds_union_when_restoring_same_old_sql_checkpoint() {
    let mut p = Peer::spawn(None);
    let original = p.init();
    let checkpoint = original["checkpoint_json"].clone();
    let base = format!(
        "sha256:{:x}",
        Sha256::digest(checkpoint.as_str().unwrap().as_bytes())
    );
    let mut current = original.clone();
    for i in 2..=32 {
        current=p.good(json!({"kind":"restore","revision":0,"checkpoint_json":checkpoint,"generation":format!("generation{i}")}));
    }
    let projection = p.projection();
    let mut request = stage_request(&current, &projection, "pending-at-capacity", "K");
    request["base_digest"] = json!(base);
    let staged = p.good(request);
    for i in 33..=34 {
        let refused=p.rpc(json!({"kind":"restore","revision":0,"checkpoint_json":checkpoint,"generation":format!("generation{i}")}));
        assert_eq!(refused["error"], "capacity");
        assert_eq!(refused["no_candidate"], false);
        assert_eq!(p.projection(), projection);
        assert_eq!(p.good(status(&staged))["state"], "pending");
    }
    // Neither the capacity refusal nor a prohibited reuse consumed the retained
    // handle. Only the trusted callback can now promote its original candidate.
    assert_eq!(p.rpc(json!({"kind":"restore","revision":0,"checkpoint_json":checkpoint,"generation":"generation2"}))["error"],"restore_failed");
    assert_eq!(
        p.good(confirm(&staged))["receipt_json"],
        staged["receipt_json"]
    );
    assert_eq!(p.good(status(&staged))["state"], "committed");
    p.close();
}
