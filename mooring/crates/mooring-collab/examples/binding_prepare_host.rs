//! Explicit experimental child; never selected by the production executable.
#[cfg(target_os = "linux")]
fn run() -> std::io::Result<()> {
    use mooring_collab::{
        dispatch::Registration,
        experimental_binding::{BindingAdapter, WriteScope},
    };
    use std::{
        os::{fd::FromRawFd, unix::net::UnixStream},
        time::Duration,
    };
    if std::env::args().skip(1).collect::<Vec<_>>() != ["--rpc-fd", "3"] {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidInput,
            "private FD required",
        ));
    }
    // The same inherited-FD entry contract as the existing packaged child.
    let flags = unsafe { libc::fcntl(3, libc::F_GETFD) };
    if flags == -1 || unsafe { libc::fcntl(3, libc::F_SETFD, flags | libc::FD_CLOEXEC) } == -1 {
        return Err(std::io::Error::last_os_error());
    }
    let stream = unsafe { UnixStream::from_raw_fd(3) };
    stream.peer_addr()?;
    let registration = Registration {
        application_id: "wordcraft".into(),
        selected: mooring_protocol::Versions {
            protocol_version: "0.4.0".into(),
            model_version: "0.3.0".into(),
            engine_version: "0.10.0".into(),
        },
        // Local test host reserves this origin. It is not participant authority.
        adapter: BindingAdapter::new(202, WriteScope::WholeDocument)
            .map_err(|_| std::io::Error::other("experimental adapter unavailable"))?,
    };
    mooring_collab::private_rpc::serve_registered(stream, Duration::from_secs(5), registration)
}
fn main() {
    #[cfg(target_os = "linux")]
    if run().is_ok() {
        return;
    }
    eprintln!("experimental binding child stopped");
    std::process::exit(1);
}
