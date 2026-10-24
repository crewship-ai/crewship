use mooring_core::session::{Batch, Error, Limits, Session};
use std::num::NonZeroUsize;
fn n(v: usize) -> NonZeroUsize {
    NonZeroUsize::new(v).unwrap()
}
fn session() -> Session<String, &'static str> {
    Session::new(
        "confirmed".into(),
        7,
        Limits {
            pending_operations: n(6),
            pending_bytes: n(60),
            in_flight_batches: n(2),
        },
    )
}
fn batch(body: &'static str, operations: usize, bytes: usize) -> Batch<&'static str> {
    Batch {
        body,
        operations: n(operations),
        bytes: n(bytes),
    }
}
#[test]
fn b_sends_before_a_ack_without_installing_speculative_state() {
    let mut s = session();
    let a = s.enqueue(batch("a", 2, 20)).unwrap();
    let b = s.enqueue(batch("b", 2, 20)).unwrap();
    assert_eq!(s.start_send().unwrap().unwrap().sequence, a);
    assert_eq!(s.start_send().unwrap().unwrap().sequence, b);
    assert!(s.start_send().unwrap().is_none());
    assert_eq!(s.confirmed(), (&"confirmed".to_string(), 7));
    assert_eq!(s.pending_usage(), (4, 40));
    assert_eq!(
        s.settle_host_response(a, Some(("host-a".into(), 9)))
            .unwrap()
            .body,
        "a"
    );
    assert_eq!(s.confirmed(), (&"host-a".to_string(), 9));
    s.settle_host_response(b, None).unwrap();
    assert_eq!(s.confirmed(), (&"host-a".to_string(), 9));
    assert_eq!(s.pending_usage(), (0, 0));
}
#[test]
fn capacity_covers_sent_and_unsent_and_returns_refused_ownership() {
    for bad in [
        batch("bad", 1, 31),
        batch("bad", 4, 1),
        batch("bad", usize::MAX, 1),
        batch("bad", 1, usize::MAX),
    ] {
        let mut s = session();
        s.enqueue(batch("sent", 2, 20)).unwrap();
        s.start_send().unwrap();
        s.enqueue(batch("unsent", 1, 10)).unwrap();
        let refused = s.enqueue(bad).unwrap_err();
        assert_eq!(refused.reason, Error::Capacity);
        assert_eq!(refused.batch.body, "bad");
        assert_eq!(s.pending_usage(), (3, 30));
        assert_eq!(
            s.into_recovery()
                .2
                .iter()
                .map(|b| b.body)
                .collect::<Vec<_>>(),
            ["sent", "unsent"]
        );
    }
}
#[test]
fn inflight_slot_reopens_only_after_ordered_verified_settlement() {
    let mut s = session();
    let a = s.enqueue(batch("a", 1, 1)).unwrap();
    s.enqueue(batch("b", 1, 1)).unwrap();
    let c = s.enqueue(batch("c", 1, 1)).unwrap();
    s.start_send().unwrap();
    s.start_send().unwrap();
    assert!(s.start_send().unwrap().is_none());
    s.settle_host_response(a, None).unwrap();
    assert_eq!(s.start_send().unwrap().unwrap().sequence, c);
}
#[test]
fn invalid_or_out_of_order_host_results_close_and_preserve_every_intent() {
    for variant in 0..5 {
        let mut s = session();
        let a = s.enqueue(batch("a", 1, 1)).unwrap();
        let b = s.enqueue(batch("b", 1, 1)).unwrap();
        if variant != 0 {
            s.start_send().unwrap();
        }
        let (id, doc, error) = match variant {
            0 => (a, None, Error::UnexpectedConfirmation),
            1 => (
                b,
                Some(("wrong-order".into(), 9)),
                Error::UnexpectedConfirmation,
            ),
            2 => (a, Some(("old".into(), 6)), Error::RevisionNotAdvanced),
            3 => (
                a,
                Some(("same-head-altered".into(), 7)),
                Error::RevisionNotAdvanced,
            ),
            _ => (999, None, Error::UnexpectedConfirmation),
        };
        assert_eq!(s.settle_host_response(id, doc).unwrap_err(), error);
        assert!(s.is_closed());
        assert_eq!(s.start_send().unwrap_err(), Error::Closed);
        assert_eq!(
            s.settle_host_response(a, Some(("late".into(), 9)))
                .unwrap_err(),
            Error::Closed
        );
        let (doc, rev, work) = s.into_recovery();
        assert_eq!((doc.as_str(), rev), ("confirmed", 7));
        assert_eq!(work.iter().map(|b| b.body).collect::<Vec<_>>(), ["a", "b"]);
    }
}
#[test]
fn death_after_send_keeps_original_bodies_and_last_confirmed_document() {
    let mut s = session();
    s.enqueue(batch("sent", 1, 1)).unwrap();
    s.start_send().unwrap();
    s.enqueue(batch("unsent", 1, 1)).unwrap();
    s.close();
    assert_eq!(
        s.enqueue(batch("new", 1, 1)).unwrap_err().reason,
        Error::Closed
    );
    let (doc, revision, work) = s.into_recovery();
    assert_eq!((doc.as_str(), revision), ("confirmed", 7));
    assert_eq!(
        work.iter().map(|b| b.body).collect::<Vec<_>>(),
        ["sent", "unsent"]
    );
}
