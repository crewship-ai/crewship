use mooring_wordcraft::{
    experimental_draft::{ClientError, DraftAccess, DraftReplica},
    Document,
};
use serde_json::{json, Value};

// Original full-native overlap failure is retained in the experiment artifact.
// E3 qualifies explicit refusal, not a replacement deletion-ownership model.
#[test]
fn e3_overlapping_delete_undo_is_refused_with_original_draft_unchanged() {
    let mut before: Value =
        serde_json::from_str(include_str!("fixtures/d1-engine-canonical.json")).unwrap();
    before["body"] = json!([{"kind":"para","text":"aa","runs":[{"len":2}]}]);
    let cp = Document::import(before.clone(), vec!["p_one".into()])
        .unwrap()
        .checkpoint()
        .unwrap();
    let mut alice = DraftReplica::restore(&cp, 17, DraftAccess::Writer).unwrap();
    let mut bob = DraftReplica::restore(&cp, 18, DraftAccess::Writer).unwrap();
    let sa = alice
        .observe_native_selection(&before, "p_one", 0, 1)
        .unwrap();
    let sb = bob
        .observe_native_selection(&before, "p_one", 0, 1)
        .unwrap();
    let mut after = before.clone();
    after["body"][0]["text"] = json!("a");
    after["body"][0]["runs"] = json!([{"len":1}]);
    let a = alice
        .native_transition_selected("delete_a", &before, &after, &sa)
        .unwrap();
    let b = bob
        .native_transition_selected("delete_b", &before, &after, &sb)
        .unwrap();
    alice
        .apply_verified_host_delta(b.origin, &b.update, &b.targets)
        .unwrap();
    bob.apply_verified_host_delta(a.origin, &a.update, &a.targets)
        .unwrap();
    assert_eq!(alice.native().unwrap(), bob.native().unwrap());
    let native = alice.native().unwrap();
    let checkpoint = alice.optimistic_checkpoint().unwrap();
    let frontier = alice.state_vector();
    let ids = alice.stable_ids().unwrap();
    assert_eq!(
        alice.undo_e3("undo_own_delete"),
        Err(ClientError::UnsupportedUndo)
    );
    assert_eq!(alice.native().unwrap(), native);
    assert_eq!(alice.optimistic_checkpoint().unwrap(), checkpoint);
    assert_eq!(alice.state_vector(), frontier);
    assert_eq!(alice.stable_ids().unwrap(), ids);
    // Exact retry of the original deletion remains the original proposal.
    assert_eq!(
        alice
            .native_transition_selected("delete_a", &before, &after, &sa)
            .unwrap(),
        a
    );
}

