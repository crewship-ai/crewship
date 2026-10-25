//! One helper process per document, a private inherited FD, no network listener.
use super::{body, expected_engine, Engine, Result, FRAME};
use serde_json::Value;
use std::{
    io::{Read, Write},
    os::{
        fd::AsRawFd,
        unix::{net::UnixStream, process::CommandExt},
    },
    path::Path,
    process::{Child, Command, Stdio},
    time::{Duration, Instant},
};

pub struct ProcessEngine {
    process: Child,
    stream: UnixStream,
    failed: bool,
}
impl ProcessEngine {
    pub fn spawn(binary: &Path) -> Result<Self> {
        if !binary.is_absolute() {
            return Err("invalid_engine_path");
        }
        let (stream, child) = UnixStream::pair().map_err(|_| "engine_socket")?;
        stream
            .set_read_timeout(Some(Duration::from_secs(5)))
            .map_err(|_| "engine_socket")?;
        stream
            .set_write_timeout(Some(Duration::from_secs(1)))
            .map_err(|_| "engine_socket")?;
        let fd = child.as_raw_fd();
        let mut command = Command::new(binary);
        command
            .env_clear()
            .env("MOORING_C1_ENGINE_FD", fd.to_string())
            .env("LANG", "C.UTF-8")
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::inherit());
        // Only the new child changes its inherited FD/limits. No host settings.
        unsafe {
            command.pre_exec(move || {
                if libc::fcntl(fd, libc::F_SETFD, 0) < 0 {
                    return Err(std::io::Error::last_os_error());
                }
                for (resource, limit) in [
                    (libc::RLIMIT_AS, 2 * 1024 * 1024 * 1024),
                    (libc::RLIMIT_CPU, 60),
                ] {
                    let bound = libc::rlimit {
                        rlim_cur: limit,
                        rlim_max: limit,
                    };
                    if libc::setrlimit(resource, &bound) != 0 {
                        return Err(std::io::Error::last_os_error());
                    }
                }
                Ok(())
            });
        }
        let process = command.spawn().map_err(|_| "engine_spawn")?;
        drop(child);
        Ok(Self {
            process,
            stream,
            failed: false,
        })
    }
    pub fn process_id(&self) -> u32 {
        self.process.id()
    }
}
#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct Reply {
    ok: bool,
    value: Option<Value>,
    reason: Option<String>,
}
#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct Success {
    native: Value,
    result: Value,
    engine_identity: Value,
}
impl ProcessEngine {
    fn poison(&mut self) {
        self.failed = true;
        let _ = self.process.kill();
        let _ = self.process.wait();
    }
    fn read_until(&mut self, mut bytes: &mut [u8], deadline: Instant) -> Result<()> {
        while !bytes.is_empty() {
            let remaining = deadline
                .checked_duration_since(Instant::now())
                .filter(|d| !d.is_zero())
                .ok_or("engine_read")?;
            self.stream
                .set_read_timeout(Some(remaining))
                .map_err(|_| "engine_read")?;
            match self.stream.read(bytes) {
                Ok(0) => return Err("engine_read"),
                Ok(n) => {
                    bytes = &mut bytes[n..];
                }
                Err(e) if e.kind() == std::io::ErrorKind::Interrupted => {}
                Err(_) => return Err("engine_read"),
            }
        }
        Ok(())
    }
    fn write_until(&mut self, mut bytes: &[u8], deadline: Instant) -> Result<()> {
        while !bytes.is_empty() {
            let remaining = deadline
                .checked_duration_since(Instant::now())
                .filter(|d| !d.is_zero())
                .ok_or("engine_write")?;
            self.stream
                .set_write_timeout(Some(remaining))
                .map_err(|_| "engine_write")?;
            match self.stream.write(bytes) {
                Ok(0) => return Err("engine_write"),
                Ok(n) => {
                    bytes = &bytes[n..];
                }
                Err(e) if e.kind() == std::io::ErrorKind::Interrupted => {}
                Err(_) => return Err("engine_write"),
            }
        }
        Ok(())
    }
    fn exchange(&mut self, raw: &[u8]) -> Result<Reply> {
        let deadline = Instant::now() + Duration::from_secs(1);
        self.write_until(&(raw.len() as u32).to_be_bytes(), deadline)?;
        self.write_until(raw, deadline)?;
        let deadline = Instant::now() + Duration::from_secs(5);
        let mut header = [0; 4];
        self.read_until(&mut header, deadline)?;
        let length = u32::from_be_bytes(header) as usize;
        if length == 0 || length > FRAME {
            return Err("engine_frame_limit");
        }
        let mut raw = vec![0; length];
        self.read_until(&mut raw, deadline)?;
        let reply: Reply = serde_json::from_slice(&raw).map_err(|_| "engine_reply")?;
        if (reply.ok && (reply.value.is_none() || reply.reason.is_some()))
            || (!reply.ok && (reply.value.is_some() || reply.reason.is_none()))
        {
            return Err("engine_reply");
        }
        Ok(reply)
    }
}
impl Engine for ProcessEngine {
    fn call(&mut self, request: Value) -> Result<Value> {
        if self.failed {
            return Err("engine_failed");
        }
        let raw = serde_json::to_vec(&request).map_err(|_| "engine_request")?;
        if raw.is_empty() || raw.len() > FRAME {
            return Err("engine_frame_limit");
        }
        let reply = match self.exchange(&raw) {
            Ok(r) => r,
            Err(e) => {
                self.poison();
                return Err(e);
            }
        };
        if !reply.ok {
            return Err(match reply.reason.as_deref() {
                Some("invalid_target") => "invalid_target",
                Some("invalid_clock") => "invalid_clock",
                _ => "engine_refused",
            });
        }
        let value = reply.value.expect("validated reply");
        let valid = serde_json::from_value::<Success>(value.clone())
            .ok()
            .is_some_and(|out| {
                let _ = &out.result; // Command-dependent result remains opaque in this scoped experiment.
                out.engine_identity == expected_engine() && body(&out.native).is_ok()
            });
        if !valid {
            self.poison();
            return Err("engine_reply");
        }
        Ok(value)
    }
}
impl Drop for ProcessEngine {
    fn drop(&mut self) {
        let _ = self.process.kill();
        let _ = self.process.wait();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        fs,
        os::unix::fs::PermissionsExt,
        time::{SystemTime, UNIX_EPOCH},
    };
    #[test]
    fn partial_timed_out_child_is_reaped_and_never_reused() {
        let path = std::env::temp_dir().join(format!(
            "c1-fault-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        fs::write(&path,"#!/usr/bin/python3\nimport os,socket,struct,time\ns=socket.socket(fileno=int(os.environ['MOORING_C1_ENGINE_FD']))\nn=struct.unpack('>I',s.recv(4))[0]\ns.recv(n)\ns.sendall(struct.pack('>I',100)+b'{')\ntime.sleep(10)\ns.sendall(b'late reply')\n").unwrap();
        fs::set_permissions(&path, fs::Permissions::from_mode(0o700)).unwrap();
        let mut engine = ProcessEngine::spawn(&path).unwrap();
        let pid = engine.process_id();
        let started = std::time::Instant::now();
        assert_eq!(engine.call(serde_json::json!({})), Err("engine_read"));
        assert_eq!(
            engine.call(serde_json::json!({"next":true})),
            Err("engine_failed")
        );
        fs::remove_file(path).unwrap();
        assert!(started.elapsed() >= Duration::from_secs(5));
        assert!(
            started.elapsed() < Duration::from_secs(8),
            "whole-frame deadline must expire before the late reply"
        );
        assert!(
            !Path::new(&format!("/proc/{pid}")).exists(),
            "child must be reaped before returning error"
        );
    }
    #[test]
    fn malformed_full_success_poison_is_not_a_recoverable_refusal() {
        let native = serde_json::json!({"body":[{"text":""}]});
        for reply in [
            serde_json::json!({"ok":true,"value":{},"unexpected":1}),
            serde_json::json!({"ok":true,"value":{}}),
            serde_json::json!({"ok":true,"value":{"native":native,"result":{},"engine_identity":expected_engine(),"unexpected":1}}),
            serde_json::json!({"ok":true,"value":{"native":native,"result":{},"engine_identity":{"revision":"wrong"}}}),
            serde_json::json!({"ok":true,"value":{"native":{"body":[]},"result":{},"engine_identity":expected_engine()}}),
        ] {
            let path = std::env::temp_dir().join(format!(
                "c1-protocol-{}-{}",
                std::process::id(),
                SystemTime::now()
                    .duration_since(UNIX_EPOCH)
                    .unwrap()
                    .as_nanos()
            ));
            let script=format!("#!/usr/bin/python3\nimport os,socket,struct,time\ns=socket.socket(fileno=int(os.environ['MOORING_C1_ENGINE_FD']))\nn=struct.unpack('>I',s.recv(4))[0]\ns.recv(n)\nr={:?}.encode()\ns.sendall(struct.pack('>I',len(r))+r)\ntime.sleep(10)\n",reply.to_string());
            fs::write(&path, script).unwrap();
            fs::set_permissions(&path, fs::Permissions::from_mode(0o700)).unwrap();
            let mut e = ProcessEngine::spawn(&path).unwrap();
            let pid = e.process_id();
            assert_eq!(
                e.call(serde_json::json!({})),
                Err("engine_reply"),
                "{reply}"
            );
            assert_eq!(e.call(serde_json::json!({})), Err("engine_failed"));
            assert!(!Path::new(&format!("/proc/{pid}")).exists());
            fs::remove_file(path).unwrap();
        }
    }
}
