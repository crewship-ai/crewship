
use mooring_protocol::{control::{self,Peer},session_control,wire};
use serde_json::Value;
#[test]
fn review_4131_large_descriptor_raw_boundary_is_exact_and_never_public(){
 let fixture:Value=serde_json::from_str(include_str!("../../../protocol/checkpoint-description-fixtures.json")).unwrap();
 let message=&fixture["cases"].as_array().unwrap().iter().find(|c|c["id"]=="session_checkpoint_described-valid").unwrap()["message"];
 let mut raw=serde_json::to_vec(message).unwrap();raw.resize(control::MAX_CONTROL_BYTES,b' ');
 assert!(session_control::validate(&raw,Peer::InheritedRuntime).is_ok());assert!(wire::validate_bounded_envelope(&raw).is_err());
 raw.push(b' ');assert_eq!(session_control::validate(&raw,Peer::InheritedRuntime).unwrap_err(),wire::WireError::ResourceLimit);
}
