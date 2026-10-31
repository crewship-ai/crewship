package mooring

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"
)

func nativeReceiptHash(b []byte) string { return "sha256:" + fmt.Sprintf("%x", sha256.Sum256(b)) }
func TestNativeSaveSnapshotReconcilesOnlyItsExactSuccessor(t *testing.T) {
	s, _, _, _, scope, p := nativeStoreFixture(t)
	l, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"v":2}`)
	hash := nativeReceiptHash(raw)
	receipt, err := s.SaveSnapshot(t.Context(), l, 1, "10:47:3", hash, raw)
	if err != nil || receipt.Version != "2" || receipt.BaseVersion != "1" || receipt.SessionID != p.SessionID || receipt.Generation != "1" || receipt.EditorRevision != "10:47:3" || receipt.CandidateHash != hash || receipt.Durability != "node" || receipt.IssuedBy != "crewship" || receipt.Phase != "committed" {
		t.Fatalf("unbound receipt: %+v %v", receipt, err)
	}
	if _, err = s.SaveSnapshot(t.Context(), l, 2, "10:48:3", hash, raw); err != nil {
		t.Fatal(err)
	}
	// Lost ACK reconciliation is pinned to base+1, never to a later matching head.
	reconciled, err := s.ReconcileSnapshot(t.Context(), l, 1, "10:47:3", hash)
	if err != nil || reconciled != receipt {
		t.Fatalf("wrong lost-ACK proof: %+v %v", reconciled, err)
	}
	for _, x := range []struct {
		base      int64
		rev, hash string
	}{{1, "10:48:3", hash}, {2, "10:47:3", hash}, {1, "10:47:3", nativeReceiptHash([]byte("other"))}} {
		if _, err = s.ReconcileSnapshot(t.Context(), l, x.base, x.rev, x.hash); !errors.Is(err, ErrNativeUnconfirmed) {
			t.Fatalf("foreign snapshot confirmed: %+v %v", x, err)
		}
	}
}
func TestNativeSaveSnapshotCannotClaimGenericSaveOrRevokedGeneration(t *testing.T) {
	s, leases, _, clock, scope, p := nativeStoreFixture(t)
	l, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"v":2}`)
	hash := nativeReceiptHash(raw)
	if _, err = s.Save(t.Context(), l, 1, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReconcileSnapshot(t.Context(), l, 1, "r1", hash); !errors.Is(err, ErrNativeUnconfirmed) {
		t.Fatal("unbound version became a save ACK", err)
	}
	if _, err = s.SaveSnapshot(t.Context(), l, 2, "r2", nativeReceiptHash([]byte("forged")), raw); err == nil {
		t.Fatal("wrong candidate hash admitted")
	}
	if _, err = s.SaveSnapshot(t.Context(), l, 2, "r2", hash, raw); err != nil {
		t.Fatal(err)
	}
	clock.advance(5 * time.Second)
	if _, err = leases.Acquire(t.Context(), scope, EditLeasePrincipal{ActorID: p.ActorID, SessionID: "new-holder"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveSnapshot(t.Context(), l, 3, "r3", nativeReceiptHash([]byte(`{"v":3}`)), []byte(`{"v":3}`)); !errors.Is(err, ErrEditLeaseStale) {
		t.Fatal("revoked generation saved", err)
	}
	// The revoked generation may still prove its own committed successor, but
	// another admission of the same person cannot claim it.
	if r, err := s.ReconcileSnapshot(t.Context(), l, 2, "r2", hash); err != nil || r.Version != "3" || r.Generation != "1" {
		t.Fatal("own committed snapshot unprovable after takeover", r, err)
	}
	other := l
	other.Holder.SessionID = "new-holder"
	if _, err = s.ReconcileSnapshot(t.Context(), other, 2, "r2", hash); !errors.Is(err, ErrEditLeaseDenied) {
		t.Fatal("another admission confirmed a foreign snapshot", err)
	}
}

// A lost reply during an outage longer than the lease TTL must still be
// provable once the connection returns; the expired lease cannot save again.
func TestNativeSaveSnapshotReconcilesAfterLeaseExpired(t *testing.T) {
	s, _, _, clock, scope, p := nativeStoreFixture(t)
	l, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"v":2}`)
	hash := nativeReceiptHash(raw)
	receipt, err := s.SaveSnapshot(t.Context(), l, 1, "r1", hash, raw)
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute)
	reconciled, err := s.ReconcileSnapshot(t.Context(), l, 1, "r1", hash)
	if err != nil || reconciled != receipt {
		t.Fatalf("lost reply unprovable after lease expiry: %+v %v", reconciled, err)
	}
	if _, err = s.ReconcileSnapshot(t.Context(), l, 1, "r1", nativeReceiptHash([]byte("other"))); !errors.Is(err, ErrNativeUnconfirmed) {
		t.Fatal("expired lease confirmed a foreign snapshot", err)
	}
	if _, err = s.SaveSnapshot(t.Context(), l, 2, "r2", nativeReceiptHash([]byte(`{"v":3}`)), []byte(`{"v":3}`)); err == nil {
		t.Fatal("expired lease saved")
	}
}
