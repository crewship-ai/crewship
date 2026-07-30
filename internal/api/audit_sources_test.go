package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A workspace keeps five separate audit trails — audit_logs (this page),
// crew_audit_log (cross-crew dispatch and messages), credential_audit (who
// used which secret), the keeper's append-only decision ledger, and
// peer_card_audit. The settings page read exactly one of them: the one whose
// only recorded events were agents being created and deleted.
//
// The tables stay separate — the keeper ledger is append-only ON PURPOSE and
// merging it into a general log would weaken that guarantee — but the READING
// is unified, so an operator asking "what happened in this workspace" has one
// place to ask instead of four.

func auditListSource(t *testing.T, h *AuditHandler, wsID, source string) auditListResponse {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/audit?source="+source, nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("source %q: status = %d, body=%s", source, rr.Code, rr.Body.String())
	}
	var out auditListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestAuditSources_CrewStreamIsReadable(t *testing.T) {
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewA := seedCrewRow(t, db, "crew-src-a", wsID, "Engineering", "engineering")
	crewB := seedCrewRow(t, db, "crew-src-b", wsID, "Ops", "ops")

	if _, err := db.Exec(`
		INSERT INTO crew_audit_log (id, workspace_id, action, from_crew_id, to_crew_id, details, created_at)
		VALUES (?, ?, 'message.sent', ?, ?, '{"size":42}', datetime('now'))`,
		generateCUID(), wsID, crewA, crewB); err != nil {
		t.Fatalf("seed crew audit: %v", err)
	}

	got := auditListSource(t, h, wsID, "crews")
	if len(got.Data) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(got.Data), got.Data)
	}
	row := got.Data[0]
	if row.Action != "message.sent" {
		t.Errorf("action = %q, want message.sent", row.Action)
	}
	// Normalised into the same shape the workspace stream uses, naming the
	// crew rather than handing back an opaque id.
	if row.EntityName == nil || *row.EntityName != "Engineering" {
		t.Errorf("entity_name = %v, want Engineering", row.EntityName)
	}
}

