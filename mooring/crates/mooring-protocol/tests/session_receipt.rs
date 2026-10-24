use mooring_protocol::{
    boundary::{self, Durability, PendingAtomic, Transport},
    control,
    session_receipt::{self, Binding},
    Counter, Fence, Scope, Versions,
};
use serde_json::{json, Value};
fn host() -> Transport<'static> {
    Transport::AuthenticatedHost {
        issuer: "crewship",
        durabilities: &[Durability::Node, Durability::Replicated],
        required: Durability::Node,
    }
}
fn fixture() -> (Value, Scope, Fence, Versions, Vec<PendingAtomic>) {
    let old: Value = serde_json::from_str(include_str!(
        "../../../protocol/boundary-semantics-fixtures.json"
    ))
    .unwrap();
    let scope: Scope = serde_json::from_value(old["context"]["scope"].clone()).unwrap();
    let fence = Fence {
        generation: Counter(7),
        token: "x".repeat(64),
    };
    let versions = Versions {
        protocol_version: "0.4.0".into(),
        model_version: "0.2.0".into(),
        engine_version: "0.10.0".into(),
    };
    let opaque = control::encode_checkpoint(b"actual-app-restore-still-required").unwrap();
    let changes = old["context"]["changes"]
        .as_array()
        .unwrap()
        .iter()
        .map(|v| PendingAtomic {
            change_id: v["change_id"].as_str().unwrap().into(),
            op_ids: v["op_ids"]
                .as_array()
                .unwrap()
                .iter()
                .map(|x| x.as_str().unwrap().into())
                .collect(),
        })
        .collect();
    let mut value = json!({"type":"session_commit_result","request_id":"batch1","control_profile":"mooring.session-causal.v1","scope":scope,"session_id":"session","runtime_ref":"runtime","fence":fence,"versions":versions,"receipt":old["base_receipt"],"checkpoint":{"scope":scope,"revision":"12","generation":"6","format_version":"test-only-opaque","versions":versions,"encoding":"base64","state":opaque.state,"digest":opaque.digest,"assets":[]},"targets":[{"target_id":"p","revision":"12","structure_revision":"0","deleted":false}]});
    let cp = serde_json::from_value(value["checkpoint"].clone()).unwrap();
    let identity = mooring_protocol::checkpoint::compute(
        &cp,
        mooring_protocol::checkpoint::Limits {
            decoded_bytes: control::MAX_CHECKPOINT_BYTES,
            preimage_bytes: control::MAX_CONTROL_BYTES,
        },
    )
    .unwrap();
    value["checkpoint"]["digest"] = json!(identity.full_digest());
    (value, scope, fence, versions, changes)
}
#[test]
fn exact_session_receipt_binds_original_pending_batch_before_checkpoint_bytes() {
    let (value, scope, fence, versions, changes) = fixture();
    let binding = Binding {
        request_id: "batch1",
        scope: &scope,
        session_id: "session",
        runtime_ref: "runtime",
        fence: &fence,
        versions: &versions,
        changes: &changes,
    };
    let raw = serde_json::to_vec(&value).unwrap();
    let checked = session_receipt::validate(&raw, &host(), &binding).unwrap();
    assert_eq!(checked.receipt().revision(), Counter(12));
    assert_eq!(checked.result().checkpoint.generation, Counter(6)); // provenance is not live owner
    assert_eq!(
        checked.checkpoint_bytes(),
        b"actual-app-restore-still-required"
    );
    let goldens: Value = serde_json::from_str(include_str!(
        "../../../protocol/session-settlement-fixtures.json"
    ))
    .unwrap();
    for case in goldens["mutations"].as_array().unwrap() {
        let path = case["path"].as_str().unwrap();
        let mutation = case["value"].clone();
        let mut tampered = value.clone();
        *tampered.pointer_mut(path).unwrap() = mutation;
        assert!(
            session_receipt::validate(&serde_json::to_vec(&tampered).unwrap(), &host(), &binding)
                .is_err(),
            "accepted {path}"
        );
    }
    let mut swapped = value.clone();
    swapped["receipt"]["changes"]
        .as_array_mut()
        .unwrap()
        .swap(0, 1);
    assert!(
        session_receipt::validate(&serde_json::to_vec(&swapped).unwrap(), &host(), &binding)
            .is_err()
    );
    let mut duplicate = value.clone();
    duplicate["targets"]
        .as_array_mut()
        .unwrap()
        .push(value["targets"][0].clone());
    assert!(
        session_receipt::validate(&serde_json::to_vec(&duplicate).unwrap(), &host(), &binding)
            .is_err()
    );
    assert!(session_receipt::validate(&raw, &Transport::Participant, &binding).is_err());
    assert!(session_receipt::validate(&raw, &Transport::Runtime, &binding).is_err());
    assert_eq!(serde_json::to_vec(&value).unwrap(), raw);
}
#[test]
fn only_known_session_receipts_obtain_larger_private_budget_and_unknown_is_not_terminal() {
    let (mut value, scope, fence, versions, _) = fixture();
    let ids: Vec<String> = (0..256)
        .map(|i| format!("op{i}{}", "é".repeat(190)))
        .collect();
    let changes = vec![PendingAtomic {
        change_id: "large".into(),
        op_ids: ids.clone(),
    }];
    let operations:Vec<_>=ids.iter().map(|id|json!({"op_id":id,"status":"rejected","reason":{"code":"forbidden","message":"é".repeat(1000),"retry":"new_operation_after_refresh"}})).collect();
    value["receipt"]["changes"] =
        json!([{"change_id":"large","status":"rejected","operations":operations}]);
    let binding = Binding {
        request_id: "batch1",
        scope: &scope,
        session_id: "session",
        runtime_ref: "runtime",
        fence: &fence,
        versions: &versions,
        changes: &changes,
    };
    let raw = serde_json::to_vec(&value).unwrap();
    assert!(raw.len() > 262144 && raw.len() < 2 * 1024 * 1024);
    assert!(session_receipt::validate(&raw, &host(), &binding).is_ok());
    let ordinary = serde_json::to_vec(&value["receipt"]).unwrap();
    assert!(boundary::validate_commit_result(
        &ordinary,
        &host(),
        &boundary::CommitBinding {
            request_id: "batch1",
            scope: &scope,
            generation: Counter(7),
            changes: &changes
        }
    )
    .is_err());
    value["receipt"]["changes"][0]["operations"][0]["reason"]["code"] = json!("unknown_outcome");
    assert!(
        session_receipt::validate(&serde_json::to_vec(&value).unwrap(), &host(), &binding).is_err()
    );
}
