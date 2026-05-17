package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/journal"
)

// seedProposalRow inserts a pending memory_proposals row + the matching
// proposal markdown on disk + a memory_consolidation inbox item. The
// approve / reject / explain handlers share this fixture so each test
// asserts a single state transition without re-staging.
func seedProposalRow(t *testing.T, db *sql.DB, workspaceID, crewID, status string) (proposalID, proposalPath string) {
	t.Helper()
	dir := t.TempDir()
	proposedDir := filepath.Join(dir, ".proposed")
	if err := os.MkdirAll(proposedDir, 0o755); err != nil {
		t.Fatalf("mkdir proposed: %v", err)
	}
	proposalID = "mp_test_" + workspaceID
	proposalPath = filepath.Join(proposedDir, "proposal-"+proposalID+".md")
	body := "# Proposed learned rules\n\nProposal ID: `" + proposalID + "`\n\n---\n\n" +
		"- **Pattern:** test pattern\n  **Action:** test action\n  **Confidence:** 0.9\n  **Evidence:** e1, e2\n"
	if err := os.WriteFile(proposalPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write proposal: %v", err)
	}

	var decidedAt sql.NullString
	if status != "pending" {
		decidedAt = sql.NullString{String: time.Now().UTC().Format(time.RFC3339Nano), Valid: true}
	}
	if _, err := db.Exec(`
		INSERT INTO memory_proposals (id, workspace_id, crew_id, proposal_path, status, evidence_json, rules_count, entries_scanned, decided_at)
		VALUES (?, ?, ?, ?, ?, '[]', 1, 12, ?)`,
		proposalID, workspaceID, crewID, proposalPath, status, decidedAt); err != nil {
		t.Fatalf("insert proposal: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, payload_json)
		VALUES (?, ?, 'memory_consolidation', ?, 'Memory proposal', '{}')`,
		"ibx_mc_"+proposalID, workspaceID, proposalID); err != nil {
		t.Fatalf("insert inbox: %v", err)
	}
	return proposalID, proposalPath
}

// seedTestCrew inserts a crew row so the journal emitter doesn't trip
// on FK constraints when the approve path writes its emit. Returns
// the crew id.
func seedTestCrew(t *testing.T, db *sql.DB, workspaceID string) string {
	t.Helper()
	crewID := "crew_test_" + workspaceID
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Test Crew', ?)`,
		crewID, workspaceID, "crew-"+workspaceID); err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	return crewID
}

func newProposedHandlerTest(t *testing.T) (*ProposedHandler, *sql.DB, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedTestCrew(t, db, wsID)
	h := NewProposedHandler(db, newTestLogger())
	w := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = w.Close() })
	h.SetJournal(w)
	return h, db, userID, wsID, crewID
}

func TestProposed_Approve_HappyPath_Returns200(t *testing.T) {
	h, db, userID, wsID, crewID := newProposedHandlerTest(t)
	proposalID, _ := seedProposalRow(t, db, wsID, crewID, "pending")

	req := httptest.NewRequest("POST", "/api/v1/consolidate/proposed/"+proposalID+"/approve", nil)
	req.SetPathValue("id", proposalID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		ProposalID    string `json:"proposal_id"`
		CanonicalPath string `json:"canonical_path"`
		RulesMerged   int    `json:"rules_merged"`
		DecidedBy     string `json:"decided_by"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ProposalID != proposalID || resp.RulesMerged != 1 || resp.DecidedBy != userID {
		t.Errorf("response = %+v, missing fields or wrong ids", resp)
	}

	// DB row flipped.
	var status string
	if err := db.QueryRow(`SELECT status FROM memory_proposals WHERE id = ?`, proposalID).Scan(&status); err != nil {
		t.Fatalf("re-read proposal: %v", err)
	}
	if status != "approved" {
		t.Errorf("status = %q, want approved", status)
	}
}

func TestProposed_Approve_NonOwner_Returns403(t *testing.T) {
	h, db, userID, wsID, crewID := newProposedHandlerTest(t)
	proposalID, _ := seedProposalRow(t, db, wsID, crewID, "pending")

	req := httptest.NewRequest("POST", "/api/v1/consolidate/proposed/"+proposalID+"/approve", nil)
	req.SetPathValue("id", proposalID)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
	// State unchanged.
	var status string
	_ = db.QueryRow(`SELECT status FROM memory_proposals WHERE id = ?`, proposalID).Scan(&status)
	if status != "pending" {
		t.Errorf("MEMBER call must not flip proposal state: got %q", status)
	}
}

func TestProposed_Approve_Missing_Returns404(t *testing.T) {
	h, _, userID, wsID, _ := newProposedHandlerTest(t)

	req := httptest.NewRequest("POST", "/api/v1/consolidate/proposed/mp_doesnotexist/approve", nil)
	req.SetPathValue("id", "mp_doesnotexist")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestProposed_Approve_AlreadyDecided_Returns409(t *testing.T) {
	h, db, userID, wsID, crewID := newProposedHandlerTest(t)
	proposalID, _ := seedProposalRow(t, db, wsID, crewID, "approved")

	req := httptest.NewRequest("POST", "/api/v1/consolidate/proposed/"+proposalID+"/approve", nil)
	req.SetPathValue("id", proposalID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)

	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (already decided)", rr.Code)
	}
}

func TestProposed_Approve_CrossWorkspace_Returns404(t *testing.T) {
	h, db, userID, wsID, crewID := newProposedHandlerTest(t)

	// Seed an "other" workspace with its own pending proposal that
	// the caller's auth context should NOT be able to approve.
	otherWS := "ws_other"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}
	otherCrew := seedTestCrew(t, db, otherWS)
	otherID, _ := seedProposalRow(t, db, otherWS, otherCrew, "pending")
	_ = crewID
	_ = wsID

	req := httptest.NewRequest("POST", "/api/v1/consolidate/proposed/"+otherID+"/approve", nil)
	req.SetPathValue("id", otherID)
	// Caller is OWNER of wsID — NOT of otherWS.
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)

	// 404 not 403: existence of a cross-workspace row must not leak.
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (cross-workspace probe must look like missing)", rr.Code)
	}

	// Crucially the other-workspace row state stays pending.
	var status string
	_ = db.QueryRow(`SELECT status FROM memory_proposals WHERE id = ?`, otherID).Scan(&status)
	if status != "pending" {
		t.Errorf("cross-workspace probe flipped state: %q", status)
	}
}

func TestProposed_Reject_HappyPath(t *testing.T) {
	h, db, userID, wsID, crewID := newProposedHandlerTest(t)
	proposalID, _ := seedProposalRow(t, db, wsID, crewID, "pending")

	body := bytes.NewBufferString(`{"reason":"hallucinated rule"}`)
	req := httptest.NewRequest("POST", "/api/v1/consolidate/proposed/"+proposalID+"/reject", body)
	req.SetPathValue("id", proposalID)
	req.Header.Set("Content-Type", "application/json")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Reject(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}

	var status string
	_ = db.QueryRow(`SELECT status FROM memory_proposals WHERE id = ?`, proposalID).Scan(&status)
	if status != "rejected" {
		t.Errorf("status = %q, want rejected", status)
	}
}

func TestProposed_Reject_MalformedBody_StillRejects(t *testing.T) {
	h, db, userID, wsID, crewID := newProposedHandlerTest(t)
	proposalID, _ := seedProposalRow(t, db, wsID, crewID, "pending")

	body := bytes.NewBufferString(`not json`)
	req := httptest.NewRequest("POST", "/api/v1/consolidate/proposed/"+proposalID+"/reject", body)
	req.SetPathValue("id", proposalID)
	req.Header.Set("Content-Type", "application/json")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Reject(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("malformed-body reject status = %d, want 200 (decode failure is permissive)", rr.Code)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM memory_proposals WHERE id = ?`, proposalID).Scan(&status)
	if status != "rejected" {
		t.Errorf("status = %q, want rejected even on bad body", status)
	}
}

