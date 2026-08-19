package api

// A standing trust grant is the act of removing a human from an approval
// loop. Until this file existed it was the only decision in the system that
// left no journal entry — `broadcastInboxUpdated` and nothing else, while
// every comparable decision goes through harbormaster.AfterDecide
// (internal/harbormaster/store_mutate.go) and emits actor, scope and refs.
//
// These tests assert the CONTENT of the entry, never a count. A count would
// pass against any emit at all, including one that named the wrong actor or
// the wrong gate — and "who disarmed which gate, on which body of the
// routine" is the entire question an audit asks here.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// decodeTrustGrantID pulls the new grant's id out of a 201 GrantTrust body.
func decodeTrustGrantID(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode grant response: %v (body=%s)", err, rr.Body.String())
	}
	if body.ID == "" {
		t.Fatalf("grant response carried no id: %s", rr.Body.String())
	}
	return body.ID
}

// trustEntry finds the single entry of the given type, failing the test when
// there is not exactly one. Returning the entry (rather than a bool) is what
// lets each case assert on fields instead of on arithmetic.
func trustEntry(t *testing.T, rec *recordingEmitter, want journal.EntryType) journal.Entry {
	t.Helper()
	var found []journal.Entry
	for _, e := range rec.entries {
		if e.Type == want {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		var got []string
		for _, e := range rec.entries {
			got = append(got, string(e.Type))
		}
		t.Fatalf("want exactly one %s entry, got %d; journal saw: %v", want, len(found), got)
	}
	return found[0]
}

func refString(t *testing.T, e journal.Entry, key string) string {
	t.Helper()
	v, ok := e.Refs[key]
	if !ok {
		t.Fatalf("entry %s has no ref %q; refs=%v", e.Type, key, e.Refs)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("ref %q on %s is %T, want string", key, e.Type, v)
	}
	return s
}

func entryPayloadString(t *testing.T, e journal.Entry, key string) string {
	t.Helper()
	v, ok := e.Payload[key]
	if !ok {
		t.Fatalf("entry %s has no payload key %q; payload=%v", e.Type, key, e.Payload)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("payload %q on %s is %T, want string", key, e.Type, v)
	}
	return s
}

// TestTrustGrantJournalsTheDecision — the grant path.
//
// Deleting the emit in GrantTrust makes trustEntry fail with "want exactly
// one approval.trust_granted entry, got 0". Weakening it (dropping the actor,
// pointing the refs at the wrong routine, forgetting the definition hash)
// fails one of the field assertions below.
func TestTrustGrantJournalsTheDecision(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	hash := seedTrustableRoutine(t, h, wsID, "tj-grant")
	rec := &recordingEmitter{}
	h.SetJournal(rec)

	rr := httptest.NewRecorder()
	h.GrantTrust(rr, grantTrustReq(t, userID, wsID, "tj-grant", "MANAGER",
		`{"step_id":"publish","reason":"approved 12x, identical every time","prior_approvals":12}`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201; body=%s", rr.Code, rr.Body.String())
	}

	e := trustEntry(t, rec, journal.EntryTrustGranted)

	// Scope. A journal entry that cannot be filtered to the workspace that
	// produced it is not an audit trail.
	if e.WorkspaceID != wsID {
		t.Errorf("workspace_id=%q want %q", e.WorkspaceID, wsID)
	}
	// Actor. AfterDecide's shape: the human who decided, typed as a user.
	if e.ActorType != journal.ActorUser {
		t.Errorf("actor_type=%q want %q — a standing grant is a human act", e.ActorType, journal.ActorUser)
	}
	if e.ActorID != userID {
		t.Errorf("actor_id=%q want the granting user %q", e.ActorID, userID)
	}
	if e.Severity != journal.SeverityNotice {
		t.Errorf("severity=%q want notice (AfterDecide's level for a recorded decision)", e.Severity)
	}

	// Refs — what the entry is ABOUT. The grant id makes the entry joinable
	// back to waitpoint_trust_grants; the slug and step name the gate that
	// stopped asking.
	if got := refString(t, e, "pipeline_slug"); got != "tj-grant" {
		t.Errorf("refs.pipeline_slug=%q want tj-grant", got)
	}
	if got := refString(t, e, "step_id"); got != "publish" {
		t.Errorf("refs.step_id=%q want publish — trust is granted per gate, so the gate must be in the refs", got)
	}
	if refString(t, e, "trust_grant_id") == "" {
		t.Error("refs.trust_grant_id is empty — the entry cannot be joined to the row it describes")
	}

	// Payload. The definition hash is the safety property of the whole
	// feature: the grant only fires against this exact routine body. An
	// audit that does not record it cannot answer "what was trusted".
	if got := entryPayloadString(t, e, "definition_hash"); got != hash {
		t.Errorf("payload.definition_hash=%q want the routine's %q", got, hash)
	}
	if got := entryPayloadString(t, e, "reason"); got != "approved 12x, identical every time" {
		t.Errorf("payload.reason=%q — the operator's stated reason must survive into the trail", got)
	}
	if !strings.Contains(e.Summary, "publish") {
		t.Errorf("summary=%q should name the gate", e.Summary)
	}
}

// TestTrustRevokeJournalsTheDecision — the revoke path. Taking trust back is
// as much a decision as granting it: it is the operator saying the gate must
// start asking again, and a trail that records only grants makes a revoked
// gate look permanently disarmed.
func TestTrustRevokeJournalsTheDecision(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	_ = seedTrustableRoutine(t, h, wsID, "tj-revoke")

	grantRR := httptest.NewRecorder()
	h.GrantTrust(grantRR, grantTrustReq(t, userID, wsID, "tj-revoke", "MANAGER", `{"step_id":"publish"}`))
	if grantRR.Code != http.StatusCreated {
		t.Fatalf("grant status=%d want 201; body=%s", grantRR.Code, grantRR.Body.String())
	}
	grantID := decodeTrustGrantID(t, grantRR)

	// Wire the recorder only now, so the assertion below cannot be satisfied
	// by the grant's own entry.
	rec := &recordingEmitter{}
	h.SetJournal(rec)

	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/x?reason=routine+is+being+re-reviewed", nil),
		userID, wsID, "MANAGER")
	req.SetPathValue("slug", "tj-revoke")
	req.SetPathValue("grantId", grantID)
	rr := httptest.NewRecorder()
	h.RevokeTrust(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("revoke status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}

	e := trustEntry(t, rec, journal.EntryTrustRevoked)
	if e.WorkspaceID != wsID {
		t.Errorf("workspace_id=%q want %q", e.WorkspaceID, wsID)
	}
	if e.ActorType != journal.ActorUser || e.ActorID != userID {
		t.Errorf("actor=%s/%s want user/%s", e.ActorType, e.ActorID, userID)
	}
	if got := refString(t, e, "trust_grant_id"); got != grantID {
		t.Errorf("refs.trust_grant_id=%q want the revoked grant %q", got, grantID)
	}
	if got := refString(t, e, "step_id"); got != "publish" {
		t.Errorf("refs.step_id=%q want publish — the revoke must name the gate that starts asking again", got)
	}
	if got := entryPayloadString(t, e, "reason"); got != "routine is being re-reviewed" {
		t.Errorf("payload.reason=%q want the ?reason= the operator supplied", got)
	}
}

// TestTrustRevokeDoesNotJournalAMiss — a revoke that changed nothing (already
// revoked, or an id belonging to another routine) must not write an entry
// saying trust was withdrawn. A journal that logs attempts as if they were
// outcomes is worse than one that logs nothing: it makes the audit disagree
// with the table.
func TestTrustRevokeDoesNotJournalAMiss(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	_ = seedTrustableRoutine(t, h, wsID, "tj-miss")
	rec := &recordingEmitter{}
	h.SetJournal(rec)

	req := withWorkspaceUser(httptest.NewRequest("DELETE", "/x", nil), userID, wsID, "MANAGER")
	req.SetPathValue("slug", "tj-miss")
	req.SetPathValue("grantId", "no-such-grant")
	rr := httptest.NewRecorder()
	h.RevokeTrust(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 for an unknown grant; body=%s", rr.Code, rr.Body.String())
	}
	for _, e := range rec.entries {
		if e.Type == journal.EntryTrustRevoked {
			t.Fatalf("a revoke that affected no row emitted %s — the trail now claims a withdrawal that never happened", e.Type)
		}
	}
}

// TestTrustGrantJournalCrossTenantScope — the entry is scoped to the
// workspace in the request context, which is the same workspace the grant row
// is written under. Neighbouring convention (escalation_segregation_test.go,
// escalation_waiter_authz_test.go): prove the scope rather than assume it.
func TestTrustGrantJournalCrossTenantScope(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	_ = seedTrustableRoutine(t, h, wsID, "tj-scope")
	rec := &recordingEmitter{}
	h.SetJournal(rec)

	// A caller presenting a DIFFERENT workspace cannot see the routine at
	// all, so there is nothing to trust and nothing to journal.
	rr := httptest.NewRecorder()
	h.GrantTrust(rr, grantTrustReq(t, userID, "ws-somebody-else", "tj-scope", "MANAGER", `{"step_id":"publish"}`))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 for a foreign workspace; body=%s", rr.Code, rr.Body.String())
	}
	for _, e := range rec.entries {
		if e.Type == journal.EntryTrustGranted {
			t.Fatalf("a refused cross-tenant grant emitted %s scoped to %q", e.Type, e.WorkspaceID)
		}
	}
}
