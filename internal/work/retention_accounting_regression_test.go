package work

import (
	"strings"
	"testing"
	"time"
)

func TestRetentionFailedBatchDoesNotReportRolledBackDeletion(t *testing.T) {
	s, db, clock := newTestStore(t)
	var ids []string
	for _, id := range []string{"first", "second"} {
		d := retDelivery(id, "endpoint", []byte("retained input"))
		d.FilterDecision = FilterIgnored
		r := acceptDelivery(t, s, db, d, retWork())
		ids = append(ids, r.DeliveryID)
	}
	clock.Advance(8 * 24 * time.Hour)
	if _, err := db.Exec(`CREATE TRIGGER fail_second_expiry BEFORE UPDATE ON webhook_deliveries WHEN old.id = '` + ids[1] + `' BEGIN SELECT RAISE(ABORT,'second expiry refused'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := s.Sweep(t.Context(), RetentionPolicy{Batch: 2})
	if err == nil || !strings.Contains(err.Error(), "second expiry refused") {
		t.Fatalf("failed batch was accepted: %#v %v", result, err)
	}
	for _, id := range ids {
		if got := rawBodyOf(t, db, id); string(got) != "retained input" {
			t.Fatalf("failed batch removed payload %s", id)
		}
	}
	if result.RawBodiesExpired != 0 || result.RawBodyBytesFreed != 0 || result.Batches != 0 {
		t.Fatalf("rolled-back payloads reported as removed: %#v", result)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_second_expiry`); err != nil {
		t.Fatal(err)
	}
	result, err = s.Sweep(t.Context(), RetentionPolicy{Batch: 2})
	if err != nil || result.RawBodiesExpired != 2 || result.RawBodyBytesFreed != int64(2*len("retained input")) {
		t.Fatalf("expiry retry: %#v %v", result, err)
	}
}

func TestRetentionKeepsCommittedBatchAccountingAfterLaterFailure(t *testing.T) {
	s, db, clock := newTestStore(t)
	var ids []string
	for _, source := range []string{"first", "second", "third", "fourth"} {
		d := retDelivery(source, "endpoint", []byte("payload"))
		d.FilterDecision = FilterIgnored
		ids = append(ids, acceptDelivery(t, s, db, d, retWork()).DeliveryID)
	}
	clock.Advance(8 * 24 * time.Hour)
	if _, err := db.Exec(`CREATE TRIGGER fail_later_expiry BEFORE UPDATE ON webhook_deliveries WHEN old.id = '` + ids[3] + `' BEGIN SELECT RAISE(ABORT,'later expiry refused'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := s.Sweep(t.Context(), RetentionPolicy{Batch: 2})
	if err == nil || !strings.Contains(err.Error(), "later expiry refused") {
		t.Fatalf("expected failure: %#v %v", result, err)
	}
	if result.RawBodiesExpired != 2 || result.RawBodyBytesFreed != 14 || result.Batches != 1 {
		t.Fatalf("committed batch accounting lost or rollback counted: %#v", result)
	}
	for i, id := range ids {
		payload := rawBodyOf(t, db, id)
		if i < 2 && payload != nil {
			t.Fatalf("committed expiry missing for %s", id)
		}
		if i >= 2 && string(payload) != "payload" {
			t.Fatalf("failed batch partially removed %s", id)
		}
	}
}
