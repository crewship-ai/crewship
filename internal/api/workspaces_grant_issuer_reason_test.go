package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestMemberEffectiveAccessExplainsInertIssuer(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('reason-admin','reason-admin@example.test'),('reason-guest','reason-guest@example.test')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('reason-admin-member',?,'reason-admin','ADMIN'),('reason-guest-member',?,'reason-guest','VIEWER')`, ws, ws)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('reason-crew',?,'Crew','reason-crew')`, ws)
	seedAgentRow(t, db, "reason-agent", ws, "reason-crew", "Shared worker", "reason-agent", "AGENT")
	execOrFatal(t, db, `UPDATE users SET full_name='Former administrator' WHERE id='reason-admin'`)
	store := access.Store{DB: db}
	m, err := store.Membership(t.Context(), "reason-guest", ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), "reason-admin", "reason-guest", ws, "restricted", m, []access.Right{{Kind: "agent", ID: "reason-agent", Operation: "discover"}}); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE id='reason-admin-member'`)
	h := newWsHandlerForTest(t)
	h.db = db
	req := withWorkspaceUser(httptest.NewRequest("GET", "/", nil), owner, ws, "OWNER")
	req.SetPathValue("workspaceId", ws)
	req.SetPathValue("memberId", "reason-guest-member")
	rr := httptest.NewRecorder()
	h.GetMemberEffectiveAccess(rr, req)
	if rr.Code != 200 {
		t.Fatalf("effective access: %d %s", rr.Code, rr.Body.String())
	}
	var result memberEffectiveAccess
	if err = json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Decisions) != 1 || result.Decisions[0].State != "denied" || !strings.Contains(result.Decisions[0].Reason, "grant issuer") {
		t.Fatalf("issuer loss not explained: %s", rr.Body.String())
	}
	// A read-edit-set round trip cannot forge liveness or attribution; explicit
	// reissue is a separate instruction bound to the authenticated actor.
	policy, err := store.Policy(t.Context(), owner, "reason-guest", ws)
	if err != nil {
		t.Fatal(err)
	}
	if policy.GrantDetails[0].IssuerName != "Former administrator" {
		t.Fatalf("issuer display name missing: %+v", policy)
	}
	policy.GrantDetails[0].IssuerID = owner
	policy.GrantDetails[0].Live = true
	put := func(p access.Policy) access.Policy {
		t.Helper()
		body, _ := json.Marshal(p)
		req := withWorkspaceUser(httptest.NewRequest("PUT", "/", bytes.NewReader(body)), owner, ws, "OWNER")
		req.SetPathValue("workspaceId", ws)
		req.SetPathValue("memberId", "reason-guest-member")
		rr := httptest.NewRecorder()
		h.PutMemberAccess(rr, req)
		if rr.Code != 200 {
			t.Fatalf("policy save: %d %s", rr.Code, rr.Body.String())
		}
		var saved access.Policy
		if err := json.Unmarshal(rr.Body.Bytes(), &saved); err != nil {
			t.Fatal(err)
		}
		return saved
	}
	policy = put(policy)
	if policy.GrantDetails[0].IssuerID != "reason-admin" || policy.GrantDetails[0].Live {
		t.Fatalf("forged metadata trusted: %+v", policy)
	}
	if err := store.Check(t.Context(), "reason-guest", ws, policy.Rights[0]); !errors.Is(err, access.ErrIssuerDenied) {
		t.Fatalf("round trip revived inert grant: %v", err)
	}
	policy.Reissue = policy.Rights
	policy = put(policy)
	if policy.GrantDetails[0].IssuerID != owner || !policy.GrantDetails[0].Live {
		t.Fatalf("explicit reissue did not use authenticated issuer: %+v", policy)
	}

	var payload string
	if err := db.QueryRow(`SELECT payload FROM journal_entries WHERE entry_type='member.access_changed' AND workspace_id=? ORDER BY rowid DESC LIMIT 1`, ws).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var event struct {
		Previous []access.GrantDetail `json:"previous_grants"`
		Current  []access.GrantDetail `json:"grants"`
		Reissued []access.Right       `json:"reissued_rights"`
	}
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		t.Fatal(err)
	}
	if len(event.Previous) != 1 || event.Previous[0].IssuerID != "reason-admin" || event.Previous[0].Live || len(event.Current) != 1 || event.Current[0].IssuerID != owner || !event.Current[0].Live || len(event.Reissued) != 1 || event.Reissued[0] != policy.Rights[0] {
		t.Fatalf("incomplete reissue audit: %s", payload)
	}
	if strings.Contains(payload, "@") || strings.Contains(payload, "Former administrator") {
		t.Fatalf("audit contains an email: %s", payload)
	}
	// If journaling fails, the policy, issuer and revision must all roll back.
	before := policy
	execOrFatal(t, db, `CREATE TRIGGER reject_member_access_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type='member.access_changed' BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`)
	body, _ := json.Marshal(access.Policy{Membership: before.Membership, Rights: []access.Right{}})
	request := withWorkspaceUser(httptest.NewRequest("PUT", "/", bytes.NewReader(body)), owner, ws, "OWNER")
	request.SetPathValue("workspaceId", ws)
	request.SetPathValue("memberId", before.ID)
	response := httptest.NewRecorder()
	h.PutMemberAccess(response, request)
	if response.Code != 500 {
		t.Fatalf("audit failure = %d %s", response.Code, response.Body.String())
	}
	after, err := store.Policy(t.Context(), owner, "reason-guest", ws)
	if err != nil || after.Revision != before.Revision || len(after.Rights) != 1 || after.GrantDetails[0].IssuerID != owner {
		t.Fatalf("audit failure committed policy: %+v %v", after, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='member.access_changed' AND workspace_id=?`, ws).Scan(&count); err != nil || count != 2 {
		t.Fatalf("expected one event per successful policy save: %d %v", count, err)
	}
}
