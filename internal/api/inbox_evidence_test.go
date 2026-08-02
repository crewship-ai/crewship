package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The operator ruling on escalations said what a person actually needs: not a
// verdict and not advice, but the consequences — "is there a backup?", "would a
// narrower key do?" — and then to be left to decide.
//
// internal/keeper/evidence already computes exactly that, and it has computed it
// for months. The facts went into the JUDGE's prompt and stopped there. The
// person deciding got the model's `reason`, which is a case FOR the verdict
// already reached, not a briefing.
//
// Two decisions this pins:
//
//	Read time, not raise time. The same rule the four-eyes notice follows, and
//	for the same reason: a backup taken SINCE the escalation changes whether
//	approving now is safe, and a frozen "no backup" would argue against
//	something that is now backed up.
//
//	Detail only, never the list. One item costs two indexed queries; a page of
//	fifty would cost a hundred, and nobody reads facts off a list row.
//
// The card renders what this computes and computes nothing itself. A fact
// derived in React is a fact nobody can test, and the next person adds a
// heuristic to it and calls it advice.

type inboxEvidenceRow struct {
	ID       string `json:"id"`
	Evidence *struct {
		LastBackup *struct {
			Exists   bool `json:"exists"`
			AgeHours int  `json:"age_hours"`
		} `json:"last_backup"`
		NarrowerCredential *struct {
			Exists        bool   `json:"exists"`
			Name          string `json:"name"`
			SecurityLevel int    `json:"security_level"`
		} `json:"narrower_credential"`
	} `json:"evidence"`
}

func getInboxEvidence(t *testing.T, h *InboxHandler, userID, wsID, id string) inboxEvidenceRow {
	t.Helper()
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/inbox/"+id, nil), userID, wsID, "OWNER")
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("inbox get = %d: %s", rr.Code, rr.Body.String())
	}
	var out inboxEvidenceRow
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rr.Body.String())
	}
	return out
}

// seedInboxEvidence builds a keeper credential escalation with a backup and a
// narrower credential to find.
func seedInboxEvidence(t *testing.T, withBackup, withNarrower bool) (*InboxHandler, string, string) {
	t.Helper()
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	crewID := seedCrewRow(t, db, "ev-crew", wsID, "Crew", "ev-crew")
	seedOwnedAgent(t, db, "ev-agent", wsID, crewID, ownerID)

	execOrFatal(t, db, `INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, security_level, status, created_by)
		VALUES ('ev-cred', ?, 'PROD_DB_ADMIN', 'v1:aW52YWxpZA==', 'SECRET', 'POSTGRES', 4, 'ACTIVE', ?)`,
		wsID, ownerID)
	if withNarrower {
		execOrFatal(t, db, `INSERT INTO credentials
			(id, workspace_id, name, encrypted_value, type, provider, security_level, status, created_by)
			VALUES ('ev-cred-lo', ?, 'PROD_DB_READONLY', 'v1:aW52YWxpZA==', 'SECRET', 'POSTGRES', 2, 'ACTIVE', ?)`,
			wsID, ownerID)
	}
	if withBackup {
		execOrFatal(t, db, `INSERT INTO backup_catalog
			(id, file_path, scope, slug, workspace_id, created_at, size, sha256, encrypted, format_version)
			VALUES ('bk1', '/b1.tar', 'workspace', 'demo', ?, '2026-01-01T00:00:00Z', 1, 'x', 1, 3)`, wsID)
	}

	execOrFatal(t, db, `INSERT INTO keeper_requests
		(id, request_type, requesting_agent_id, requesting_crew_id, credential_id, intent, decision)
		VALUES ('ev-kr', 'access', 'ev-agent', ?, 'ev-cred', 'drop the deprecated table', 'ESCALATE')`, crewID)
	execOrFatal(t, db, `INSERT INTO inbox_items
		(id, workspace_id, kind, source_id, target_role, title, body_md,
		 sender_type, sender_id, sender_name, state, priority, blocking,
		 payload_json, created_at, updated_at)
		VALUES ('ev-inbox', ?, 'escalation', 'ev-kr', 'ADMIN', 'Keeper escalation', '',
		 'system', 'keeper', 'Keeper', 'unread', 'high', 1,
		 '{"request_type":"access","request_id":"ev-kr","credential_id":"ev-cred","agent_id":"ev-agent"}',
		 datetime('now'), datetime('now'))`, wsID)

	return NewInboxHandler(db, newTestLogger(), nil), ownerID, wsID
}

