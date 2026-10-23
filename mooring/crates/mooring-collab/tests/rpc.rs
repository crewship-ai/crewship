use mooring_collab::{read_frame, write_frame, MAX_FRAME};
use std::io::{Cursor, Write};

#[test]
fn framing_bounds_truncation_invalid_utf8_and_json_fail_closed() {
    for length in [0, MAX_FRAME as u32 + 1, u32::MAX] {
        assert!(read_frame(&mut Cursor::new(length.to_be_bytes())).is_err());
    }
    for raw in [
        vec![0, 0, 0],
        vec![0, 0, 0, 2, b'{'],
        vec![0, 0, 0, 1, 0xff],
        vec![0, 0, 0, 1, b'x'],
    ] {
        assert!(read_frame(&mut Cursor::new(raw)).is_err());
    }
    assert!(read_frame(&mut Cursor::new([])).unwrap().is_none());
    let mut output = Vec::new();
    assert!(write_frame(&mut output, b"x").is_err());
    assert!(output.is_empty());
    let maximum = format!("[{}]", " ".repeat(MAX_FRAME - 2));
    write_frame(&mut output, maximum.as_bytes()).unwrap();
    assert_eq!(
        read_frame(&mut Cursor::new(output)).unwrap().unwrap(),
        maximum.as_bytes()
    );
}

#[cfg(unix)]
mod unix {
    use super::*;
    use mooring_collab::private_rpc::serve;
    use std::os::unix::net::UnixStream;
    use std::thread;
    use std::time::Duration;
    const HELLO: &[u8] = br#"{"type":"hello","request_id":"host-start","supported":[{"protocol_version":"0.4.0","model_version":"1.0.0","engine_version":"0.1.0"}]}"#;
    fn start(timeout: Duration) -> (UnixStream, thread::JoinHandle<std::io::Result<()>>) {
        let (host, child) = UnixStream::pair().unwrap();
        host.set_read_timeout(Some(Duration::from_secs(2))).unwrap();
        (host, thread::spawn(move || serve(child, timeout)))
    }
    #[test]
    fn readiness_is_strict_non_authoritative_and_eof_stops_child() {
        let (mut host, child) = start(Duration::from_secs(1));
        // Real socket fragmentation is accepted, no message boundary assumption.
        let mut encoded = Vec::new();
        write_frame(&mut encoded, HELLO).unwrap();
        for byte in encoded {
            host.write_all(&[byte]).unwrap();
        }
        let ready: serde_json::Value =
            serde_json::from_slice(&read_frame(&mut host).unwrap().unwrap()).unwrap();
        assert_eq!(
            ready,
            serde_json::json!({"type":"runtime_ready","request_id":"host-start","selected":{"protocol_version":"0.4.0","model_version":"1.0.0","engine_version":"0.1.0"}})
        );
        drop(host);
        assert!(child.join().unwrap().is_ok());
    }
    #[test]
    fn old_versions_duplicate_or_unknown_fields_never_get_readiness() {
        for input in [
            String::from_utf8(HELLO.to_vec())
                .unwrap()
                .replace("0.4.0", "0.3.0"),
            String::from_utf8(HELLO.to_vec()).unwrap().replace(
                r#""request_id":"#,
                r#""request_id":"duplicate","request_id":"#,
            ),
            String::from_utf8(HELLO.to_vec())
                .unwrap()
                .replace(r#""supported":"#, r#""authority":true,"supported":"#),
        ] {
            let (mut host, child) = start(Duration::from_secs(1));
            write_frame(&mut host, input.as_bytes()).unwrap();
            assert!(read_frame(&mut host).unwrap().is_none());
            assert!(child.join().unwrap().is_err());
        }
    }
    #[test]
    fn post_bootstrap_content_or_control_is_not_dispatched() {
        let (mut host, child) = start(Duration::from_secs(1));
        write_frame(&mut host, HELLO).unwrap();
        assert!(read_frame(&mut host).unwrap().is_some());
        write_frame(&mut host, br#"{"type":"saved","issuer":"fake"}"#).unwrap();
        assert!(read_frame(&mut host).unwrap().is_none());
        assert!(child.join().unwrap().is_err());
    }
    #[test]
    fn silent_or_fragmented_host_cannot_extend_absolute_bootstrap_deadline() {
        let (mut host, child) = start(Duration::from_millis(80));
        host.write_all(&[0]).unwrap();
        thread::sleep(Duration::from_millis(50));
        let _ = host.write_all(&[0]);
        thread::sleep(Duration::from_millis(60));
        assert!(!matches!(read_frame(&mut host), Ok(Some(_))));
        assert!(child.join().unwrap().is_err());
    }
}

#[cfg(target_os = "linux")]
#[test]
fn actual_child_owns_fd3_and_marks_host_capability_close_on_exec() {
    use std::os::fd::AsRawFd;
    use std::os::unix::{net::UnixStream, process::CommandExt};
    use std::process::{Command, Stdio};
    use std::time::Duration;
    let (mut host, inherited) = UnixStream::pair().unwrap();
    host.set_read_timeout(Some(Duration::from_secs(2))).unwrap();
    let child_fd = inherited.as_raw_fd();
    let mut command = Command::new(env!("CARGO_BIN_EXE_mooring-collab"));
    command
        .args(["--rpc-fd", "3"])
        .env_clear()
        .env("LANG", "C.UTF-8")
        .env("TZ", "UTC")
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null());
    // Async-signal-safe syscalls only between fork and exec. The inherited
    // socket is owned by the parent until spawn completes, and duplicated only
    // in the child. Runtime startup must restore CLOEXEC after this exec.
    unsafe {
        command.pre_exec(move || {
            if libc::dup2(child_fd, 3) == -1 || libc::fcntl(3, libc::F_SETFD, 0) == -1 {
                return Err(std::io::Error::last_os_error());
            }
            Ok(())
        });
    }
    let mut child = command.spawn().unwrap();
    drop(inherited);
    write_frame(&mut host, br#"{"type":"hello","request_id":"fd-test","supported":[{"protocol_version":"0.4.0","model_version":"0.1.0","engine_version":"0.10.0"}]}"#).unwrap();
    assert!(read_frame(&mut host).unwrap().is_some());
    let info = std::fs::read_to_string(format!("/proc/{}/fdinfo/3", child.id())).unwrap();
    let flags = info
        .lines()
        .find_map(|line| line.strip_prefix("flags:\t"))
        .unwrap();
    let flags = u32::from_str_radix(flags, 8).unwrap();
    assert_ne!(
        flags & libc::O_CLOEXEC as u32,
        0,
        "host capability could leak across exec"
    );
    drop(host);
    assert!(child.wait().unwrap().success());
}
