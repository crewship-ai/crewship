//! TIME-BOUNDED GENERIC RESEARCH ONLY. NOT model-v2, an app, or a wire profile.
//! Original Y.Text characters/IDs are never deleted by deletion/own Undo.
//! Immutable per-author intentions determine projection; separate own active
//! flags support Undo/Redo without restoring cloned character IDs.
//! No untrusted update decoder, authentication, durable Saved, host clock witness,
//! production format migration, paragraph adapter or WordApp integration qualified.
//! Logical author IDs and fresh CRDT origins are SEPARATE synthetic host inputs.
//! No client labels are authenticated by this test-only model.
use std::collections::BTreeSet;
use yrs::{
    updates::decoder::Decode, Any, Assoc, ClientID, Doc, GetString, IndexedSequence, Map, MapRef,
    OffsetKind, Options, Out, ReadTxn, StateVector, Text, TextRef, Transact, Update, ID,
};
const FORMAT: &str = "experimental.visibility-ownership/1";
const MAX_TEXT: usize = 8192;
const MAX_ACTIONS: usize = 1024;
#[derive(Clone, Debug, PartialEq, Eq)]
struct Glyph {
    id: ID,
    text: char,
    bold: Option<bool>,
    italic: Option<bool>,
}
#[derive(Clone)]
struct Snapshot {
    format: &'static str,
    update: Vec<u8>,
}
#[derive(Clone, Debug, PartialEq, Eq)]
struct Intent {
    owner: u64,
    seq: u32,
    stamp: u64,
    kind: char,
    value: char,
    ids: Vec<ID>,
}
impl Intent {
    fn key(&self) -> String {
        format!("{}:{}", self.owner, self.seq)
    }
    fn encode(&self) -> String {
        format!(
            "{}|{}|{}|{}",
            self.kind,
            self.value,
            self.stamp,
            self.ids
                .iter()
                .map(|id| format!("{}:{}", id.client.get(), id.clock))
                .collect::<Vec<_>>()
                .join(",")
        )
    }
    fn decode(key: &str, value: &str) -> Self {
        let (owner, seq) = key.split_once(':').unwrap();
        let fields: Vec<_> = value.split('|').collect();
        assert_eq!(fields.len(), 4);
        let kind = fields[0].chars().next().unwrap();
        let value = fields[1].chars().next().unwrap();
        assert!(matches!(kind, 'I' | 'D' | 'B' | 'T'));
        assert!(matches!(value, '1' | '0' | 'i'));
        let ids = fields[3]
            .split(',')
            .map(|pair| {
                let (client, clock) = pair.split_once(':').unwrap();
                let n: u64 = client.parse().unwrap();
                assert!(n < (1 << 53));
                ID::new(ClientID::new(n), clock.parse().unwrap())
            })
            .collect::<Vec<_>>();
        assert!(ids.len() <= MAX_TEXT);
        let owner: u64 = owner.parse().unwrap();
        assert!(owner > 0 && owner < (1 << 53));
        Self {
            owner,
            seq: seq.parse().unwrap(),
            stamp: fields[2].parse().unwrap(),
            kind,
            value,
            ids,
        }
    }
}
struct Replica {
    doc: Doc,
    text: TextRef,
    intents: MapRef,
    active: MapRef,
    origin: u64,
    author: u64,
    next: u32,
}
impl Replica {
    fn initial(text: &str) -> Snapshot {
        let d = Doc::with_options(Options {
            client_id: ClientID::new(7),
            offset_kind: OffsetKind::Utf16,
            skip_gc: true,
            ..Options::default()
        });
        d.get_or_insert_text("experimental.text")
            .insert(&mut d.transact_mut(), 0, text);
        let update = d
            .transact()
            .encode_state_as_update_v1(&StateVector::default());
        Snapshot {
            format: FORMAT,
            update,
        }
    }
    fn restore(snapshot: &Snapshot, origin: u64) -> Self {
        Self::restore_as(snapshot, origin, origin)
    }
    fn restore_as(snapshot: &Snapshot, origin: u64, author: u64) -> Self {
        assert_eq!(snapshot.format, FORMAT);
        assert!(snapshot.update.len() <= 2 * 1024 * 1024);
        assert!(origin > 0 && origin < (1 << 53));
        let doc = Doc::with_options(Options {
            client_id: ClientID::new(origin),
            offset_kind: OffsetKind::Utf16,
            skip_gc: true,
            ..Options::default()
        });
        doc.transact_mut()
            .apply_update(Update::decode_v1(&snapshot.update).unwrap())
            .unwrap();
        assert!(!doc.transact().has_missing_updates());
        assert_eq!(
            doc.transact().state_vector().get(&ClientID::new(origin)),
            0,
            "fresh CRDT origin required on cold restore"
        );
        let text = doc.get_or_insert_text("experimental.text");
        let intents = doc.get_or_insert_map("experimental.intent");
        let active = doc.get_or_insert_map("experimental.active");
        let next = intents
            .iter(&doc.transact())
            .filter_map(|(k, _)| k.split_once(':'))
            .filter(|(o, _)| o.parse::<u64>() == Ok(author))
            .map(|(_, s)| s.parse::<u32>().unwrap())
            .max()
            .unwrap_or(0)
            + 1;
        let r = Self {
            doc,
            text,
            intents,
            active,
            origin,
            author,
            next,
        };
        r.view();
        r
    }
    fn snapshot(&self) -> Snapshot {
        Snapshot {
            format: FORMAT,
            update: self
                .doc
                .transact()
                .encode_state_as_update_v1(&StateVector::default()),
        }
    }
    fn apply(&self, update: &[u8]) {
        assert!(update.len() <= 262144);
        self.doc
            .transact_mut()
            .apply_update(Update::decode_v1(update).unwrap())
            .unwrap();
        assert!(!self.doc.transact().has_missing_updates());
        self.view();
    }
    fn all(&self) -> Vec<(ID, char, u32)> {
        let txn = self.doc.transact();
        let string = self.text.get_string(&txn);
        assert!(string.encode_utf16().count() <= MAX_TEXT);
        let mut at = 0;
        string
            .chars()
            .map(|ch| {
                let id = *self
                    .text
                    .sticky_index(&txn, at, Assoc::After)
                    .unwrap()
                    .id()
                    .unwrap();
                let entry = (id, ch, at);
                at += ch.len_utf16() as u32;
                entry
            })
            .collect()
    }
    fn actions(&self) -> Vec<(Intent, bool)> {
        let txn = self.doc.transact();
        assert!(self.intents.len(&txn) as usize <= MAX_ACTIONS);
        self.intents
            .iter(&txn)
            .map(|(key, value)| {
                let Out::Any(Any::String(value)) = value else {
                    panic!("closed scalar intent")
                };
                let active = match self.active.get(&txn, key) {
                    None => true,
                    Some(Out::Any(Any::Bool(value))) => value,
                    _ => panic!("closed active flag"),
                };
                (Intent::decode(key, &value), active)
            })
            .collect()
    }
    fn view(&self) -> Vec<Glyph> {
        let actions = self.actions();
        let all = self.all();
        let available: BTreeSet<_> = all
            .iter()
            .map(|(id, _, _)| (id.client.get(), id.clock))
            .collect();
        for (a, _) in &actions {
            assert!(
                a.ids
                    .iter()
                    .all(|id| available.contains(&(id.client.get(), id.clock))),
                "no unknown target IDs"
            );
        }
        all.into_iter()
            .filter_map(|(id, text, _)| {
                let includes = |a: &Intent| a.ids.contains(&id);
                let hidden = actions.iter().any(|(a, active)| {
                    includes(a) && ((a.kind == 'D' && *active) || (a.kind == 'I' && !*active))
                });
                if hidden {
                    return None;
                }
                let property = |kind| {
                    actions
                        .iter()
                        .filter(|(a, active)| *active && a.kind == kind && includes(a))
                        .max_by_key(|(a, _)| (a.stamp, a.owner, a.seq))
                        .and_then(|(a, _)| match a.value {
                            '1' => Some(true),
                            '0' => Some(false),
                            _ => None,
                        })
                };
                Some(Glyph {
                    id,
                    text,
                    bold: property('B'),
                    italic: property('T'),
                })
            })
            .collect()
    }
    fn string(&self) -> String {
        self.view().iter().map(|g| g.text).collect()
    }
    fn selected(&self, start: u32, end: u32) -> Vec<ID> {
        assert!(start <= end);
        let mut at = 0;
        let mut ids = Vec::new();
        let mut start_ok = start == 0;
        let mut end_ok = end == 0;
        for g in self.view() {
            let next = at + g.text.len_utf16() as u32;
            if at >= start && next <= end {
                ids.push(g.id);
            }
            at = next;
            start_ok |= at == start;
            end_ok |= at == end;
        }
        assert!(
            start_ok && end_ok,
            "UTF16 selection must end at scalar boundaries"
        );
        ids
    }
    fn physical_offset(&self, visible: u32) -> u32 {
        let view = self.view();
        let mut at = 0;
        for glyph in view {
            if at == visible {
                return self
                    .all()
                    .into_iter()
                    .find(|(id, _, _)| *id == glyph.id)
                    .unwrap()
                    .2;
            }
            at += glyph.text.len_utf16() as u32;
        }
        assert_eq!(
            at, visible,
            "offset must be scalar boundary inside visible text"
        );
        self.text.len(&self.doc.transact())
    }
    fn action(&mut self, kind: char, value: char, ids: Vec<ID>) -> String {
        assert!(!ids.is_empty());
        assert!((self.intents.len(&self.doc.transact()) as usize) < MAX_ACTIONS);
        // Experimental deterministic causal rank only; NOT a qualified host witness.
        let stamp = self
            .doc
            .transact()
            .state_vector()
            .iter()
            .map(|(_, clock)| *clock as u64)
            .sum::<u64>()
            + 1;
        let intent = Intent {
            owner: self.author,
            seq: self.next,
            stamp,
            kind,
            value,
            ids,
        };
        self.next += 1;
        let key = intent.key();
        self.intents
            .insert(&mut self.doc.transact_mut(), key.clone(), intent.encode());
        key
    }
    fn delta(&self, before: &StateVector) -> Vec<u8> {
        self.doc.transact().encode_state_as_update_v1(before)
    }
    fn delete(&mut self, start: u32, end: u32) -> (String, Vec<u8>) {
        let before = self.doc.transact().state_vector();
        let ids = self.selected(start, end);
        let key = self.action('D', '1', ids);
        (key, self.delta(&before))
    }
    fn format(
        &mut self,
        start: u32,
        end: u32,
        bold: bool,
        value: Option<bool>,
    ) -> (String, Vec<u8>) {
        let before = self.doc.transact().state_vector();
        let ids = self.selected(start, end);
        let key = self.action(
            if bold { 'B' } else { 'T' },
            match value {
                Some(true) => '1',
                Some(false) => '0',
                None => 'i',
            },
            ids,
        );
        (key, self.delta(&before))
    }
    fn insert(&mut self, offset: u32, text: &str) -> (String, Vec<u8>) {
        let before = self.doc.transact().state_vector();
        let at = self.physical_offset(offset);
        let start = self
            .doc
            .transact()
            .state_vector()
            .get(&ClientID::new(self.origin));
        self.text.insert(&mut self.doc.transact_mut(), at, text);
        let mut clock = start;
        let ids = text
            .chars()
            .map(|ch| {
                let id = ID::new(ClientID::new(self.origin), clock);
                clock += ch.len_utf16() as u32;
                id
            })
            .collect();
        let key = self.action('I', '1', ids);
        (key, self.delta(&before))
    }
    fn toggle(&self, key: &str, value: bool) -> Vec<u8> {
        assert_eq!(
            key.split_once(':').unwrap().0.parse::<u64>().unwrap(),
            self.author,
            "cannot undo another author"
        );
        assert!(self.intents.get(&self.doc.transact(), key).is_some());
        let before = self.doc.transact().state_vector();
        self.active.insert(&mut self.doc.transact_mut(), key, value);
        self.delta(&before)
    }
}
#[test]
fn original_ids_survive_overlapping_delete_undo_redo_and_cold_restore() {
    for partial in [false, true] {
        for a_first in [false, true] {
            let initial = Replica::initial("aa😀b");
            let mut a = Replica::restore(&initial, 17);
            let mut b = Replica::restore(&initial, 18);
            let original = a.view();
            let (ak, ad) = a.delete(0, if partial { 2 } else { 1 });
            let a_only = a.view();
            let (bk, bd) = b.delete(if partial { 1 } else { 0 }, if partial { 2 } else { 1 });
            let b_only = b.view();
            a.apply(&bd);
            b.apply(&ad);
            assert_eq!(a.view(), b.view());
            let (first, second, fk, sk) = if a_first {
                (&a, &b, &ak, &bk)
            } else {
                (&b, &a, &bk, &ak)
            };
            let intent_before = first.intents.get(&first.doc.transact(), fk);
            let undo = first.toggle(fk, false);
            assert!(!undo.is_empty());
            second.apply(&undo);
            assert_eq!(first.view(), second.view());
            assert_eq!(
                first.view(),
                if a_first { b_only } else { a_only },
                "first Undo must equal peer-only projection"
            );
            let undo = second.toggle(sk, false);
            first.apply(&undo);
            assert_eq!(first.view(), original);
            assert_eq!(second.view(), original);
            assert_eq!(
                first.intents.get(&first.doc.transact(), fk),
                intent_before,
                "original intent must not change"
            );
            let redo = first.toggle(fk, true);
            second.apply(&redo);
            assert_eq!(first.view(), second.view());
            assert_ne!(first.view(), original);
            let redo = second.toggle(sk, true);
            first.apply(&redo);
            assert_eq!(first.view(), second.view());
            let ac = Replica::restore_as(&a.snapshot(), 117, 17);
            let bc = Replica::restore_as(&b.snapshot(), 118, 18);
            assert_eq!(ac.view(), bc.view());
            let undo = ac.toggle(&ak, false);
            bc.apply(&undo);
            let undo = bc.toggle(&bk, false);
            ac.apply(&undo);
            assert_eq!(ac.view(), original);
            assert_eq!(bc.view(), original);
        }
    }
}
#[test]
fn peer_format_survives_insertion_undo_redo_and_unknown_author_cannot_undo() {
    let snapshot = Replica::initial("a😀b");
    let mut a = Replica::restore(&snapshot, 17);
    let mut b = Replica::restore(&snapshot, 18);
    let (key, insert) = a.insert(1, "X");
    b.apply(&insert);
    let (_, format) = b.format(1, 2, true, Some(true));
    a.apply(&format);
    let formatted = a.view();
    assert_eq!(formatted, b.view());
    let undo = a.toggle(&key, false);
    b.apply(&undo);
    assert_eq!(a.string(), "a😀b");
    assert_eq!(a.view(), b.view());
    let redo = a.toggle(&key, true);
    b.apply(&redo);
    assert_eq!(a.view(), formatted);
    assert_eq!(b.view(), formatted);
    let denial = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| b.toggle(&key, false)));
    assert!(denial.is_err());
    assert_eq!(a.view(), b.view());
}
#[test]
fn hidden_characters_map_visible_utf16_positions_without_splitting_emoji() {
    let snapshot = Replica::initial("a😀bc");
    let mut a = Replica::restore(&snapshot, 17);
    let mut b = Replica::restore(&snapshot, 18);
    let (_, deletion) = a.delete(0, 1);
    b.apply(&deletion);
    assert_eq!(a.string(), "😀bc");
    assert_eq!(a.physical_offset(0), 1);
    assert_eq!(a.physical_offset(2), 3);
    let malformed = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| a.selected(0, 1)));
    assert!(malformed.is_err());
    let (_, insertion) = b.insert(2, "e\u{301}");
    a.apply(&insertion);
    assert_eq!(a.string(), "😀e\u{301}bc");
    assert_eq!(a.view(), b.view());
    let cold = Replica::restore(&a.snapshot(), 19);
    assert_eq!(cold.view(), a.view());
}

