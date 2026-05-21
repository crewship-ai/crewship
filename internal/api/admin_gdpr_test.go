package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

// gdprTestRig wires the AdminGDPRHandler against a real sqlite,
// seeds a workspace + admin user + a target data subject, and
// gives test helpers for crafting requests with role/workspace
// context already plumbed.
type gdprTestRig struct {
	h         *AdminGDPRHandler
	db        *sql.DB
	wsID      string
	adminID   string
	targetID  string
	crewID    string
	agentID   string
	agentSlug string
	output    string
}

func gdprTestSetup(t *testing.T) *gdprTestRig {
	t.Helper()
	dir := t.TempDir()
	dbh, err := database.Open("file:" + filepath.Join(dir, "g.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(context.Background(), dbh.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = dbh.Close() })
	if _, err := dbh.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := dbh.Exec(`INSERT INTO users (id, email)
		VALUES ('admin1','admin@x'),('subj1','subj@x'),('member1','member@x')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := dbh.Exec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, allowed_domains)
		VALUES ('crew1','ws1','C','c','free','[]')`); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := dbh.Exec(`INSERT INTO agents (id, workspace_id, crew_id, slug, name, agent_role)
		VALUES ('a1','ws1','crew1','alice','Alice','AGENT')`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return &gdprTestRig{
		h:         NewAdminGDPRHandler(dbh.DB, silent, dir),
		db:        dbh.DB,
		wsID:      "ws1",
		adminID:   "admin1",
		targetID:  "subj1",
		crewID:    "crew1",
		agentID:   "a1",
		agentSlug: "alice",
		output:    dir,
	}
}

// seedAll loads representative rows in each cascadable table that
// point at the data subject. Returns expected counts.
func (r *gdprTestRig) seedAll(t *testing.T) {
	t.Helper()
	// peer_cards row referencing the user.
	if _, err := r.db.Exec(`INSERT INTO peer_cards
		(id, workspace_id, agent_id, user_id, user_slug, path, bytes)
		VALUES ('pc1','ws1','a1','subj1','slug-subj1','/peers/slug-subj1.md',100)`); err != nil {
		t.Fatalf("seed peer_cards: %v", err)
	}
	// memory_versions: persona/peer tier write ABOUT this user.
	if _, err := r.db.Exec(`INSERT INTO memory_versions
		(id, workspace_id, path, tier, sha256, bytes, payload_ref, data_subject_id)
		VALUES ('mv1','ws1','/p/subj','peer','sha-mv1',50,'/blob/sha-mv1','subj1'),
		       ('mv2','ws1','/p/subj','peer','sha-mv2',60,'/blob/sha-mv2','subj1')`); err != nil {
		t.Fatalf("seed memory_versions: %v", err)
	}
	// memory_versions row for a DIFFERENT subject — must NOT be touched.
	if _, err := r.db.Exec(`INSERT INTO memory_versions
		(id, workspace_id, path, tier, sha256, bytes, payload_ref, data_subject_id)
		VALUES ('mv-other','ws1','/p/other','peer','sha-other',10,'/blob/sha-other','member1')`); err != nil {
		t.Fatalf("seed mv-other: %v", err)
	}
	// inbox_items row tagged for the subject (persona suggestion proposal).
	if _, err := r.db.Exec(`INSERT INTO inbox_items
		(id, workspace_id, kind, source_id, title, data_subject_id)
		VALUES ('ib1','ws1','message','msg-subj1','persona suggestion about subj1','subj1')`); err != nil {
		t.Fatalf("seed inbox_items: %v", err)
	}
}

// adminReq crafts a request with workspace + admin role context
// pre-plumbed. role can be overridden (e.g. for the RBAC test).
func (r *gdprTestRig) adminReq(t *testing.T, method, body, userIDPath, role string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, "/", bytes.NewBufferString(body))
	req.SetPathValue("userId", userIDPath)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, r.wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: r.adminID})
	ctx = context.WithValue(ctx, ctxRole, role)
	return req.WithContext(ctx)
}

