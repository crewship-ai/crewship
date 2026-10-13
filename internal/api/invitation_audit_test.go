package api

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInvitationLifecycleJournal(t *testing.T) {
	db := setupTestDB(t)
	owner, ws := seedInviter(t, db)
	h := NewWorkspaceHandler(db, newTestLogger())
	create := httptest.NewRecorder()
	h.CreateInvitation(create, withWorkspaceUser(httptest.NewRequest("POST", "/invitations", strings.NewReader(`{"email":"audit-target@example.test","role":"MEMBER"}`)), owner, ws, "OWNER"))
	if create.Code != 201 {
		t.Fatal(create.Code, create.Body.String())
	}
	var inv invitationResponse
	if err := json.Unmarshal(create.Body.Bytes(), &inv); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND entry_type='invitation.created' AND actor_id=?`, ws, owner).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("committed invitation missing atomic journal entry: count=%d", count)
	}
}

func TestInvitationJournalMutationAndAcceptance(t *testing.T) {
	db := setupTestDB(t)
	owner, ws := seedInviter(t, db)
	h := NewWorkspaceHandler(db, newTestLogger())
	create := httptest.NewRecorder()
	h.CreateInvitation(create, withWorkspaceUser(httptest.NewRequest("POST", "/invitations", strings.NewReader(`{"email":"audit-accept@example.test","role":"MEMBER"}`)), owner, ws, "OWNER"))
	if create.Code != 201 {
		t.Fatal(create.Code, create.Body.String())
	}
	var inv invitationResponse
	_ = json.Unmarshal(create.Body.Bytes(), &inv)
	mutate := func(reissue bool) {
		req := withWorkspaceUser(httptest.NewRequest("POST", "/invitations", nil), owner, ws, "OWNER")
		req.SetPathValue("invitationId", inv.ID)
		rr := httptest.NewRecorder()
		if reissue {
			h.ReissueInvitation(rr, req)
			if rr.Code == 200 {
				_ = json.Unmarshal(rr.Body.Bytes(), &inv)
			}
		} else {
			h.RevokeInvitation(rr, req)
		}
		if rr.Code != 200 {
			t.Fatal(rr.Code, rr.Body.String())
		}
	}
	mutate(true)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('audit-accept','audit-accept@example.test')`)
	authH := NewAuthHandler(db, newTestLogger(), nil, nil, false)
	accept := func() {
		rr := httptest.NewRecorder()
		authH.AcceptInvitation(rr, withWorkspaceUser(httptest.NewRequest("POST", "/accept", strings.NewReader(`{"token":"`+inv.Token+`"}`)), "audit-accept", ws, "MEMBER"))
		if rr.Code != 200 {
			t.Fatal(rr.Code, rr.Body.String())
		}
	}
	accept()
	accept() // retry is a no-op, not a second accepted event
	var count int
	for _, kind := range []string{"created", "reissued", "accepted"} {
		if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND entry_type=?`, ws, "invitation."+kind).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v", kind, count, err)
		}
	}
	// A separate pending row exercises revoke without changing accepted history.
	create = httptest.NewRecorder()
	h.CreateInvitation(create, withWorkspaceUser(httptest.NewRequest("POST", "/invitations", strings.NewReader(`{"email":"audit-revoke@example.test","role":"VIEWER"}`)), owner, ws, "OWNER"))
	if create.Code != 201 {
		t.Fatal(create.Code)
	}
	_ = json.Unmarshal(create.Body.Bytes(), &inv)
	mutate(false)
	rows, err := db.Query(`SELECT entry_type,actor_id,payload FROM journal_entries WHERE workspace_id=? AND entry_type LIKE 'invitation.%'`, ws)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, actor, payload string
		if err := rows.Scan(&kind, &actor, &payload); err != nil {
			t.Fatal(err)
		}
		var data map[string]string
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			t.Fatal(err)
		}
		if len(data) != 3 || data["invitation_id"] == "" || data["role"] == "" {
			t.Fatalf("unexpected audit schema: %s", payload)
		}
		if kind == "invitation.accepted" {
			if actor != "audit-accept" || data["subject_user_id"] != "audit-accept" || data["role"] != "MEMBER" {
				t.Fatalf("wrong acceptance identity: %s %s", actor, payload)
			}
		} else if actor != owner || data["subject_user_id"] != "" {
			t.Fatalf("wrong issuer identity: %s %s", actor, payload)
		}
		if actor == "" || strings.Contains(payload, "@") || strings.Contains(payload, "sha256:") || strings.Contains(payload, "wi1_") {
			t.Fatalf("unsafe audit actor=%q payload=%s", actor, payload)
		}
	}
}

func TestInvitationJournalFailureRollsBack(t *testing.T) {
	for _, operation := range []string{"created", "reissued", "revoked", "accepted"} {
		t.Run(operation, func(t *testing.T) {
			db := setupTestDB(t)
			owner, ws := seedInviter(t, db)
			h := NewWorkspaceHandler(db, newTestLogger())
			execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('audit-rollback','audit-rollback@example.test')`)
			seedInvitation(t, db, "audit-rollback", ws, owner, "audit-rollback@example.test", "MEMBER", time.Now().Add(time.Hour), nil)
			execOrFatal(t, db, `CREATE TRIGGER reject_invitation_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type='invitation.`+operation+`' BEGIN SELECT RAISE(ABORT,'synthetic journal failure'); END`)
			rr := httptest.NewRecorder()
			req := withWorkspaceUser(httptest.NewRequest("POST", "/invitations", strings.NewReader(`{"email":"audit-create-rollback@example.test","role":"MEMBER"}`)), owner, ws, "OWNER")
			req.SetPathValue("invitationId", "audit-rollback")
			switch operation {
			case "created":
				h.CreateInvitation(rr, req)
			case "reissued":
				h.ReissueInvitation(rr, req)
			case "revoked":
				h.RevokeInvitation(rr, req)
			case "accepted":
				NewAuthHandler(db, newTestLogger(), nil, nil, false).AcceptInvitation(rr, withWorkspaceUser(httptest.NewRequest("POST", "/accept", strings.NewReader(`{"token":"tok-audit-rollback"}`)), "audit-rollback", ws, "MEMBER"))
			}
			if rr.Code != 500 {
				t.Fatalf("%s status%d body%s", operation, rr.Code, rr.Body.String())
			}
			var proof string
			var accepted sql.NullString
			if err := db.QueryRow(`SELECT token,accepted_at FROM workspace_invitations WHERE id='audit-rollback'`).Scan(&proof, &accepted); err != nil {
				t.Fatal(err)
			}
			if proof != invitationTokenHash("tok-audit-rollback") || accepted.Valid {
				t.Fatal("failed audit changed invitation")
			}
			var members, created int
			_ = db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE user_id='audit-rollback'`).Scan(&members)
			_ = db.QueryRow(`SELECT COUNT(*) FROM workspace_invitations WHERE email='audit-create-rollback@example.test'`).Scan(&created)
			if members != 0 || created != 0 {
				t.Fatal("failed audit left membership/invitation", members, created)
			}
		})
	}
}

func TestInvitationJournalConcurrentAcceptance(t *testing.T) {
	db := setupTestDB(t)
	owner, ws := seedInviter(t, db)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('audit-concurrent','audit-concurrent@example.test')`)
	seedInvitation(t, db, "audit-concurrent", ws, owner, "audit-concurrent@example.test", "MEMBER", time.Now().Add(time.Hour), nil)
	h := NewAuthHandler(db, newTestLogger(), nil, nil, false)
	start := make(chan struct{})
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			rr := httptest.NewRecorder()
			h.AcceptInvitation(rr, withWorkspaceUser(httptest.NewRequest("POST", "/accept", strings.NewReader(`{"token":"tok-audit-concurrent"}`)), "audit-concurrent", ws, "MEMBER"))
			results <- rr.Code
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		select {
		case code := <-results:
			if code != 200 {
				t.Fatalf("concurrent status%d", code)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent acceptance stalled")
		}
	}
	var members, events int
	_ = db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE user_id='audit-concurrent'`).Scan(&members)
	_ = db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='invitation.accepted' AND actor_id='audit-concurrent'`).Scan(&events)
	if members != 1 || events != 1 {
		t.Fatal("concurrent acceptance not exactly once", members, events)
	}
}
