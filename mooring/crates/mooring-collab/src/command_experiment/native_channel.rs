//! Private inherited descriptor adapter. SQL, authorization and recovery remain
//! the trusted Go host's responsibility; this binary never opens a listener.
use super::{
    expected_engine, id, process::ProcessEngine, Engine, Host, Prepared, Result, StagedCommit,
};
#[path = "native_wire.rs"]
mod wire;
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::{
    collections::BTreeSet,
    fs::File,
    io::Read,
    os::fd::{AsRawFd, FromRawFd},
    os::unix::net::UnixStream,
    path::PathBuf,
};

struct ObservedEngine {
    engine: ProcessEngine,
    calls: u64,
}
impl Engine for ObservedEngine {
    fn call(&mut self, request: Value) -> Result<Value> {
        self.calls = self
            .calls
            .checked_add(1)
            .ok_or("engine_observation_limit")?;
        self.engine.call(request)
    }
}
struct Configuration {
    engine: PathBuf,
    engine_sha256: String,
    runtime_ref: String,
}
impl Configuration {
    fn from_environment() -> Result<Self> {
        let args: Vec<String> = std::env::args().skip(1).collect();
        if args.len() != 6
            || args[0] != "--engine"
            || args[2] != "--engine-sha256"
            || args[4] != "--runtime-ref"
        {
            return Err("configuration");
        }
        let config = Self {
            engine: PathBuf::from(&args[1]),
            engine_sha256: args[3].clone(),
            runtime_ref: args[5].clone(),
        };
        if !config.engine.is_absolute()
            || !id(&config.runtime_ref)
            || !digest_string(&config.engine_sha256)
        {
            return Err("configuration");
        }
        Ok(config)
    }
    fn engine(&self) -> Result<ObservedEngine> {
        // Trusted deployment paths only, never a request field. Recheck the ELF
        // bytes before EVERY spawn, including a restore's replacement engine.
        let mut file = File::open(&self.engine).map_err(|_| "engine_pin")?;
        let meta = file.metadata().map_err(|_| "engine_pin")?;
        if !meta.is_file() || meta.len() > 128 * 1024 * 1024 {
            return Err("engine_pin");
        }
        let mut hash = Sha256::new();
        let mut data = [0; 65536];
        let mut first = true;
        loop {
            let n = file.read(&mut data).map_err(|_| "engine_pin")?;
            if n == 0 {
                break;
            }
            if first && (n < 4 || &data[..4] != b"\x7fELF") {
                return Err("engine_pin");
            }
            first = false;
            hash.update(&data[..n]);
        }
        if first || format!("sha256:{:x}", hash.finalize()) != self.engine_sha256 {
            return Err("engine_pin");
        }
        Ok(ObservedEngine {
            engine: ProcessEngine::spawn(&self.engine)?,
            calls: 0,
        })
    }
}
fn digest_string(s: &str) -> bool {
    s.len() == 71
        && s.starts_with("sha256:")
        && s.as_bytes()[7..]
            .iter()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(b))
}
struct Retained {
    reference: String,
    staged: StagedCommit,
}
struct Channel {
    config: Configuration,
    nonce: String,
    host: Option<Host<ObservedEngine>>,
    staged: Option<Retained>,
    issued: u64,
    generations: BTreeSet<String>,
}
struct Failure {
    reason: &'static str,
    no_candidate: bool,
}
type Answer = std::result::Result<Value, Failure>;
fn failure(reason: &'static str) -> Failure {
    Failure {
        reason,
        no_candidate: false,
    }
}
fn engine_reason(reason: &'static str) -> &'static str {
    if reason.starts_with("engine_") || reason == "invalid_engine_path" {
        "engine_failed"
    } else {
        "validation_refused"
    }
}
impl Channel {
    fn metadata(&self) -> Result<Value> {
        let h = self.host.as_ref().ok_or("invalid_state")?;
        Ok(
            json!({"runtime_ref":self.config.runtime_ref,"format":"wordcraft-c1-v2","engine_revision":expected_engine()["revision"],"engine_sha256":self.config.engine_sha256,"patch_sha256":expected_engine()["patch_sha256"],"catalog":"wordcraft-c1-10-v1","checkpoint_json":String::from_utf8(h.checkpoint()?).map_err(|_|"internal_failure")?,"revision":h.revision(),"generation":h.generation()}),
        )
    }
    fn stage_value(&self) -> Answer {
        let r = self
            .staged
            .as_ref()
            .ok_or_else(|| failure("invalid_state"))?;
        let s = &r.staged;
        Ok(
            json!({"state":"staged","stage_ref":r.reference,"op_id":s.op_id(),"document":s.document(),"generation":s.generation(),"format":s.format(),"engine_revision":s.engine_identity()["revision"],"engine_sha256":self.config.engine_sha256,"base_revision":s.base_revision(),"revision":s.revision(),"base_digest":s.base_checkpoint_digest(),"digest":s.checkpoint_digest(),"fingerprint":s.fingerprint(),"checkpoint_json":std::str::from_utf8(s.checkpoint()).map_err(|_|failure("invalid_state"))?,"receipt_json":serde_json::to_string(s.candidate_receipt()).map_err(|_|failure("invalid_state"))?}),
        )
    }
    fn execute(&mut self, request: wire::Request) -> Answer {
        use wire::Request::*;
        match request {
            Init {
                document,
                generation,
                ..
            } => {
                if self.host.is_some() {
                    return Err(failure("invalid_state"));
                }
                let engine = self.config.engine().map_err(|_| failure("engine_failed"))?;
                let h = Host::new(engine, &document, &generation, None)
                    .map_err(|e| failure(engine_reason(e)))?;
                self.generations.extend(h.state.generations.iter().cloned());
                self.host = Some(h);
                self.metadata().map_err(|_| failure("invalid_state"))
            }
            Restore {
                revision,
                checkpoint_json,
                generation,
                ..
            } => {
                if self.generations.contains(&generation) {
                    return Err(failure("restore_failed"));
                }
                let engine = self.config.engine().map_err(|_| failure("engine_failed"))?;
                let h = Host::restore(engine, checkpoint_json.as_bytes(), &generation)
                    .map_err(|_| failure("restore_failed"))?;
                if h.revision() != revision
                    || self
                        .host
                        .as_ref()
                        .is_some_and(|old| old.state.document != h.state.document)
                {
                    return Err(failure("restore_failed"));
                }
                let mut generations = self.generations.clone();
                generations.extend(h.state.generations.iter().cloned());
                if generations.len() > 32 {
                    // No trimming/reuse: failed replacement must retain current
                    // P, K and its opaque handle for canonical reconciliation.
                    return Err(failure("capacity"));
                }
                self.generations = generations;
                self.host = Some(h);
                self.staged = None;
                self.metadata().map_err(|_| failure("invalid_state"))
            }
            Stage {
                base_revision,
                base_digest,
                input_json,
                admission,
                ..
            } => {
                let h = self.host.as_mut().ok_or_else(|| failure("invalid_state"))?;
                let empty = h.k.is_none();
                let refused = |reason| Failure {
                    reason,
                    no_candidate: empty,
                };
                if h.revision() != base_revision || h.canonical_checkpoint_digest() != base_digest {
                    return Err(refused("base_conflict"));
                }
                let a = admission
                    .admitted()
                    .map_err(|_| refused("admission_denied"))?;
                let calls = h.engine.calls;
                let prepared = h.prepare(input_json.as_bytes(), &a).map_err(|e| Failure {
                    reason: if e == "pending_limit" {
                        "pending"
                    } else {
                        engine_reason(e)
                    },
                    no_candidate: empty
                        && h.k.is_none()
                        && h.engine.calls == calls
                        && !e.starts_with("engine_"),
                })?;
                match prepared {
                    Prepared::Pending { op_id } => {
                        let staged = h
                            .stage_commit(&op_id, &a)
                            .map_err(|_| failure("admission_denied"))?;
                        if let Some(old) = &self.staged {
                            if old.staged.op_id() == staged.op_id()
                                && old.staged.checkpoint() == staged.checkpoint()
                            {
                                return self.stage_value();
                            }
                        }
                        self.issued = self
                            .issued
                            .checked_add(1)
                            .ok_or_else(|| failure("capacity"))?;
                        self.staged = Some(Retained {
                            reference: format!("c1:{}:{}", self.nonce, self.issued),
                            staged,
                        });
                        self.stage_value()
                    }
                    Prepared::Known(_) => Err(failure("known_operation")),
                    Prepared::Read(_) => Err(failure("validation_refused")),
                }
            }
            Confirm {
                stage_ref,
                admission,
                ..
            } => {
                let retained = self
                    .staged
                    .as_ref()
                    .filter(|r| r.reference == stage_ref)
                    .ok_or_else(|| failure("unknown_stage"))?;
                let a = admission
                    .admitted()
                    .map_err(|_| failure("admission_denied"))?;
                let h = self.host.as_mut().ok_or_else(|| failure("invalid_state"))?;
                let receipt = h
                    .confirm_committed(&retained.staged, &a)
                    .map_err(|_| failure("admission_denied"))?;
                Ok(
                    json!({"receipt_json":serde_json::to_string(&receipt).map_err(|_|failure("invalid_state"))?}),
                )
            }
            Reject { stage_ref, .. } => {
                let r = self
                    .staged
                    .as_ref()
                    .filter(|r| r.reference == stage_ref)
                    .ok_or_else(|| failure("unknown_stage"))?;
                self.host
                    .as_mut()
                    .ok_or_else(|| failure("invalid_state"))?
                    .reject(r.staged.op_id())
                    .map_err(|_| failure("invalid_state"))?;
                self.staged = None;
                Ok(json!({"state":"rejected"}))
            }
            Status {
                op_id,
                expected_fingerprint,
                ..
            } => {
                if !id(&op_id) || !digest_string(&expected_fingerprint) {
                    return Err(failure("invalid_request"));
                }
                let h = self.host.as_ref().ok_or_else(|| failure("invalid_state"))?;
                if let Some(known) = h.state.seen.get(&op_id).or_else(|| h.rejected.get(&op_id)) {
                    if known.input != expected_fingerprint.as_bytes() {
                        return Err(failure("validation_refused"));
                    }
                    return Ok(
                        json!({"state":if h.state.seen.contains_key(&op_id){"committed"}else{"rejected"},"receipt_json":serde_json::to_string(&known.result).map_err(|_|failure("invalid_state"))?}),
                    );
                }
                if let Some(k) = h.k.as_ref().filter(|k| k.envelope.op_id == op_id) {
                    if k.input != expected_fingerprint.as_bytes() {
                        return Err(failure("validation_refused"));
                    }
                    return Ok(json!({"state":"pending"}));
                }
                Ok(json!({"state":"unknown"}))
            }
            Projection { .. } => {
                let h = self.host.as_ref().ok_or_else(|| failure("invalid_state"))?;
                Ok(
                    json!({"generation":h.generation(),"revision":h.revision(),"ids":h.ids(),"native_json":serde_json::to_string(h.native()).map_err(|_|failure("invalid_state"))?}),
                )
            }
        }
    }
}
// A process-local opaque handle namespace prevents a restarted daemon from
// accepting a prior process's handle even when a trusted runtime_ref is reused.
// This does not authorize a caller; only the private inherited channel may ACK.
fn channel_nonce() -> Result<String> {
    let mut raw = [0u8; 32];
    let mut done = 0;
    while done < raw.len() {
        let n = unsafe { libc::getrandom(raw[done..].as_mut_ptr().cast(), raw.len() - done, 0) };
        if n > 0 {
            done += n as usize;
        } else if n < 0 && std::io::Error::last_os_error().kind() == std::io::ErrorKind::Interrupted
        {
            continue;
        } else {
            return Err("host_nonce");
        }
    }
    Ok(raw.iter().map(|b| format!("{b:02x}")).collect())
}

/// The descriptor is issued by the trusted Go parent, not selected in JSON.
pub fn run() -> Result<()> {
    let config = Configuration::from_environment()?;
    if std::env::var("MOORING_C1_HOST_FD").as_deref() != Ok("3") {
        return Err("private_descriptor");
    }
    // Validate openness before taking ownership; FromRawFd requires a valid FD.
    if unsafe { libc::fcntl(3, libc::F_GETFD) } < 0 {
        return Err("private_descriptor");
    }
    // SAFETY: this process exclusively takes ownership of inherited FD 3 once.
    let mut stream = unsafe { UnixStream::from_raw_fd(3) };
    let mut kind: libc::c_int = 0;
    let mut domain: libc::c_int = 0;
    let mut len = std::mem::size_of_val(&kind) as libc::socklen_t;
    // Ensure AF_UNIX SOCK_STREAM, then prevent engine children inheriting the
    // parent control socket. Only their separate ProcessEngine FD is inherited.
    unsafe {
        if libc::getsockopt(
            stream.as_raw_fd(),
            libc::SOL_SOCKET,
            libc::SO_DOMAIN,
            (&mut domain as *mut libc::c_int).cast(),
            &mut len,
        ) != 0
            || domain != libc::AF_UNIX
            || libc::getsockopt(
                stream.as_raw_fd(),
                libc::SOL_SOCKET,
                libc::SO_TYPE,
                (&mut kind as *mut libc::c_int).cast(),
                &mut len,
            ) != 0
            || kind != libc::SOCK_STREAM
            || libc::fcntl(stream.as_raw_fd(), libc::F_SETFD, libc::FD_CLOEXEC) < 0
        {
            return Err("private_descriptor");
        }
    }
    stream.peer_addr().map_err(|_| "private_descriptor")?;
    let mut channel = Channel {
        config,
        nonce: channel_nonce()?,
        host: None,
        staged: None,
        issued: 0,
        generations: BTreeSet::new(),
    };
    while let Some(raw) = wire::read_frame(&mut stream)? {
        let request: wire::Request = serde_json::from_slice(&raw).map_err(|_| "invalid_request")?;
        let request_id = request.correlation()?.to_owned();
        let reply = match channel.execute(request) {
            Ok(value) => json!({"protocol":1,"request_id":request_id,"ok":true,"value":value}),
            Err(e) => {
                json!({"protocol":1,"request_id":request_id,"ok":false,"error":e.reason,"no_candidate":e.no_candidate})
            }
        };
        let raw = serde_json::to_vec(&reply).map_err(|_| "reply_encode")?;
        wire::write_frame(&mut stream, &raw)?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    fn request(v: Value) -> wire::Request {
        serde_json::from_value(v).unwrap()
    }
    fn actual_config() -> Configuration {
        let engine = PathBuf::from(
            std::env::var_os("MOORING_C1_ENGINE_BINARY")
                .expect("actual C1 engine required; no mock/skip"),
        );
        Configuration {
            engine_sha256: format!(
                "sha256:{:x}",
                Sha256::digest(std::fs::read(&engine).unwrap())
            ),
            engine,
            runtime_ref: "counter-proof".into(),
        }
    }
    #[test]
    fn pure_status_and_restored_known_operation_never_execute_engine() {
        let mut c = Channel {
            config: actual_config(),
            nonce: "unit-counter-proof".into(),
            host: None,
            staged: None,
            issued: 0,
            generations: BTreeSet::new(),
        };
        c.execute(request(json!({"kind":"init","protocol":1,"request_id":"a","document":"document","generation":"g1"}))).ok().unwrap();
        let h = c.host.as_ref().unwrap();
        let envelope = json!({"generation":"g1","op_id":"g1:operation","participant":"jana","base_revision":0,"target":{"block_id":h.ids()[0],"unit":"utf8_bytes","start":0,"end":0},"command":"text.insert","params":{"text":"X"}});
        let stage_request = json!({"kind":"stage","protocol":1,"request_id":"b","base_revision":0,"base_digest":h.canonical_checkpoint_digest(),"input_json":serde_json::to_string(&envelope).unwrap(),"admission":{"participant":"jana","author":"Jana","time":"2001-02-03T04:05:06Z","epoch":1,"writer":true}});
        let staged = c.execute(request(stage_request.clone())).ok().unwrap();
        let status = json!({"kind":"status","protocol":1,"request_id":"c","op_id":staged["op_id"],"expected_fingerprint":staged["fingerprint"]});
        let calls = c.host.as_ref().unwrap().engine.calls;
        assert_eq!(
            c.execute(request(status.clone())).ok().unwrap()["state"],
            "pending"
        );
        assert_eq!(c.host.as_ref().unwrap().engine.calls, calls);
        c.execute(request(json!({"kind":"restore","protocol":1,"request_id":"d","revision":1,"checkpoint_json":staged["checkpoint_json"],"generation":"g2"}))).ok().unwrap();
        let calls = c.host.as_ref().unwrap().engine.calls;
        assert_eq!(
            c.execute(request(status)).ok().unwrap()["state"],
            "committed"
        );
        let mut known = stage_request;
        known["base_revision"] = json!(1);
        known["base_digest"] = staged["digest"].clone();
        let result = c.execute(request(known)).err().unwrap();
        assert_eq!(result.reason, "known_operation");
        assert!(!result.no_candidate);
        assert_eq!(c.host.as_ref().unwrap().engine.calls, calls);
    }
}
