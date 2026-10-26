package api

import (
	"bytes"
	"errors"
	"testing"
)

// Default CI authorization regression with real SQL and an explicit synthetic
// engine port. Actual native/agent execution remains separately qualified.
func TestCommandAgentUnconfirmedMandateCannotReadWriteOrReconcile(t *testing.T) {
	r := newCommandSQLRig(t, true)
	before := append([]byte(nil), r.p.p...)
	if a, err := r.s.bindAgent(collaborationAgentConfirmed{}, "agent:unconfirmed"); !errors.Is(err, errCollaborationAgentAccess) || a != nil {
		t.Fatal("unconfirmed agent binding accepted", err)
	}
	for _, a := range []*collaborationCommandAgent{nil, {store: r.s, actor: "agent:unconfirmed"}} {
		req := r.request()
		req.Header.Set("Authorization", "Bearer unconfirmed-fixture")
		if _, err := a.Submit(req, []byte(`{"participant":"human:forged"}`)); !errors.Is(err, errCollaborationAgentAccess) {
			t.Fatal("unconfirmed submit", err)
		}
		if _, err := a.ReadCheckpoint(req); !errors.Is(err, errCollaborationAgentAccess) {
			t.Fatal("unconfirmed read", err)
		}
		if _, err := a.Reconcile(req, "unconfirmed-op", "unconfirmed-fingerprint"); !errors.Is(err, errCollaborationAgentAccess) {
			t.Fatal("unconfirmed outcome", err)
		}
		if _, err := a.ReconcileOriginal(req, []byte(`{"op_id":"unconfirmed-op"}`)); !errors.Is(err, errCollaborationAgentAccess) {
			t.Fatal("unconfirmed original", err)
		}
	}
	var operations int
	if err := r.f.r.db.QueryRow(`SELECT count(*) FROM mooring_collab_operations WHERE page_id='admission-page'`).Scan(&operations); err != nil || operations != 0 {
		t.Fatal("unconfirmed agent wrote SQL", operations, err)
	}
	if r.p.stages != 0 || r.p.acks != 0 || r.p.rejects != 0 || !bytes.Equal(before, r.p.p) {
		t.Fatal("unconfirmed agent reached or changed engine state")
	}
}