fn simple_native(properties: Value) -> Value {
    let mut native: Value =
        serde_json::from_str(include_str!("fixtures/d1-engine-canonical.json")).unwrap();
    native["body"] = json!([{"kind":"para","text":"aa","runs":[{"len":2,"props":properties}]}]);
    if native["body"][0]["runs"][0]["props"]
        .as_object()
        .unwrap()
        .is_empty()
    {
        native["body"][0]["runs"][0]
            .as_object_mut()
            .unwrap()
            .remove("props");
    }
    native
}
fn command(value: Value) -> mooring_wordcraft::command::Command {
    serde_json::from_value(value).unwrap()
}
#[test]
fn e3_own_insert_and_format_undo_redo_preserve_full_peer_native_in_both_orders() {
    for kind in ["insert", "different-format", "same-format", "clear-inherit"] {
        for peer_first in [false, true] {
            let initial_props = if kind == "clear-inherit" {
                json!({"bold":true})
            } else {
                json!({})
            };
            let native = simple_native(initial_props);
            let cp = Document::import(native.clone(), vec!["p_one".into()])
                .unwrap()
                .checkpoint()
                .unwrap();
            let mut alice = DraftReplica::restore(&cp, 17, DraftAccess::Writer).unwrap();
            let mut bob = DraftReplica::restore(&cp, 18, DraftAccess::Writer).unwrap();
            let own_command = match kind {
                "insert" => {
                    json!({"kind":"text.insert","target_id":"p_one","at":{"kind":"end"},"text":" Č😀","properties":{"bold":false,"italic":true}})
                }
                "clear-inherit" => {
                    json!({"kind":"text.format","target_id":"p_one","start":{"kind":"start"},"end":{"kind":"end"},"properties":{"bold":null}})
                }
                _ => {
                    json!({"kind":"text.format","target_id":"p_one","start":{"kind":"start"},"end":{"kind":"end"},"properties":{"bold":true}})
                }
            };
            let peer_props = if kind == "same-format" {
                json!({"bold":false})
            } else {
                json!({"italic":true})
            };
            let a = alice.edit("own", command(own_command)).unwrap();
            let b = bob.edit("peer",command(json!({"kind":"text.format","target_id":"p_one","start":{"kind":"start"},"end":{"kind":"end"},"properties":peer_props}))).unwrap();
            let peer_only = bob.native().unwrap();
            if peer_first {
                bob.apply_verified_host_delta(a.origin, &a.update, &a.targets)
                    .unwrap();
                alice
                    .apply_verified_host_delta(b.origin, &b.update, &b.targets)
                    .unwrap();
            } else {
                alice
                    .apply_verified_host_delta(b.origin, &b.update, &b.targets)
                    .unwrap();
                bob.apply_verified_host_delta(a.origin, &a.update, &a.targets)
                    .unwrap();
            }
            let combined = alice.native().unwrap();
            assert_eq!(
                combined,
                bob.native().unwrap(),
                "convergence {kind}/{peer_first}"
            );
            let undo = alice.undo_e3("undo-own").unwrap();
            assert_eq!(
                alice.native().unwrap(),
                peer_only,
                "FULL native peer-only undo {kind}/{peer_first}"
            );
            assert_eq!(
                alice.undo_e3("undo-own").unwrap(),
                undo,
                "immutable undo retry"
            );
            bob.apply_verified_host_delta(undo.origin, &undo.update, &undo.targets)
                .unwrap();
            assert_eq!(bob.native().unwrap(), peer_only);
            let redo = alice.redo_e3("redo-own").unwrap();
            bob.apply_verified_host_delta(redo.origin, &redo.update, &redo.targets)
                .unwrap();
            assert_eq!(
                alice.native().unwrap(),
                combined,
                "FULL native redo {kind}/{peer_first}"
            );
            assert_eq!(bob.native().unwrap(), combined);
            let restored = Document::restore(&alice.optimistic_checkpoint().unwrap()).unwrap();
            assert_eq!(restored.native().unwrap(), combined);
            assert_eq!(restored.block_ids().unwrap(), vec!["p_one"]);
        }
    }
}

#[test]
fn e3_delete_undo_and_redo_refusal_never_skip_or_pop_history() {
    let before = simple_native(json!({}));
    let cp = Document::import(before.clone(), vec!["p_one".into()])
        .unwrap()
        .checkpoint()
        .unwrap();
    let mut client = DraftReplica::restore(&cp, 17, DraftAccess::Writer).unwrap();
    client.edit("insert",command(json!({"kind":"text.insert","target_id":"p_one","at":{"kind":"end"},"text":"x","properties":null}))).unwrap();
    let full = client.native().unwrap();
    let selection = client
        .observe_native_selection(&full, "p_one", 0, 1)
        .unwrap();
    let mut after = full.clone();
    after["body"][0]["text"] = json!("ax");
    after["body"][0]["runs"][0]["len"] = json!(2);
    client
        .native_transition_selected("delete", &full, &after, &selection)
        .unwrap();
    let checkpoint = client.optimistic_checkpoint().unwrap();
    for id in ["deny1", "deny2"] {
        assert_eq!(client.undo_e3(id), Err(ClientError::UnsupportedUndo));
        assert_eq!(client.optimistic_checkpoint().unwrap(), checkpoint);
    }
    // Research-only legacy API establishes a deletion redo stack. The E3 API
    // must deny it too without skipping to the older insertion action.
    client.undo("legacy-undo-delete").unwrap();
    assert_eq!(
        client.undo_e3("legacy-undo-delete"),
        Err(ClientError::OperationConflict)
    );
    assert_eq!(client.native().unwrap(), full);
    let checkpoint = client.optimistic_checkpoint().unwrap();
    assert_eq!(
        client.redo_e3("deny-redo"),
        Err(ClientError::UnsupportedUndo)
    );
    assert_eq!(client.optimistic_checkpoint().unwrap(), checkpoint);
    // Denial did not consume that same original redo history entry.
    client.redo("legacy-redo-delete").unwrap();
    assert_eq!(client.native().unwrap(), after);
}
