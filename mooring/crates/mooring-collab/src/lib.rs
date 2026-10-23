//! Private host-child bootstrap only. No participant, content or Saved dispatch.
use std::io::{self, Read, Write};

pub const MAX_FRAME: usize = 262_144;

/// Bounds raw allocation before decoding; a clean EOF is distinct from truncation.
pub fn read_frame(reader: &mut impl Read) -> io::Result<Option<Vec<u8>>> {
    let mut header = [0u8; 4];
    loop {
        match reader.read(&mut header[..1]) {
            Ok(0) => return Ok(None),
            Ok(_) => break,
            Err(e) if e.kind() == io::ErrorKind::Interrupted => continue,
            Err(e) => return Err(e),
        }
    }
    reader.read_exact(&mut header[1..])?;
    let length = u32::from_be_bytes(header) as usize;
    if length == 0 || length > MAX_FRAME {
        return Err(invalid("raw frame limit"));
    }
    let mut data = vec![0; length];
    reader.read_exact(&mut data)?;
    std::str::from_utf8(&data).map_err(|_| invalid("invalid UTF-8"))?;
    // This only checks syntax, not JCS, duplicate arbitrary keys, or schema.
    serde_json::from_slice::<serde_json::Value>(&data).map_err(|_| invalid("invalid JSON"))?;
    Ok(Some(data))
}

pub fn write_frame(writer: &mut impl Write, data: &[u8]) -> io::Result<()> {
    if data.is_empty() || data.len() > MAX_FRAME {
        return Err(invalid("raw frame limit"));
    }
    std::str::from_utf8(data).map_err(|_| invalid("invalid UTF-8"))?;
    serde_json::from_slice::<serde_json::Value>(data).map_err(|_| invalid("invalid JSON"))?;
    writer.write_all(&(data.len() as u32).to_be_bytes())?;
    writer.write_all(data)?;
    writer.flush()
}

fn invalid(message: &'static str) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, message)
}

#[cfg(unix)]
pub mod private_rpc {
    use super::*;
    use mooring_protocol::{negotiate, Hello, RuntimeReady, RuntimeReadyType, Versions};
    use std::os::unix::net::UnixStream;
    use std::time::{Duration, Instant};

    /// Recomputes the remaining absolute bootstrap budget after every read.
    /// Fragmentation cannot extend the deadline by resetting a per-read timeout.
    struct BootstrapReader<'a> {
        stream: &'a mut UnixStream,
        deadline: Instant,
    }
    impl Read for BootstrapReader<'_> {
        fn read(&mut self, buffer: &mut [u8]) -> io::Result<usize> {
            let remaining = self
                .deadline
                .checked_duration_since(Instant::now())
                .filter(|remaining| !remaining.is_zero())
                .ok_or_else(|| io::Error::new(io::ErrorKind::TimedOut, "bootstrap deadline"))?;
            self.stream.set_read_timeout(Some(remaining))?;
            self.stream.read(buffer)
        }
    }

    pub fn versions() -> Versions {
        Versions {
            protocol_version: "0.4.0".into(),
            model_version: "1.0.0".into(),
            engine_version: env!("CARGO_PKG_VERSION").into(),
        }
    }

    pub fn bootstrap(stream: &mut UnixStream, bootstrap_timeout: Duration) -> io::Result<()> {
        let deadline = Instant::now()
            .checked_add(bootstrap_timeout)
            .ok_or_else(|| invalid("invalid bootstrap timeout"))?;
        let frame = read_frame(&mut BootstrapReader { stream, deadline })?
            .ok_or_else(|| invalid("missing host hello"))?;
        // Concrete structs reject duplicate/unknown fields, including version keys.
        let hello: Hello = serde_json::from_slice(&frame).map_err(|_| invalid("invalid hello"))?;
        let supported = [versions()];
        let selected = negotiate(&hello, &supported).map_err(invalid)?;
        let ready = RuntimeReady {
            r#type: RuntimeReadyType::RuntimeReady,
            selected_control_profile: None,
            request_id: hello.request_id,
            selected: selected.clone(),
        };
        let response = serde_json::to_vec(&ready).map_err(|_| invalid("readiness encoding"))?;
        let remaining = deadline
            .checked_duration_since(Instant::now())
            .filter(|remaining| !remaining.is_zero())
            .ok_or_else(|| io::Error::new(io::ErrorKind::TimedOut, "bootstrap deadline"))?;
        stream.set_write_timeout(Some(remaining))?;
        write_frame(stream, &response)?;
        stream.set_read_timeout(None)?;
        stream.set_write_timeout(None)?;
        Ok(())
    }

    pub fn serve(mut stream: UnixStream, bootstrap_timeout: Duration) -> io::Result<()> {
        bootstrap(&mut stream, bootstrap_timeout)?;
        // Future dispatch must validate scope, host authority, fencing, JCS and
        // delivery gates. Today any post-bootstrap message fails closed.
        if read_frame(&mut stream)?.is_some() {
            return Err(invalid("participant dispatch unavailable"));
        }
        Ok(())
    }
}
