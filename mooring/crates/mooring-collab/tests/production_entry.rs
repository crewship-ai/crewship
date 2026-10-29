#![cfg(target_os = "linux")]
//! Actual packaged binary, not the integration example. Host admission and SQL
//! are separate Go qualifications; these tests grant no participant authority.
use mooring_collab::{read_frame, write_frame};
use serde_json::{json, Value};
use std::{
    os::fd::AsRawFd,
    os::unix::{net::UnixStream, process::CommandExt},
    process::{Child, Command, Stdio},
    time::Duration,
};
fn start() -> (Child, UnixStream) {
    let (host, child) = UnixStream::pair().unwrap();
    host.set_read_timeout(Some(Duration::from_secs(2))).unwrap();
    let descriptor = child.as_raw_fd();
    let mut command = Command::new(env!("CARGO_BIN_EXE_mooring-collab"));
    command
        .args(["--rpc-fd", "3"])
        .env_clear()
        .env("LANG", "C.UTF-8")
        .env("TZ", "UTC")
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null());
    // Only async-signal-safe syscalls between fork and exec. The production
    // entrypoint must immediately restore CLOEXEC on this inherited FD.
    unsafe {
        command.pre_exec(move || {
            if libc::dup2(descriptor, 3) == -1 || libc::fcntl(3, libc::F_SETFD, 0) == -1 {
                return Err(std::io::Error::last_os_error());
            }
            Ok(())
        });
    }
    let spawned = command.spawn().unwrap();
    drop(child);
    (spawned, host)
}
fn hello(profiles: Option<Value>) -> Value {
    let mut value = json!({"type":"hello", "request_id":"production-start", "supported":[mooring_collab::dispatch::wordcraft_versions()]});
    if let Some(profiles) = profiles {
        value["control_profiles"] = profiles;
    }
    value
}
fn send(host: &mut UnixStream, value: &Value) {
    write_frame(host, &serde_json::to_vec(value).unwrap()).unwrap();
}
#[test]
fn production_binary_retains_nooffer_strict_and_causal_negotiation() {
    for profile in [
        None,
        Some("mooring.prepare.v1"),
        Some("mooring.prepare-causal.v1"),
    ] {
        let (mut child, mut host) = start();
        send(&mut host, &hello(profile.map(|p| json!([p]))));
        let ready: Value =
            serde_json::from_slice(&read_frame(&mut host).unwrap().unwrap()).unwrap();
        assert_eq!(
            ready["selected"],
            serde_json::to_value(mooring_collab::dispatch::wordcraft_versions()).unwrap()
        );
        assert_eq!(ready["request_id"], "production-start");
        match profile {
            Some(profile) => assert_eq!(ready["selected_control_profile"], profile),
            None => assert!(ready.get("selected_control_profile").is_none()),
        }
        drop(host);
        assert!(child.wait().unwrap().success());
    }
}
#[test]
fn production_binary_selects_actual_session_driver_on_exact_offer_only() {
    let (mut child, mut host) = start();
    send(
        &mut host,
        &hello(Some(json!(["mooring.session-causal.v1"]))),
    );
    let ready: Value = serde_json::from_slice(&read_frame(&mut host).unwrap().unwrap()).unwrap();
    assert_eq!(
        ready["selected_control_profile"],
        "mooring.session-causal.v1"
    );
    let mut open = include_str!("../../../protocol/session-control-fixtures.json")
        .parse::<Value>()
        .unwrap()["cases"]
        .as_array()
        .unwrap()
        .iter()
        .find(|case| case["id"] == "valid-SessionOpen")
        .unwrap()["message"]
        .clone();
    open["versions"] =
        serde_json::to_value(mooring_collab::dispatch::wordcraft_versions()).unwrap();
    open["scope"]["application_id"] = json!("wordcraft");
    send(&mut host, &open);
    let acquire: Value = serde_json::from_slice(&read_frame(&mut host).unwrap().unwrap()).unwrap();
    assert_eq!(acquire["type"], "owner_acquire");
    assert_eq!(acquire["scope"], open["scope"]);
    assert_eq!(acquire["runtime_ref"], open["runtime_ref"]);
    // No fake grant: dying host closes the actual driver before any actor,
    // participant, document installation or durable outcome is fabricated.
    drop(host);
    assert!(!child.wait().unwrap().success());
}
#[test]
fn production_binary_never_downgrades_malformed_ambiguous_or_incompatible_session_offers() {
    for mode in [
        "mixed",
        "duplicate",
        "two-tuples",
        "old-model",
        "unknown-profile",
        "unknown-field",
    ] {
        let (mut child, mut host) = start();
        let mut request = hello(Some(json!(["mooring.session-causal.v1"])));
        match mode {
            "mixed" => {
                request["control_profiles"] =
                    json!(["mooring.session-causal.v1", "mooring.prepare-causal.v1"])
            }
            "duplicate" => {
                request["control_profiles"] =
                    json!(["mooring.session-causal.v1", "mooring.session-causal.v1"])
            }
            "two-tuples" => {
                let mut old = request["supported"][0].clone();
                old["model_version"] = json!("0.1.0");
                request["supported"].as_array_mut().unwrap().push(old);
            }
            "old-model" => request["supported"][0]["model_version"] = json!("0.1.0"),
            "unknown-profile" => request["control_profiles"] = json!(["mooring.session-causal.v2"]),
            "unknown-field" => request["authority"] = json!(true),
            _ => unreachable!(),
        }
        send(&mut host, &request);
        assert!(read_frame(&mut host).unwrap().is_none(), "{mode}");
        assert!(!child.wait().unwrap().success(), "{mode}");
    }
}
