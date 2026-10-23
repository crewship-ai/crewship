//! Actual model-v2 projection/packing and action identity regressions.
//! These replicas do not qualify human authority or causal-read admission.
use crate::*;
use mooring_text::yrs::{
    types::Attrs, updates::decoder::Decode, ClientID, OffsetKind, Options, Origin, TextRef,
    UndoManager, Update,
};
use std::{
    cell::RefCell,
    collections::{BTreeMap, HashSet},
    rc::Rc,
    sync::Arc,
};

struct Replica {
    doc: Doc,
    base: Value,
    ids: Vec<String>,
    origin: u64,
    undo: UndoManager,
}
impl Replica {
    fn initial(origin: u64) -> Self {
        let native: Value =
            serde_json::from_str(include_str!("../tests/fixtures/d1-native.json")).unwrap();
        let ids: Vec<_> = (0..8).map(|i| format!("p{i}")).collect();
        let document = Document::import(native.clone(), ids.clone()).unwrap();
        let update = document
            .doc
            .transact()
            .encode_state_as_update_v1(&StateVector::default());
        Self::from_update(native, ids, origin, &update)
    }
    fn from_update(base: Value, ids: Vec<String>, origin: u64, bytes: &[u8]) -> Self {
        let doc = Doc::with_options(Options {
            client_id: ClientID::new(origin),
            offset_kind: OffsetKind::Utf16,
            skip_gc: true,
            ..Options::default()
        });
        doc.transact_mut()
            .apply_update(Update::decode_v1(bytes).unwrap())
            .unwrap();
        let texts = doc.get_or_insert_map("wordcraft.texts");
        let mut undo = UndoManager::with_options(mooring_text::yrs::undo::Options {
            capture_timeout_millis: 0,
            tracked_origins: HashSet::from([Origin::from(ClientID::new(origin))]),
            capture_transaction: None,
            timestamp: Arc::new(|| 0_u64),
            init_undo_stack: vec![],
            init_redo_stack: vec![],
        });
        undo.expand_scope(&doc, &texts);
        Self {
            doc,
            base,
            ids,
            origin,
            undo,
        }
    }
    fn fork(&self, origin: u64) -> Self {
        Self::from_update(self.base.clone(), self.ids.clone(), origin, &self.update())
    }
    fn update(&self) -> Vec<u8> {
        self.doc
            .transact()
            .encode_state_as_update_v1(&StateVector::default())
    }
    fn text(&self) -> TextRef {
        let txn = self.doc.transact();
        let Some(Out::YText(text)) = txn
            .get_map("wordcraft.texts")
            .unwrap()
            .get(&txn, &self.ids[0])
        else {
            panic!()
        };
        text
    }
    fn stamp(&self) -> u64 {
        self.doc
            .transact()
            .state_vector()
            .iter()
            .try_fold(1_u64, |n, (_, clock)| n.checked_add(*clock as u64))
            .unwrap()
    }
    fn key(&self, field: &str, stamp: u64) -> Arc<str> {
        format!("wc2:{field}:{stamp}:{}", self.origin).into()
    }
    fn capture(&mut self, change: impl FnOnce(&mut Self)) -> Vec<u8> {
        let bytes = Rc::new(RefCell::new(Vec::new()));
        let received = bytes.clone();
        self.doc
            .observe_update_v1("capture", move |_, event| {
                *received.borrow_mut() = event.update.clone();
            })
            .unwrap();
        change(self);
        self.doc.unobserve_update_v1("capture").unwrap();
        bytes.take()
    }
    fn insert(&mut self, value: &str, properties: BTreeMap<&str, bool>) -> Vec<u8> {
        self.capture(|replica| {
            let stamp = replica.stamp();
            let mut attrs = Attrs::new();
            attrs.insert(
                replica.key("empty", stamp),
                Any::Bool(properties.is_empty()),
            );
            for field in CHARACTER_FLAGS {
                attrs.insert(
                    replica.key(field, stamp),
                    properties
                        .get(field)
                        .copied()
                        .map(Any::Bool)
                        .unwrap_or(Any::String("inherit".into())),
                );
            }
            replica.text().insert_with_attributes(
                &mut replica.doc.transact_mut_with(ClientID::new(replica.origin)),
                0,
                value,
                attrs,
            );
        })
    }
    fn format(&mut self, value: Option<bool>) -> Vec<u8> {
        self.capture(|replica| {
            let attrs = Attrs::from([(
                replica.key("bold", replica.stamp()),
                value
                    .map(Any::Bool)
                    .unwrap_or(Any::String("inherit".into())),
            )]);
            replica.text().format(
                &mut replica.doc.transact_mut_with(ClientID::new(replica.origin)),
                0,
                1,
                attrs,
            );
        })
    }
    fn apply(&mut self, bytes: &[u8]) {
        self.doc
            .transact_mut_with("verified-peer")
            .apply_update(Update::decode_v1(bytes).unwrap())
            .unwrap();
        assert!(!self.doc.transact().has_missing_updates());
    }
    fn undo(&mut self) -> Vec<u8> {
        self.capture(|replica| assert!(replica.undo.undo_blocking()))
    }
    fn redo(&mut self) -> Vec<u8> {
        self.capture(|replica| assert!(replica.undo.redo_blocking()))
    }
    fn document(&self) -> Document {
        Document {
            doc: self.doc.clone(),
            read_only: false,
            project_text: true,
            model: format::Model::V2,
        }
    }
    fn native(&self) -> Value {
        self.document().native().unwrap()
    }
    fn saved_restore(&self) {
        let bytes = self.document().checkpoint().unwrap();
        let restored = Document::restore(&bytes).unwrap();
        assert!(restored.native().unwrap() == self.native());
        assert_eq!(restored.block_ids().unwrap(), self.ids);
        assert_eq!(
            restored.doc.transact().state_vector(),
            self.doc.transact().state_vector()
        );
    }
}
fn prefix(base: &Value, value: &str, props: Value) -> Value {
    let mut expected = base.clone();
    expected["body"][0]["text"] = json!(format!(
        "{value}{}",
        base["body"][0]["text"].as_str().unwrap()
    ));
    expected["body"][0]["runs"]
        .as_array_mut()
        .unwrap()
        .insert(0, json!({"len":value.len(),"props":props}));
    expected
}
#[test]
fn full_native_same_value_peer_insert_undo_and_redo_preserve_every_field() {
    let mut own = Replica::initial(17);
    let base = own.native();
    let props = BTreeMap::from([("bold", false), ("italic", true)]);
    let _a = own.insert("OWN", props.clone());
    let mut peer = own.fork(18);
    let b = peer.insert("PEER", props);
    own.apply(&b);
    let merged = own.native();
    let own_undo = own.undo();
    own.saved_restore();
    assert!(
        own.native() == prefix(&base, "PEER", json!({"bold":false,"italic":true})),
        "FULL peer-only native equality"
    );
    peer.apply(&own_undo);
    let peer_undo = peer.undo();
    own.apply(&peer_undo);
    assert!(
        own.native() == base && peer.native() == base,
        "both origin undos must return full baseline"
    );
    let peer_redo = peer.redo();
    own.apply(&peer_redo);
    assert!(own.native() == prefix(&base, "PEER", json!({"bold":false,"italic":true})));
    let own_redo = own.redo();
    peer.apply(&own_redo);
    assert!(
        own.native() == merged && peer.native() == merged,
        "redo retains peer native formatting"
    );
}
#[test]
fn full_native_peer_same_different_and_inherit_format_survive_own_undo() {
    for value in [Some(true), Some(false), None] {
        let mut own = Replica::initial(17);
        let base = own.native();
        own.format(Some(true));
        let mut peer = own.fork(18);
        let b = peer.format(value);
        own.apply(&b);
        let own_undo = own.undo();
        own.saved_restore();
        peer.apply(&own_undo);
        let mut expected = base.clone();
        if let Some(value) = value {
            expected["body"][0]["runs"] = json!([{"len":2,"props":{"bold":value}},{"len":25}]);
        }
        assert!(
            own.native() == expected && peer.native() == expected,
            "full peer-only format equality for {value:?}"
        );
        let peer_undo = peer.undo();
        own.apply(&peer_undo);
        assert!(
            own.native() == base && peer.native() == base,
            "both format undos baseline for {value:?}"
        );
    }
}
#[test]
fn concurrent_formats_in_both_delivery_orders_have_identical_full_native() {
    let mut a = Replica::initial(17);
    let mut b = a.fork(18);
    let da = a.format(Some(true));
    let db = b.format(Some(false));
    a.apply(&db);
    b.apply(&da);
    assert!(a.native() == b.native());
    let mut expected = a.base.clone();
    expected["body"][0]["runs"] = json!([{"len":2,"props":{"bold":false}},{"len":25}]);
    assert!(a.native() == expected);
}
#[test]
fn measure_one_thousand_actual_model_v2_changes() {
    for inserts in [false, true] {
        let mut replica = Replica::initial(9_007_199_254_740_991);
        let initial = replica.update().len();
        let mut total_delta = 0;
        for _ in 0..1000 {
            total_delta += if inserts {
                replica
                    .insert("x", BTreeMap::from([("bold", false), ("italic", true)]))
                    .len()
            } else {
                replica.format(Some(true)).len()
            };
        }
        let update = replica.update();
        let checkpoint = replica.document().checkpoint().unwrap();
        replica.saved_restore();
        if let Ok(directory) = std::env::var("MOORING_MODEL_V2_QUALIFICATION_OUT") {
            let directory = std::path::Path::new(&directory);
            std::fs::create_dir_all(directory).unwrap();
            std::fs::write(
                directory.join(format!("1000-{inserts}-checkpoint.json")),
                &checkpoint,
            )
            .unwrap();
            std::fs::write(
                directory.join(format!("1000-{inserts}-update.bin")),
                &update,
            )
            .unwrap();
        }
        eprintln!("V2_FEASIBILITY inserts={inserts} operations=1000 initial_update_bytes={initial} final_update_bytes={} checkpoint_packed_bytes={} checkpoint_outer_base64_bytes={} total_delta_bytes={total_delta}", update.len(), checkpoint.len(), 4*checkpoint.len().div_ceil(3));
        assert!(
            replica
                .doc
                .transact()
                .state_vector()
                .get(&ClientID::new(replica.origin))
                > 1000
        );
    }
}

#[test]
fn full_native_peer_insert_with_different_or_explicit_empty_properties_survives_undo() {
    for peer_props in [
        BTreeMap::from([("bold", true), ("italic", false)]),
        BTreeMap::new(),
    ] {
        let mut own = Replica::initial(9_007_199_254_740_991);
        let base = own.native();
        own.insert("OWN", BTreeMap::from([("bold", false), ("italic", true)]));
        let mut peer = own.fork(9_007_199_254_740_990);
        let b = peer.insert("PEER", peer_props.clone());
        own.apply(&b);
        let merged = own.native();
        let own_undo = own.undo();
        own.saved_restore();
        peer.apply(&own_undo);
        let props = serde_json::to_value(&peer_props).unwrap();
        assert!(
            own.native() == prefix(&base, "PEER", props.clone())
                && peer.native() == prefix(&base, "PEER", props),
            "full different/empty peer-only native"
        );
        let peer_undo = peer.undo();
        own.apply(&peer_undo);
        assert!(own.native() == base && peer.native() == base);
        let peer_redo = peer.redo();
        own.apply(&peer_redo);
        let own_redo = own.redo();
        peer.apply(&own_redo);
        assert!(own.native() == merged && peer.native() == merged);
    }
}