func TestGDPRCascade_DeletesPeerCards(t *testing.T) {
	r := gdprTestSetup(t)
	r.seedAll(t)

	rec := httptest.NewRecorder()
	body := `{"reason":"GDPR SAR ticket #1234"}`
	r.h.DeleteUserData(rec, r.adminReq(t, http.MethodDelete, body, r.targetID, "ADMIN"))

	if rec.Code != http.StatusAccepted && rec.Code != http.StatusMultiStatus {
		t.Fatalf("expected 202/207; got %d body=%s", rec.Code, rec.Body.String())
	}

	var cnt int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM peer_cards
		WHERE workspace_id=? AND user_id=?`, r.wsID, r.targetID).Scan(&cnt); err != nil {
		t.Fatalf("count peer_cards: %v", err)
	}
	if cnt != 0 {
		t.Errorf("peer_cards remaining for subject = %d, want 0", cnt)
	}
}

func TestGDPRCascade_DeletesMemoryVersions(t *testing.T) {
	r := gdprTestSetup(t)
	r.seedAll(t)

	rec := httptest.NewRecorder()
	r.h.DeleteUserData(rec, r.adminReq(t, http.MethodDelete,
		`{"reason":"sar"}`, r.targetID, "ADMIN"))
	if rec.Code >= 400 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	var cnt int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM memory_versions
		WHERE workspace_id=? AND data_subject_id=?`, r.wsID, r.targetID).Scan(&cnt); err != nil {
		t.Fatalf("count memory_versions: %v", err)
	}
	if cnt != 0 {
		t.Errorf("memory_versions remaining for subject = %d, want 0", cnt)
	}

	// Other subject's row must remain — scope isolation invariant.
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM memory_versions
		WHERE workspace_id=? AND data_subject_id='member1'`, r.wsID).Scan(&cnt); err != nil {
		t.Fatalf("count memory_versions other: %v", err)
	}
	if cnt != 1 {
		t.Errorf("other subject memory_versions = %d, want 1 (cascade leaked)", cnt)
	}
}

func TestGDPRCascade_IsIdempotent(t *testing.T) {
	r := gdprTestSetup(t)
	r.seedAll(t)

	// First call.
	rec := httptest.NewRecorder()
	r.h.DeleteUserData(rec, r.adminReq(t, http.MethodDelete,
		`{"reason":"first"}`, r.targetID, "ADMIN"))
	if rec.Code >= 400 {
		t.Fatalf("first delete: %d %s", rec.Code, rec.Body.String())
	}

	// Second call — must not error, returns zero counts in scope.
	rec2 := httptest.NewRecorder()
	r.h.DeleteUserData(rec2, r.adminReq(t, http.MethodDelete,
		`{"reason":"second"}`, r.targetID, "ADMIN"))
	if rec2.Code >= 400 {
		t.Fatalf("second delete: %d %s", rec2.Code, rec2.Body.String())
	}
	var resp struct {
		Scope struct {
			PeerCards      int `json:"peer_cards"`
			MemoryVersions int `json:"memory_versions"`
			InboxItems     int `json:"inbox_items"`
		} `json:"scope"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode second delete response: %v body=%s", err, rec2.Body.String())
	}
	if got := resp.Scope.PeerCards + resp.Scope.MemoryVersions + resp.Scope.InboxItems; got != 0 {
		t.Errorf("second delete scope sum = %d, want 0 (idempotent)", got)
	}

	// Two gdpr_actions rows for the subject (both attempts logged).
	var cnt int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM gdpr_actions
		WHERE workspace_id=? AND data_subject_id=? AND action='delete'`,
		r.wsID, r.targetID).Scan(&cnt); err != nil {
		t.Fatalf("count gdpr_actions: %v", err)
	}
	if cnt != 2 {
		t.Errorf("gdpr_actions delete rows = %d, want 2 (both attempts audited)", cnt)
	}
}

func TestGDPRCascade_LogsToGDPRActions(t *testing.T) {
	r := gdprTestSetup(t)
	r.seedAll(t)

	rec := httptest.NewRecorder()
	r.h.DeleteUserData(rec, r.adminReq(t, http.MethodDelete,
		`{"reason":"GDPR SAR ticket #42"}`, r.targetID, "ADMIN"))
	if rec.Code >= 400 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	var (
		actor, action, status, reason, scopeJSON string
		completedAt                              sql.NullString
	)
	err := r.db.QueryRow(`SELECT actor_user_id, action, status,
		COALESCE(reason,''), COALESCE(scope_json,''), completed_at
		FROM gdpr_actions
		WHERE workspace_id=? AND data_subject_id=?`,
		r.wsID, r.targetID).Scan(&actor, &action, &status, &reason, &scopeJSON, &completedAt)
	if err != nil {
		t.Fatalf("read gdpr_actions: %v", err)
	}
	if actor != r.adminID {
		t.Errorf("actor = %q, want %q", actor, r.adminID)
	}
	if action != "delete" {
		t.Errorf("action = %q, want \"delete\"", action)
	}
	if status != "completed" {
		t.Errorf("status = %q, want \"completed\"", status)
	}
	if reason != "GDPR SAR ticket #42" {
		t.Errorf("reason = %q, want \"GDPR SAR ticket #42\"", reason)
	}
	if !completedAt.Valid {
		t.Error("completed_at = NULL, want timestamp")
	}
	// scope_json must contain non-zero counts.
	var scope map[string]int
	if err := json.Unmarshal([]byte(scopeJSON), &scope); err != nil {
		t.Fatalf("scope_json parse: %v body=%q", err, scopeJSON)
	}
	if scope["peer_cards"] != 1 || scope["memory_versions"] != 2 || scope["inbox_items"] != 1 {
		t.Errorf("scope = %+v, want peer_cards=1 mv=2 inbox=1", scope)
	}
}

func TestGDPRExport_ReturnsAllUserData(t *testing.T) {
	r := gdprTestSetup(t)
	r.seedAll(t)

	rec := httptest.NewRecorder()
	r.h.ExportUserData(rec, r.adminReq(t, http.MethodGet, "", r.targetID, "ADMIN"))
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}

	var bundle struct {
		DataSubjectID string           `json:"data_subject_id"`
		ActionID      string           `json:"action_id"`
		PeerCards     []map[string]any `json:"peer_cards"`
		MemoryVersion []map[string]any `json:"memory_versions"`
		InboxItems    []map[string]any `json:"inbox_items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &bundle); err != nil {
		t.Fatalf("parse bundle: %v", err)
	}
	if bundle.DataSubjectID != r.targetID {
		t.Errorf("data_subject_id = %q, want %q", bundle.DataSubjectID, r.targetID)
	}
	if bundle.ActionID == "" {
		t.Error("action_id empty in response")
	}
	if len(bundle.PeerCards) != 1 {
		t.Errorf("peer_cards in bundle = %d, want 1", len(bundle.PeerCards))
	}
	if len(bundle.MemoryVersion) != 2 {
		t.Errorf("memory_versions in bundle = %d, want 2", len(bundle.MemoryVersion))
	}
	if len(bundle.InboxItems) != 1 {
		t.Errorf("inbox_items in bundle = %d, want 1", len(bundle.InboxItems))
	}

	// Export must have logged a gdpr_actions row with action='export'.
	var cnt int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM gdpr_actions
		WHERE data_subject_id=? AND action='export' AND status='completed'`,
		r.targetID).Scan(&cnt); err != nil {
		t.Fatalf("count export actions: %v", err)
	}
	if cnt != 1 {
		t.Errorf("export gdpr_actions = %d, want 1", cnt)
	}
}

func TestGDPRDelete_RequiresReason(t *testing.T) {
	r := gdprTestSetup(t)

	// Empty body.
	rec := httptest.NewRecorder()
	r.h.DeleteUserData(rec, r.adminReq(t, http.MethodDelete, `{}`, r.targetID, "ADMIN"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty reason: status = %d, want 400", rec.Code)
	}

	// Whitespace-only reason.
	rec = httptest.NewRecorder()
	r.h.DeleteUserData(rec, r.adminReq(t, http.MethodDelete,
		`{"reason":"   "}`, r.targetID, "ADMIN"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("blank reason: status = %d, want 400", rec.Code)
	}

	// Confirm nothing was deleted AND no gdpr_actions row was written
	// (the audit row writes only after reason validation).
	r.seedAll(t)
	rec = httptest.NewRecorder()
	r.h.DeleteUserData(rec, r.adminReq(t, http.MethodDelete, `{}`, r.targetID, "ADMIN"))

	var cnt int
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM peer_cards
		WHERE workspace_id=? AND user_id=?`, r.wsID, r.targetID).Scan(&cnt)
	if cnt != 1 {
		t.Errorf("peer_cards after rejected delete = %d, want 1 (delete must not have run)", cnt)
	}
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM gdpr_actions
		WHERE data_subject_id=? AND action='delete'`, r.targetID).Scan(&cnt)
	if cnt != 0 {
		t.Errorf("gdpr_actions rows after rejected delete = %d, want 0 (no audit on validation failure)", cnt)
	}
}

func TestGDPRDelete_RequiresAdmin(t *testing.T) {
	r := gdprTestSetup(t)
	r.seedAll(t)

	for _, role := range []string{"MEMBER", "VIEWER", "MANAGER", ""} {
		rec := httptest.NewRecorder()
		r.h.DeleteUserData(rec, r.adminReq(t, http.MethodDelete,
			`{"reason":"x"}`, r.targetID, role))
		if rec.Code != http.StatusForbidden {
			t.Errorf("role=%q DELETE: status = %d, want 403", role, rec.Code)
		}

		recE := httptest.NewRecorder()
		r.h.ExportUserData(recE, r.adminReq(t, http.MethodGet, "", r.targetID, role))
		if recE.Code != http.StatusForbidden {
			t.Errorf("role=%q EXPORT: status = %d, want 403", role, recE.Code)
		}
	}

	// Sanity: ADMIN + OWNER do pass the gate.
	for _, role := range []string{"ADMIN", "OWNER"} {
		rec := httptest.NewRecorder()
		r.h.ExportUserData(rec, r.adminReq(t, http.MethodGet, "", r.targetID, role))
		if rec.Code != http.StatusOK {
			t.Errorf("role=%q EXPORT: status = %d, want 200", role, rec.Code)
		}
	}
}