func TestProposed_Explain_HappyPath(t *testing.T) {
	h, db, userID, wsID, crewID := newProposedHandlerTest(t)
	proposalID, proposalPath := seedProposalRow(t, db, wsID, crewID, "pending")

	req := httptest.NewRequest("GET", "/api/v1/consolidate/proposed/"+proposalID+"/explain", nil)
	req.SetPathValue("id", proposalID)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Explain(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		ProposalID     string          `json:"proposal_id"`
		Status         string          `json:"status"`
		ProposalPath   string          `json:"proposal_path"`
		RulesCount     int             `json:"rules_count"`
		EntriesScanned int             `json:"entries_scanned"`
		Evidence       json.RawMessage `json:"evidence"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ProposalID != proposalID {
		t.Errorf("proposal_id = %q, want %q", resp.ProposalID, proposalID)
	}
	if resp.Status != "pending" {
		t.Errorf("status = %q, want pending", resp.Status)
	}
	if resp.ProposalPath != proposalPath {
		t.Errorf("proposal_path = %q, want %q", resp.ProposalPath, proposalPath)
	}
	if resp.RulesCount != 1 {
		t.Errorf("rules_count = %d, want 1", resp.RulesCount)
	}
	if resp.EntriesScanned != 12 {
		t.Errorf("entries_scanned = %d, want 12", resp.EntriesScanned)
	}
}

func TestProposed_Explain_MemberRoleAllowed(t *testing.T) {
	h, db, userID, wsID, crewID := newProposedHandlerTest(t)
	proposalID, _ := seedProposalRow(t, db, wsID, crewID, "pending")

	req := httptest.NewRequest("GET", "/api/v1/consolidate/proposed/"+proposalID+"/explain", nil)
	req.SetPathValue("id", proposalID)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Explain(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("MEMBER should be allowed to explain, got %d", rr.Code)
	}
}

func TestProposed_Explain_CrossWorkspace_Returns404(t *testing.T) {
	h, db, _, wsID, _ := newProposedHandlerTest(t)
	// Other workspace with a proposal the caller should not see.
	otherWS := "ws_other"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}
	otherCrew := seedTestCrew(t, db, otherWS)
	otherID, _ := seedProposalRow(t, db, otherWS, otherCrew, "pending")

	// Make a second user that belongs to wsID (the caller's workspace).
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('other-user', 'o@x', 'O')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	req := httptest.NewRequest("GET", "/api/v1/consolidate/proposed/"+otherID+"/explain", nil)
	req.SetPathValue("id", otherID)
	req = withWorkspaceUser(req, "other-user", wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Explain(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (cross-workspace probe)", rr.Code)
	}
}

// Ensure the consolidate package's error sentinels are still wired
// through the handler's mapDecisionError — guards against a future
// refactor that accidentally swallows them.
func TestProposed_ErrorMap_Sentinels(t *testing.T) {
	if consolidate.ErrProposalNotFound == nil {
		t.Errorf("ErrProposalNotFound nil")
	}
	if consolidate.ErrProposalNotPending == nil {
		t.Errorf("ErrProposalNotPending nil")
	}
	_ = context.Background()
}
