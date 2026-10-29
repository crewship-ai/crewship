#[cfg(target_os = "linux")]
fn main() {
    if mooring_collab::command_experiment::native_channel::run().is_err() {
        eprintln!("mooring command host stopped");
        std::process::exit(1);
    }
}
#[cfg(not(target_os = "linux"))]
fn main() {
    std::process::exit(1);
}