func TestAuditSources_CredentialStreamIsReadable(t *testing.T) {
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(
		`INSERT INTO credentials (id, workspace_id, name, encrypted_value, created_by) VALUES ('cred-src', ?, 'GH_TOKEN', 'x', ?)`,
		wsID, userID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO credential_audit (id, credential_id, event_type, occurred_at)
		VALUES (?, 'cred-src', 'revealed', datetime('now'))`, generateCUID()); err != nil {
		t.Fatalf("seed credential audit: %v", err)
	}

	got := auditListSource(t, h, wsID, "credentials")
	if len(got.Data) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(got.Data), got.Data)
	}
	if got.Data[0].Action != "revealed" {
		t.Errorf("action = %q, want revealed", got.Data[0].Action)
	}
	if got.Data[0].EntityName == nil || *got.Data[0].EntityName != "GH_TOKEN" {
		t.Errorf("entity_name = %v, want GH_TOKEN", got.Data[0].EntityName)
	}
}

func TestAuditSources_KeeperStreamIsReadable(t *testing.T) {
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`
		INSERT INTO keeper_request_events (id, request_id, workspace_id, seq, state, actor_type, recorded_at)
		VALUES (?, 'req-1', ?, 1, 'DENY', 'keeper', datetime('now'))`,
		generateCUID(), wsID); err != nil {
		t.Fatalf("seed keeper event: %v", err)
	}

	got := auditListSource(t, h, wsID, "keeper")
	if len(got.Data) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(got.Data), got.Data)
	}
	if got.Data[0].Action != "DENY" {
		t.Errorf("action = %q, want DENY", got.Data[0].Action)
	}
}

// The date range is the one filter that means the same thing on every
// trail, and the UI sends date_from on all of them. Dropping it made the
// selector and the result disagree: the operator narrowed to a week and
// got all history back, with no sign the range had been ignored.
func TestAuditSources_HonourTheDateRange(t *testing.T) {
	for _, tc := range []struct {
		source string
		seed   func(t *testing.T, db *sql.DB, wsID, userID, when string)
	}{
		{"crews", func(t *testing.T, db *sql.DB, wsID, userID, when string) {
			crewA := seedCrewRow(t, db, "crew-dr-"+when[:4], wsID, "Engineering", "eng-"+when[:4])
			if _, err := db.Exec(`
				INSERT INTO crew_audit_log (id, workspace_id, action, from_crew_id, details, created_at)
				VALUES (?, ?, 'message.sent', ?, '{}', ?)`,
				generateCUID(), wsID, crewA, when); err != nil {
				t.Fatalf("seed crew audit: %v", err)
			}
		}},
		{"credentials", func(t *testing.T, db *sql.DB, wsID, userID, when string) {
			credID := "cred-dr-" + when[:4]
			if _, err := db.Exec(
				`INSERT INTO credentials (id, workspace_id, name, encrypted_value, created_by) VALUES (?, ?, ?, 'x', ?)`,
				credID, wsID, "GH_TOKEN_"+when[:4], userID); err != nil {
				t.Fatalf("seed credential: %v", err)
			}
			if _, err := db.Exec(`
				INSERT INTO credential_audit (id, credential_id, event_type, occurred_at)
				VALUES (?, ?, 'revealed', ?)`, generateCUID(), credID, when); err != nil {
				t.Fatalf("seed credential audit: %v", err)
			}
		}},
		{"keeper", func(t *testing.T, db *sql.DB, wsID, userID, when string) {
			if _, err := db.Exec(`
				INSERT INTO keeper_request_events (id, request_id, workspace_id, seq, state, actor_type, recorded_at)
				VALUES (?, ?, ?, 1, 'DENY', 'keeper', ?)`,
				generateCUID(), "req-"+when[:4], wsID, when); err != nil {
				t.Fatalf("seed keeper event: %v", err)
			}
		}},
	} {
		t.Run(tc.source, func(t *testing.T) {
			h, db := newAuditHandler(t)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			tc.seed(t, db, wsID, userID, "2020-01-01 00:00:00")
			tc.seed(t, db, wsID, userID, "2026-06-01 00:00:00")

			all := auditListSource(t, h, wsID, tc.source)
			if len(all.Data) != 2 {
				t.Fatalf("unfiltered rows = %d, want 2: %+v", len(all.Data), all.Data)
			}

			req := httptest.NewRequest("GET",
				"/api/v1/audit?source="+tc.source+"&date_from=2026-01-01&date_to=2026-12-31", nil)
			req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
			rr := httptest.NewRecorder()
			h.List(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
			}
			var got auditListResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(got.Data) != 1 {
				t.Fatalf("rows in range = %d, want 1 (the 2020 row leaked): %+v", len(got.Data), got.Data)
			}
			// The count drives pagination — a total that ignores the range
			// tells the operator there are pages of results that aren't there.
			if got.Pagination.Total != 1 {
				t.Errorf("pagination total = %d, want 1", got.Pagination.Total)
			}
		})
	}
}

// A source nobody asked for must not silently answer with another one's data.
func TestAuditSources_UnknownSourceIsRejected(t *testing.T) {
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("GET", "/api/v1/audit?source=everything", nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// Every stream is workspace-scoped, or the page becomes a cross-tenant leak
// with four times the surface.
func TestAuditSources_AreWorkspaceScoped(t *testing.T) {
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	otherWS, otherCrew := seedOtherWorkspaceWithCrew(t, db)

	if _, err := db.Exec(`
		INSERT INTO crew_audit_log (id, workspace_id, action, from_crew_id, created_at)
		VALUES (?, ?, 'message.sent', ?, datetime('now'))`,
		generateCUID(), otherWS, otherCrew); err != nil {
		t.Fatalf("seed foreign crew audit: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO keeper_request_events (id, request_id, workspace_id, seq, state, actor_type, recorded_at)
		VALUES (?, 'req-other', ?, 1, 'ALLOW', 'keeper', datetime('now'))`,
		generateCUID(), otherWS); err != nil {
		t.Fatalf("seed foreign keeper event: %v", err)
	}

	for _, src := range []string{"crews", "keeper", "credentials"} {
		if got := auditListSource(t, h, wsID, src); len(got.Data) != 0 {
			t.Errorf("source %q leaked %d foreign rows: %+v", src, len(got.Data), got.Data)
		}
	}
}

// The default is the stream this page has always shown, so no existing caller
// (the CLI, a bookmark, the export) changes behaviour under them.
func TestAuditSources_DefaultsToTheWorkspaceStream(t *testing.T) {
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAuditLog(t, db, wsID, userID, "create", "AGENT")

	got := auditListSource(t, h, wsID, "")
	if len(got.Data) != 1 || got.Data[0].Action != "create" {
		t.Fatalf("default source did not return the workspace stream: %+v", got.Data)
	}
}
