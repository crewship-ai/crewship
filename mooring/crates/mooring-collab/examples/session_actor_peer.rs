//! Explicit integration fixture, never selected by production main. The actual
//! inherited host must supply canonical rights, sealed originals, SQL COMMIT
//! and independent final delivery gates. This child issues no authority.
#[cfg(target_os = "linux")]
fn run() -> std::io::Result<()> {
    use mooring_protocol::boundary::{Durability, Transport};
    use std::{os::fd::FromRawFd, os::unix::net::UnixStream, time::Duration};
    if std::env::args().skip(1).collect::<Vec<_>>() != ["--rpc-fd", "3"] {
        return Err(std::io::Error::other("inherited fixture FD required"));
    }
    let flags = unsafe { libc::fcntl(3, libc::F_GETFD) };
    if flags == -1 || unsafe { libc::fcntl(3, libc::F_SETFD, flags | libc::FD_CLOEXEC) } == -1 {
        return Err(std::io::Error::last_os_error());
    }
    let mut stream = unsafe { UnixStream::from_raw_fd(3) };
    stream.peer_addr()?;
    mooring_collab::private_rpc::bootstrap_session(&mut stream, Duration::from_secs(5))?;
    // Logical host mode only: browser/authentication/SQL authority is not
    // inferred from an issuer string or numeric origin in any request.
    let host = Transport::AuthenticatedHost {
        issuer: "crewship",
        durabilities: &[Durability::Node],
        required: Durability::Node,
    };
    let driver =
        mooring_collab::session_driver::Driver::from_negotiated_inherited_stream_with_origins(
            stream,
            &host,
            mooring_core::lease::boottime,
        )?;
    let mut actor = driver.into_actor()?;
    loop {
        actor.step(&host, Duration::from_millis(20))?;
    }
}
#[cfg(not(target_os = "linux"))]
fn run() -> std::io::Result<()> {
    Err(std::io::Error::other("Linux fixture only"))
}
fn main() {
    if run().is_err() {
        std::process::exit(1)
    }
}
