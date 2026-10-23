//! Private inherited host control. Stateless preparation is not a host commit.
use std::io::{self, Read, Write};
pub mod dispatch;

pub const MAX_FRAME: usize = 262_144;

/// Bounds raw allocation before decoding; a clean EOF is distinct from truncation.
pub fn read_frame(reader: &mut impl Read) -> io::Result<Option<Vec<u8>>> {
    read_profile_frame(reader, false)
}
fn read_profile_frame(reader: &mut impl Read, control: bool) -> io::Result<Option<Vec<u8>>> {
    let limit = if control {
        mooring_protocol::control::MAX_CONTROL_BYTES
    } else {
        MAX_FRAME
    };
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
    if length == 0 || length > limit {
        return Err(invalid("raw frame limit"));
    }
    let mut data = vec![0; length];
    reader.read_exact(&mut data)?;
    std::str::from_utf8(&data).map_err(|_| invalid("invalid UTF-8"))?;
    frame_syntax(&data, control)?;
    Ok(Some(data))
}

pub fn write_frame(writer: &mut impl Write, data: &[u8]) -> io::Result<()> {
    write_profile_frame(writer, data, false)
}
fn write_profile_frame(writer: &mut impl Write, data: &[u8], control: bool) -> io::Result<()> {
    let limit = if control {
        mooring_protocol::control::MAX_CONTROL_BYTES
    } else {
        MAX_FRAME
    };
    if data.is_empty() || data.len() > limit {
        return Err(invalid("raw frame limit"));
    }
    std::str::from_utf8(data).map_err(|_| invalid("invalid UTF-8"))?;
    frame_syntax(data, control)?;
    writer.write_all(&(data.len() as u32).to_be_bytes())?;
    writer.write_all(data)?;
    writer.flush()
}

fn frame_syntax(data: &[u8], control: bool) -> io::Result<()> {
    let preflight = if control {
        mooring_protocol::control::preflight(data)
    } else {
        mooring_protocol::wire::preflight(data)
    };
    preflight.map_err(|_| invalid("JSON resource limit"))?;
    // Framing does not allocate a JSON value or interpret payload semantics.
    // The subsequent typed boundary rejects duplicates/nonfinite values via JCS.
    serde_json::from_slice::<serde::de::IgnoredAny>(data).map_err(|_| invalid("invalid JSON"))?;
    Ok(())
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

    /// Legacy generic bootstrap fixture tuple. Registered adapters supply their
    /// own qualified model/engine tuple; production does not infer it here.
    pub fn versions() -> Versions {
        Versions {
            protocol_version: "0.4.0".into(),
            model_version: "1.0.0".into(),
            engine_version: env!("CARGO_PKG_VERSION").into(),
        }
    }

    pub fn bootstrap(stream: &mut UnixStream, bootstrap_timeout: Duration) -> io::Result<()> {
        bootstrap_selected(stream, bootstrap_timeout, &versions(), false).map(|_| ())
    }
    fn bootstrap_selected(
        stream: &mut UnixStream,
        bootstrap_timeout: Duration,
        supported_version: &Versions,
        supports_control: bool,
    ) -> io::Result<bool> {
        let deadline = Instant::now()
            .checked_add(bootstrap_timeout)
            .ok_or_else(|| invalid("invalid bootstrap timeout"))?;
        let frame = read_frame(&mut BootstrapReader { stream, deadline })?
            .ok_or_else(|| invalid("missing host hello"))?;
        let normalized = mooring_protocol::control::validate_bootstrap(
            &frame,
            mooring_protocol::control::Peer::InheritedHost,
        )
        .map_err(|_| invalid("invalid hello"))?;
        let hello: Hello = serde_json::from_value(normalized.value().clone())
            .map_err(|_| invalid("invalid hello"))?;
        let supported = [supported_version.clone()];
        let selected = negotiate(&hello, &supported).map_err(invalid)?;
        let selected_control = supports_control
            && hello.control_profiles.as_ref().is_some_and(|profiles| {
                profiles.contains(&mooring_protocol::HelloControlProfilesItem::MooringPrepareV1)
            });
        let ready = RuntimeReady {
            r#type: RuntimeReadyType::RuntimeReady,
            selected_control_profile: selected_control
                .then_some(mooring_protocol::RuntimeReadySelectedControlProfile::MooringPrepareV1),
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
        Ok(selected_control)
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
    /// Caller must supply the verified inherited host channel, not a listener
    /// accepted from a participant. Bootstrap selects control explicitly before
    /// larger frame allocation; every subsequent message is stateless prepare.
    pub fn serve_registered<A: mooring_core::adapter::PrepareAdapter>(
        mut stream: UnixStream,
        bootstrap_timeout: Duration,
        registration: crate::dispatch::Registration<A>,
    ) -> io::Result<()> {
        let selected =
            bootstrap_selected(&mut stream, bootstrap_timeout, &registration.selected, true)?;
        if !selected {
            if read_frame(&mut stream)?.is_some() {
                return Err(invalid("private profile not selected"));
            }
            return Ok(());
        }
        while let Some(frame) = read_profile_frame(&mut stream, true)? {
            let response = registration.respond(&frame)?;
            write_profile_frame(&mut stream, &response, true)?;
        }
        Ok(())
    }
}