#[test]
fn independent_original_updates_converge_in_all_three_actor_delivery_orders() {
    let snapshot = Replica::initial("aa😀");
    let mut a = Replica::restore(&snapshot, 17);
    let mut b = Replica::restore(&snapshot, 18);
    let mut agent = Replica::restore(&snapshot, 19);
    let (ak, ad) = a.insert(1, "X");
    let (bk, bd) = b.insert(1, "Y");
    let (gk, gd) = agent.format(0, 2, true, Some(true));
    let updates = [ad, bd, gd];
    let mut expected = None;
    for order in [
        [0, 1, 2],
        [0, 2, 1],
        [1, 0, 2],
        [1, 2, 0],
        [2, 0, 1],
        [2, 1, 0],
    ] {
        let replica = Replica::restore(&snapshot, 20);
        for index in order {
            replica.apply(&updates[index]);
        }
        if let Some(ref expected) = expected {
            assert_eq!(&replica.view(), expected);
        } else {
            expected = Some(replica.view());
        }
        let cold = Replica::restore(&replica.snapshot(), 21);
        assert_eq!(cold.view(), replica.view());
        // This is projection/CRDT convergence, NOT an agent authority/read qualification.
        assert_eq!(replica.intents.len(&replica.doc.transact()), 3);
        let author_a = Replica::restore_as(&replica.snapshot(), 117, 17);
        replica.apply(&author_a.toggle(&ak, false));
        let author_b = Replica::restore_as(&replica.snapshot(), 118, 18);
        replica.apply(&author_b.toggle(&bk, false));
        let author_agent = Replica::restore_as(&replica.snapshot(), 119, 19);
        replica.apply(&author_agent.toggle(&gk, false));
        assert_eq!(replica.string(), "aa😀");
    }
}
#[test]
fn same_value_peer_format_undo_and_clear_inheritance_are_action_scoped() {
    for a_first in [false, true] {
        let snapshot = Replica::initial("aa");
        let mut a = Replica::restore(&snapshot, 17);
        let mut b = Replica::restore(&snapshot, 18);
        let (ak, ad) = a.format(0, 2, true, Some(true));
        let (bk, bd) = b.format(0, 2, true, Some(true));
        a.apply(&bd);
        b.apply(&ad);
        let (first, second, fk, sk) = if a_first {
            (&a, &b, &ak, &bk)
        } else {
            (&b, &a, &bk, &ak)
        };
        second.apply(&first.toggle(fk, false));
        assert!(first.view().iter().all(|g| g.bold == Some(true)));
        assert_eq!(first.view(), second.view());
        first.apply(&second.toggle(sk, false));
        assert!(first.view().iter().all(|g| g.bold.is_none()));
        assert_eq!(first.view(), second.view());
        second.apply(&first.toggle(fk, true));
        assert!(second.view().iter().all(|g| g.bold == Some(true)));
        let (clear, update) = a.format(0, 2, true, None);
        b.apply(&update);
        assert!(a.view().iter().all(|g| g.bold.is_none()));
        b.apply(&a.toggle(&clear, false));
        assert!(a.view().iter().all(|g| g.bold == Some(true)));
        assert_eq!(a.view(), b.view());
    }
}
#[test]
fn one_thousand_retained_equal_format_intents_have_measured_checkpoint_growth() {
    let initial = Replica::initial("a");
    let mut replica = Replica::restore(&initial, 17);
    let mut largest = 0;
    for _ in 0..1000 {
        let (_, update) = replica.format(0, 1, true, Some(true));
        largest = largest.max(update.len());
    }
    let snapshot = replica.snapshot();
    assert_eq!(replica.intents.len(&replica.doc.transact()), 1000);
    assert!(snapshot.update.len() < 2 * 1024 * 1024);
    assert!(largest <= 262144);
    let cold = Replica::restore(&snapshot, 18);
    assert_eq!(cold.view(), replica.view());
    println!("experimental_visibility_metrics initial_raw={} final_raw={} largest_original_update={} intents=1000",initial.update.len(),snapshot.update.len(),largest);
}

