//! Closed private Go/Rust host channel; never a browser or agent protocol.
use super::super::{id, Admission, Result};
use serde::Deserialize;
use std::{
    io::{Read, Write},
    os::unix::net::UnixStream,
    time::{Duration, Instant},
};

pub const LIMIT: usize = 32 * 1024 * 1024;
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TrustedAdmission {
    participant: String,
    author: String,
    time: String,
    epoch: u64,
    writer: bool,
}
impl TrustedAdmission {
    pub fn admitted(&self) -> Result<Admission> {
        Admission::new(
            &self.participant,
            &self.author,
            &self.time,
            self.epoch,
            self.writer,
        )
    }
}
#[derive(Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub enum Request {
    Init {
        protocol: u8,
        request_id: String,
        document: String,
        generation: String,
    },
    Stage {
        protocol: u8,
        request_id: String,
        base_revision: u64,
        base_digest: String,
        input_json: String,
        admission: TrustedAdmission,
    },
    Confirm {
        protocol: u8,
        request_id: String,
        stage_ref: String,
        admission: TrustedAdmission,
    },
    Reject {
        protocol: u8,
        request_id: String,
        stage_ref: String,
    },
    Restore {
        protocol: u8,
        request_id: String,
        revision: u64,
        checkpoint_json: String,
        generation: String,
    },
    Status {
        protocol: u8,
        request_id: String,
        op_id: String,
        expected_fingerprint: String,
    },
    Projection {
        protocol: u8,
        request_id: String,
    },
}
impl Request {
    pub fn correlation(&self) -> Result<&str> {
        let (protocol, request_id) = match self {
            Self::Init {
                protocol,
                request_id,
                ..
            }
            | Self::Stage {
                protocol,
                request_id,
                ..
            }
            | Self::Confirm {
                protocol,
                request_id,
                ..
            }
            | Self::Reject {
                protocol,
                request_id,
                ..
            }
            | Self::Restore {
                protocol,
                request_id,
                ..
            }
            | Self::Status {
                protocol,
                request_id,
                ..
            }
            | Self::Projection {
                protocol,
                request_id,
            } => (protocol, request_id),
        };
        if *protocol != 1 || !id(request_id) {
            return Err("invalid_request");
        }
        Ok(request_id)
    }
}
fn read_until(stream: &mut UnixStream, mut data: &mut [u8], until: Instant) -> Result<()> {
    while !data.is_empty() {
        let remaining = until
            .checked_duration_since(Instant::now())
            .filter(|n| !n.is_zero())
            .ok_or("frame_timeout")?;
        stream
            .set_read_timeout(Some(remaining))
            .map_err(|_| "frame_io")?;
        match stream.read(data) {
            Ok(0) => return Err("frame_truncated"),
            Ok(n) => {
                data = &mut data[n..];
            }
            Err(e) if e.kind() == std::io::ErrorKind::Interrupted => {}
            Err(_) => return Err("frame_io"),
        }
    }
    Ok(())
}
pub fn read_frame(stream: &mut UnixStream) -> Result<Option<Vec<u8>>> {
    // No request is in flight while waiting for its first byte. Once a frame
    // starts, one total deadline covers header and body, including short reads.
    stream.set_read_timeout(None).map_err(|_| "frame_io")?;
    let mut header = [0; 4];
    loop {
        match stream.read(&mut header[..1]) {
            Ok(0) => return Ok(None),
            Ok(_) => break,
            Err(e) if e.kind() == std::io::ErrorKind::Interrupted => {}
            Err(_) => return Err("frame_io"),
        }
    }
    let until = Instant::now() + Duration::from_secs(5);
    read_until(stream, &mut header[1..], until)?;
    let size = u32::from_be_bytes(header) as usize;
    if size == 0 || size > LIMIT {
        return Err("frame_limit");
    }
    let mut raw = vec![0; size];
    read_until(stream, &mut raw, until)?;
    std::str::from_utf8(&raw).map_err(|_| "frame_utf8")?;
    Ok(Some(raw))
}
pub fn write_frame(stream: &mut UnixStream, raw: &[u8]) -> Result<()> {
    if raw.is_empty() || raw.len() > LIMIT {
        return Err("frame_limit");
    }
    let until = Instant::now() + Duration::from_secs(5);
    let header = (raw.len() as u32).to_be_bytes();
    for mut data in [header.as_slice(), raw] {
        while !data.is_empty() {
            let remaining = until
                .checked_duration_since(Instant::now())
                .filter(|n| !n.is_zero())
                .ok_or("frame_timeout")?;
            stream
                .set_write_timeout(Some(remaining))
                .map_err(|_| "frame_io")?;
            match stream.write(data) {
                Ok(0) => return Err("frame_io"),
                Ok(n) => {
                    data = &data[n..];
                }
                Err(e) if e.kind() == std::io::ErrorKind::Interrupted => {}
                Err(_) => return Err("frame_io"),
            }
        }
    }
    Ok(())
}
