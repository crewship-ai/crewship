//! Private C1 helper, not a user command endpoint. No TCP/HTTP listener.
use serde::Deserialize;
use serde_json::{json, Value};
use std::{
    io::{self, Read, Write},
    os::{fd::FromRawFd, unix::net::UnixStream},
};
use unicode_segmentation::UnicodeSegmentation;
use wordcraft_doc::{Block, Document};
use wordcraft_engine::{io as native_io, Session};
const FRAME: usize = 4 * 1024 * 1024;
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Target {
    block: usize,
    start: usize,
    end: usize,
    unit: String,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Request {
    command: String,
    params: Value,
    native: Option<Value>,
    target: Option<Target>,
    author: String,
    time: String,
}
fn keys(p: &Value, allowed: &[&str]) -> Result<(), &'static str> {
    let p = p.as_object().ok_or("invalid_params")?;
    if p.keys().any(|k| !allowed.contains(&k.as_str())) {
        return Err("invalid_params");
    }
    Ok(())
}
fn run(raw: &[u8]) -> Result<Value, &'static str> {
    let r: Request = serde_json::from_slice(raw).map_err(|_| "invalid_request")?;
    let doc = if let Some(native) = r.native {
        native_io::open_bytes(
            "state.json",
            &serde_json::to_vec(&native).map_err(|_| "invalid_document")?,
        )
        .map_err(|_| "invalid_document")?
    } else {
        Document::new()
    };
    let mut s = Session::new(doc);
    let _layout = s.layout(); // Cold M2 load+first layout is deliberately measured.
    let (engine, params) = match r.command.as_str() {
        "init" | "document.read" => {
            keys(&r.params, &[])?;
            if r.target.is_some() {
                return Err("invalid_target");
            }
            ("document.text", json!({}))
        }
        "text.insert" => {
            keys(&r.params, &["text"])?;
            let text = r
                .params
                .get("text")
                .and_then(Value::as_str)
                .ok_or("invalid_params")?;
            if text.is_empty()
                || text.len() > 32768
                || text.contains(['\n', '\r', '\0', '\u{fffc}'])
            {
                return Err("invalid_params");
            }
            ("text.insert", r.params.clone())
        }
        "text.delete" | "text.backspace" | "text.newParagraph" => {
            keys(&r.params, &[])?;
            (r.command.as_str(), r.params.clone())
        }
        "format.bold" => {
            keys(&r.params, &["value"])?;
            if r.params.get("value").is_some_and(|v| !v.is_boolean()) {
                return Err("invalid_params");
            }
            ("format.bold", r.params.clone())
        }
        "format.font" => {
            keys(&r.params, &["name"])?;
            if !r
                .params
                .get("name")
                .and_then(Value::as_str)
                .is_some_and(|x| !x.is_empty() && x.len() <= 200)
            {
                return Err("invalid_params");
            }
            ("format.font", r.params.clone())
        }
        "format.size" => {
            keys(&r.params, &["size"])?;
            if !r
                .params
                .get("size")
                .and_then(Value::as_f64)
                .is_some_and(|n| (1.0..=200.0).contains(&n))
            {
                return Err("invalid_params");
            }
            ("format.size", r.params.clone())
        }
        "para.align" => {
            keys(&r.params, &["value"])?;
            if !matches!(
                r.params.get("value").and_then(Value::as_str),
                Some("left" | "center" | "right" | "justify")
            ) {
                return Err("invalid_params");
            }
            ("para.align", r.params.clone())
        }
        _ => return Err("command_denied"),
    };
    if !matches!(r.command.as_str(), "init" | "document.read") {
        let t = r.target.ok_or("invalid_target")?;
        let Some(Block::Para(p)) = s.doc.body.get(t.block).map(|b| &**b) else {
            return Err("invalid_target");
        };
        let boundaries = p
            .text
            .grapheme_indices(true)
            .map(|(n, _)| n)
            .chain(std::iter::once(p.text.len()))
            .collect::<Vec<_>>();
        if t.unit != "utf8_bytes"
            || t.start > t.end
            || !boundaries.contains(&t.start)
            || !boundaries.contains(&t.end)
        {
            return Err("invalid_target");
        }
        s.run_with_host_context("select.range",&json!({"anchor":{"block":t.block,"off":t.start},"focus":{"block":t.block,"off":t.end}}),&r.author,&r.time).map_err(|_|"invalid_clock")?;
    }
    let result = s
        .run_with_host_context(engine, &params, &r.author, &r.time)
        .map_err(|_| "engine_refused")?;
    let _layout = s.layout();
    let native: Value = serde_json::from_slice(
        &native_io::save_bytes("state.json", &s.doc).map_err(|_| "invalid_document")?,
    )
    .map_err(|_| "invalid_document")?;
    let source: Value =
        serde_json::from_str(include_str!("../source.json")).map_err(|_| "source_identity")?;
    Ok(
        json!({"native":native,"result":result,"engine_identity":{"revision":source["rev"],"patch_sha256":source["patch_sha256"],"catalog":"wordcraft-c1-10-v1"}}),
    )
}
fn main() -> io::Result<()> {
    let fd = std::env::var("MOORING_C1_ENGINE_FD")
        .ok()
        .and_then(|s| s.parse::<i32>().ok())
        .filter(|fd| *fd >= 3)
        .ok_or_else(|| io::Error::other("private engine FD required"))?;
    let mut stream = unsafe { UnixStream::from_raw_fd(fd) };
    stream.peer_addr()?;
    if unsafe { libc::fcntl(fd, libc::F_SETFD, libc::FD_CLOEXEC) } < 0 {
        return Err(io::Error::last_os_error());
    }
    loop {
        let mut head = [0; 4];
        match stream.read_exact(&mut head) {
            Ok(()) => {}
            Err(e) if e.kind() == io::ErrorKind::UnexpectedEof => return Ok(()),
            Err(e) => return Err(e),
        }
        let n = u32::from_be_bytes(head) as usize;
        if n == 0 || n > FRAME {
            return Err(io::Error::other("frame limit"));
        }
        let mut raw = vec![0; n];
        stream.read_exact(&mut raw)?;
        let reply = match run(&raw) {
            Ok(value) => json!({"ok":true,"value":value}),
            Err(reason) => json!({"ok":false,"reason":reason}),
        };
        let raw = serde_json::to_vec(&reply)?;
        if raw.len() > FRAME {
            return Err(io::Error::other("reply limit"));
        }
        stream.write_all(&(raw.len() as u32).to_be_bytes())?;
        stream.write_all(&raw)?;
    }
}