#[test]
fn different_properties_survive_peer_undo_with_fresh_origin_history_restore() {
    let snapshot = Replica::initial("aa");
    let mut a = Replica::restore(&snapshot, 17);
    let mut b = Replica::restore(&snapshot, 18);
    let (ak, ad) = a.format(0, 2, true, Some(true));
    let (bk, bd) = b.format(0, 2, false, Some(false));
    a.apply(&bd);
    b.apply(&ad);
    assert!(a
        .view()
        .iter()
        .all(|g| g.bold == Some(true) && g.italic == Some(false)));
    let a = Replica::restore_as(&a.snapshot(), 117, 17);
    let b = Replica::restore_as(&b.snapshot(), 118, 18);
    b.apply(&a.toggle(&ak, false));
    assert!(a
        .view()
        .iter()
        .all(|g| g.bold.is_none() && g.italic == Some(false)));
    assert_eq!(a.view(), b.view());
    a.apply(&b.toggle(&bk, false));
    assert!(a
        .view()
        .iter()
        .all(|g| g.bold.is_none() && g.italic.is_none()));
    assert_eq!(a.view(), b.view());
    b.apply(&a.toggle(&ak, true));
    a.apply(&b.toggle(&bk, true));
    assert_eq!(a.view(), b.view());
    assert!(a
        .view()
        .iter()
        .all(|g| g.bold == Some(true) && g.italic == Some(false)));
}
