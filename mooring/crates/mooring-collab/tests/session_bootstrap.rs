#![cfg(unix)]
use serde_json::{json, Value};
use std::{os::unix::net::UnixStream, time::Duration};
#[test]
fn session_bootstrap_selects_only_exact_explicit_offer() {
    for offered in ["mooring.session-causal.v1", "mooring.prepare-causal.v1"] {
        let (mut child, mut peer) = UnixStream::pair().unwrap();
        peer.set_read_timeout(Some(Duration::from_secs(2))).unwrap();
        let host = std::thread::spawn(move || {
            let hello = json!({"type":"hello","request_id":"session-bootstrap","supported":[mooring_collab::dispatch::wordcraft_versions()],"control_profiles":[offered]});
            mooring_collab::write_frame(&mut peer, &serde_json::to_vec(&hello).unwrap()).unwrap();
            let raw = mooring_collab::read_frame(&mut peer).unwrap().unwrap();
            let ready: Value = serde_json::from_slice(&raw).unwrap();
            assert_eq!(ready["request_id"], "session-bootstrap");
            assert_eq!(
                ready["selected"],
                serde_json::to_value(mooring_collab::dispatch::wordcraft_versions()).unwrap()
            );
            if offered == "mooring.session-causal.v1" {
                assert_eq!(ready["selected_control_profile"], offered)
            } else {
                assert!(ready.get("selected_control_profile").is_none());
            }
        });
        let result =
            mooring_collab::private_rpc::bootstrap_session(&mut child, Duration::from_secs(2));
        assert_eq!(result.is_ok(), offered == "mooring.session-causal.v1");
        host.join().unwrap();
    }
}
