//! The sole entry point is an inherited private FD, never a public listener.
#[cfg(target_os = "linux")]
fn run() -> std::io::Result<()> {
    use std::os::fd::FromRawFd;
    use std::os::unix::net::UnixStream;
    use std::time::Duration;
    let arguments: Vec<_> = std::env::args().skip(1).collect();
    if arguments != ["--rpc-fd", "3"] {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidInput,
            "private FD required",
        ));
    }
    // Validate the descriptor before taking ownership (including a standalone
    // invocation without Go). No other thread can close it during bootstrap.
    let descriptor_flags = unsafe { libc::fcntl(3, libc::F_GETFD) };
    if descriptor_flags == -1 {
        return Err(std::io::Error::last_os_error());
    }
    // FD3 authenticates the host channel; never pass it to future descendants.
    if unsafe { libc::fcntl(3, libc::F_SETFD, descriptor_flags | libc::FD_CLOEXEC) } == -1 {
        return Err(std::io::Error::last_os_error());
    }
    // Go owns bootstrap and passes exactly one socketpair end as ExtraFiles[0].
    // This process owns descriptor 3 from this point and never opens a listener.
    let stream = unsafe { UnixStream::from_raw_fd(3) };
    stream.peer_addr()?;
    mooring_collab::private_rpc::serve_causal_registered(
        stream,
        Duration::from_secs(5),
        mooring_collab::dispatch::wordcraft_registration(),
    )
}
#[cfg(not(target_os = "linux"))]
fn run() -> std::io::Result<()> {
    Err(std::io::Error::new(
        std::io::ErrorKind::Unsupported,
        "Linux runtime required",
    ))
}
fn main() {
    if run().is_err() {
        // No untrusted frames or credentials enter diagnostics.
        std::process::exit(1);
    }
}