func TestInboxGet_CarriesTheFactsThePersonNeeds(t *testing.T) {
	h, ownerID, wsID := seedInboxEvidence(t, true, true)

	got := getInboxEvidence(t, h, ownerID, wsID, "ev-inbox")
	if got.Evidence == nil {
		t.Fatal("no evidence on a keeper credential escalation; the person deciding " +
			"still only gets the model's case for its own verdict")
	}
	if got.Evidence.LastBackup == nil || !got.Evidence.LastBackup.Exists {
		t.Errorf("last_backup = %+v, want a backup the workspace actually has", got.Evidence.LastBackup)
	}
	if n := got.Evidence.NarrowerCredential; n == nil || !n.Exists || n.Name != "PROD_DB_READONLY" {
		t.Errorf("narrower_credential = %+v, want PROD_DB_READONLY", n)
	}
}

// "We looked and there is none" is the answer that matters most on a
// destructive request, and it must reach the card as a stated fact rather than
// as a missing field the reader can mistake for "not checked".
func TestInboxGet_SaysWhenThereIsNoBackupAndNoNarrowerKey(t *testing.T) {
	h, ownerID, wsID := seedInboxEvidence(t, false, false)

	got := getInboxEvidence(t, h, ownerID, wsID, "ev-inbox")
	if got.Evidence == nil {
		t.Fatal("no evidence block at all")
	}
	if got.Evidence.LastBackup == nil || got.Evidence.LastBackup.Exists {
		t.Errorf("last_backup = %+v, want a present fact reporting none", got.Evidence.LastBackup)
	}
	if got.Evidence.NarrowerCredential == nil || got.Evidence.NarrowerCredential.Exists {
		t.Errorf("narrower_credential = %+v, want a present fact reporting none",
			got.Evidence.NarrowerCredential)
	}
}

// A non-credential item must carry nothing. Evidence is about a credential
// request; attaching it elsewhere would cost queries on every kind and put
// lines on a card they do not describe.
func TestInboxGet_NoEvidenceOnANonCredentialItem(t *testing.T) {
	h, ownerID, wsID := seedInboxEvidence(t, true, true)
	execOrFatal(t, dbOf(h), `UPDATE inbox_items SET payload_json = '{"kind":"skill_proposal"}' WHERE id = 'ev-inbox'`)

	if got := getInboxEvidence(t, h, ownerID, wsID, "ev-inbox"); got.Evidence != nil {
		t.Errorf("evidence = %+v on an item that names no credential", got.Evidence)
	}
}

// The LIST must stay free of it. One item is two indexed queries; a page of
// fifty is a hundred, and nobody reads a backup age off a list row.
func TestInboxList_DoesNotPayForEvidence(t *testing.T) {
	h, ownerID, wsID := seedInboxEvidence(t, true, true)

	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/inbox", nil), ownerID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Rows []inboxEvidenceRow `json:"rows"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, row := range out.Rows {
		if row.Evidence != nil {
			t.Errorf("row %s carries evidence; the list pays per row for something "+
				"only the detail pane shows", row.ID)
		}
	}
}

// dbOf reaches the handler's database for a fixture edit. Declared here rather
// than exporting a field: the handler's db is deliberately private, and one test
// helper is cheaper than widening the type.
func dbOf(h *InboxHandler) *sql.DB { return h.db }
