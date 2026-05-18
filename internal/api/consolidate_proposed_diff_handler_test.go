package api

// Diff-endpoint coverage for the HITL memory proposal flow.
//
// The contract under test:
//
//   1. GET /diff returns 200 with a unified diff that, when
//      conceptually "applied", would produce exactly what an
//      Approve on the same proposal would write to the canonical
//      learned-*.md file. The byte-equality check between the
//      diff's post-merge half and a real ApproveProposal's output
//      is the load-bearing assertion — drift between the two is
//      the bug class that would erode operator trust in the HITL
//      UI.
//
//   2. Cross-workspace probes get 404, not 403 (mirrors Explain).
//
//   3. Missing markdown on disk (proposal row outlived its
//      .proposed file) surfaces as 410 Gone — distinct from 404
//      so the operator knows the row exists but the artefact is
//      gone. Recoverable via re-running the consolidator.
//
//   4. The first-time-canonical case (no learned-*.md yet) and
//      the append case (learned-*.md already exists) produce
//      different diff prefixes — both verified.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/journal"
)

func diffDecode(t *testing.T, rr *httptest.ResponseRecorder) proposalDiffResponse {
	t.Helper()
	var resp proposalDiffResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode diff response: %v; body=%s", err, rr.Body.String())
	}
	return resp
}

func TestProposed_Diff_HappyPath_FirstTimeCanonical_Returns200(t *testing.T) {
	h, db, userID, wsID, crewID := newProposedHandlerTest(t)
	proposalID, proposalPath := seedProposalRow(t, db, wsID, crewID, "pending")

	req := httptest.NewRequest("GET", "/api/v1/consolidate/proposed/"+proposalID+"/diff", nil)
	req.SetPathValue("id", proposalID)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Diff(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	resp := diffDecode(t, rr)

	if resp.ProposalID != proposalID || resp.WorkspaceID != wsID || resp.CrewID != crewID {
		t.Errorf("identity fields wrong: got %+v", resp)
	}
	if resp.Status != "pending" {
		t.Errorf("status = %q, want pending (the row was seeded pending)", resp.Status)
	}
	if resp.CanonicalExists {
		t.Errorf("canonical_exists = true on a brand-new fixture; want false")
	}
	if resp.ProposalPath != proposalPath {
		t.Errorf("proposal_path = %q, want %q", resp.ProposalPath, proposalPath)
	}
	expectedCanonical := filepath.Join(filepath.Dir(filepath.Dir(proposalPath)),
		"learned-"+time.Now().UTC().Format("2006-01-02")+".md")
	if resp.CanonicalPath != expectedCanonical {
		t.Errorf("canonical_path = %q, want %q", resp.CanonicalPath, expectedCanonical)
	}
	if resp.RulesCount != 1 || resp.Stats.RulesAppended != 1 {
		t.Errorf("rules_count = %d / rules_appended = %d; want 1/1 (the fixture seeds 1 rule)",
			resp.RulesCount, resp.Stats.RulesAppended)
	}
	if resp.Stats.Additions <= 0 {
		t.Errorf("additions = %d; want >0 for a non-empty merge", resp.Stats.Additions)
	}
	if resp.Stats.Deletions != 0 {
		t.Errorf("deletions = %d; want 0 (append-only merge)", resp.Stats.Deletions)
	}
	// The diff body has to be a real unified diff — the chrome
	// headers are the signal we got difflib output rather than
	// e.g. a json error.
	if !strings.Contains(resp.Diff, "--- canonical (current)") ||
		!strings.Contains(resp.Diff, "+++ canonical (post-merge)") {
		t.Errorf("diff missing unified header; got:\n%s", resp.Diff)
	}
	// First-time canonical: the diff body must include the
	// auto-generated header line so the operator sees the file
	// would be brand new.
	if !strings.Contains(resp.Diff, "Learned rules") {
		t.Errorf("first-time diff missing the file header; got:\n%s", resp.Diff)
	}
}

func TestProposed_Diff_AppendCase_DiffShowsAppendOnly(t *testing.T) {
	// When a canonical file already exists, the merge appends a
	// new block prefixed by "\n---\n\n" + the "Approved at"
	// header. The diff stats should still report deletions=0,
	// additions=>0, and the diff body should contain the divider.
	h, db, userID, wsID, crewID := newProposedHandlerTest(t)
	proposalID, proposalPath := seedProposalRow(t, db, wsID, crewID, "pending")

	// Stage a canonical file so the append branch fires. Path
	// derivation matches CanonicalPathForProposal — the helper is
	// the contract, so we call it directly rather than rebuilding
	// the path here.
	canonicalPath := consolidate.CanonicalPathForProposal(proposalPath, time.Now().UTC())
	if err := os.WriteFile(canonicalPath, []byte("# Learned rules — pre-existing\n\nbody\n"), 0o644); err != nil {
		t.Fatalf("seed canonical: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/consolidate/proposed/"+proposalID+"/diff", nil)
	req.SetPathValue("id", proposalID)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Diff(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	resp := diffDecode(t, rr)

	if !resp.CanonicalExists {
		t.Errorf("canonical_exists = false; want true (we seeded it)")
	}
	if resp.Stats.Deletions != 0 {
		t.Errorf("deletions = %d; want 0 even in the append branch", resp.Stats.Deletions)
	}
	if !strings.Contains(resp.Diff, "---\n") {
		// The "\n---\n\n" divider from BuildCanonicalAppendBlock
		// must show up in the diff so the operator can see the
		// section boundary.
		t.Errorf("append diff missing the section divider; got:\n%s", resp.Diff)
	}
}

func TestProposed_Diff_ByteEqualToApprove_NoDriftBetweenPreviewAndWrite(t *testing.T) {
	// This is the contract that matters: the post-merge half of
	// the diff must be byte-identical to what an Approve on the
	// same proposal would land on disk. Drift between preview
	// and write erodes operator trust in the HITL UI in the most
	// damaging way (silent disagreement).
	//
	// Strategy:
	//   1. Build the "post-merge" bytes the diff would render
	//      via the same helpers the handler uses.
	//   2. Approve the proposal — which writes the canonical
	//      file.
	//   3. Assert the file on disk equals the simulated bytes.
	//
	// Because the handler captures `now` from time.Now() and so
	// does ApproveProposal, the date strings only match when both
	// run within the same UTC day — which is always true for any
	// non-pathological CI run. The "Approved at" timestamp uses
	// 15:04:05 MST format, which CAN differ second-to-second; we
	// pin equality on the DATE-bearing prefix (header + divider +
	// rules body) and accept that the time portion is
	// preview-only.
	_, db, userID, wsID, crewID := newProposedHandlerTest(t)
	proposalID, proposalPath := seedProposalRow(t, db, wsID, crewID, "pending")

	// Compute what the diff WOULD render — using the same path
	// helpers the handler uses.
	now := time.Now().UTC()
	canonicalPath := consolidate.CanonicalPathForProposal(proposalPath, now)
	rulesBody, err := os.ReadFile(proposalPath)
	if err != nil {
		t.Fatalf("read proposal body: %v", err)
	}
	previewBlock := consolidate.BuildCanonicalAppendBlock(false, now,
		consolidate.ExtractProposalRulesBody(string(rulesBody)))

	// Now actually Approve.
	jw := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = jw.Close() })
	_, err = consolidate.ApproveProposal(context.Background(), db, jw, newTestLogger(), proposalID, userID,
		consolidate.ApprovalOptions{})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}

	got, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("read canonical after approve: %v", err)
	}

	// Equality on the header + date + rules body, ignoring the
	// "Approved at HH:MM:SS MST" line which is captured at the
	// instant of each call (preview and approve race the wall
	// clock for that one line).
	want := stripApprovedAtLine(previewBlock)
	have := stripApprovedAtLine(string(got))
	if want != have {
		t.Fatalf("preview vs. approve byte mismatch (excluding 'Approved at' line)\n--want--\n%s\n--have--\n%s",
			want, have)
	}
}

func TestProposed_Diff_CrossWorkspace_Returns404NotLeaky(t *testing.T) {
	h, db, _, wsID, crewID := newProposedHandlerTest(t)
	proposalID, _ := seedProposalRow(t, db, wsID, crewID, "pending")

	// A user authed into a different workspace asking about
	// `proposalID` (which belongs to wsID) MUST get 404, not
	// 403 — 403 would leak the existence of the row across
	// tenants. seedTestUser/Workspace use fixed IDs, so we
	// inline a second tenant with explicit unique IDs here.
	otherUserID := "test-other-user-id"
	otherEmail := "other@example.com"
	otherWS := "test-other-workspace-id"
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, 'Other User')`,
		otherUserID, otherEmail); err != nil {
		t.Fatalf("insert other user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`,
		otherWS); err != nil {
		t.Fatalf("insert other workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m_other', ?, ?, 'OWNER')`,
		otherWS, otherUserID); err != nil {
		t.Fatalf("insert other member: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/consolidate/proposed/"+proposalID+"/diff", nil)
	req.SetPathValue("id", proposalID)
	req = withWorkspaceUser(req, otherUserID, otherWS, "OWNER")
	rr := httptest.NewRecorder()
	h.Diff(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (cross-workspace probe)", rr.Code)
	}
}

func TestProposed_Diff_MissingProposalMarkdown_Returns410Gone(t *testing.T) {
	// Seeded the row, then nuke the markdown on disk to simulate
	// out-of-band deletion (container rebuild, restore from a
	// backup that pre-dates this row, manual cleanup, etc.).
	h, db, userID, wsID, crewID := newProposedHandlerTest(t)
	proposalID, proposalPath := seedProposalRow(t, db, wsID, crewID, "pending")
	if err := os.Remove(proposalPath); err != nil {
		t.Fatalf("nuke proposal markdown: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/consolidate/proposed/"+proposalID+"/diff", nil)
	req.SetPathValue("id", proposalID)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Diff(rr, req)

	if rr.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410 (proposal row exists, markdown missing)", rr.Code)
	}
}

func TestProposed_Diff_MissingWorkspace_Returns401(t *testing.T) {
	h, _, _, _, _ := newProposedHandlerTest(t)
	// No workspace context wired — middleware-bypass / mis-wired
	// router case.
	req := httptest.NewRequest("GET", "/api/v1/consolidate/proposed/some-id/diff", nil)
	req.SetPathValue("id", "some-id")
	rr := httptest.NewRecorder()
	h.Diff(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no workspace context)", rr.Code)
	}
}

func TestProposed_Diff_MissingProposalID_Returns400(t *testing.T) {
	h, _, userID, wsID, _ := newProposedHandlerTest(t)
	req := httptest.NewRequest("GET", "/api/v1/consolidate/proposed//diff", nil)
	// Deliberately do NOT SetPathValue("id", ...) — simulates a
	// router that passed the request through without binding the
	// {id} variable. Handler must 400.
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Diff(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (empty proposal id)", rr.Code)
	}
}

func TestProposed_Diff_UnknownProposalID_Returns404(t *testing.T) {
	h, _, userID, wsID, _ := newProposedHandlerTest(t)
	req := httptest.NewRequest("GET", "/api/v1/consolidate/proposed/mp_no_such/diff", nil)
	req.SetPathValue("id", "mp_no_such")
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Diff(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (unknown id)", rr.Code)
	}
}

// ── helpers ──────────────────────────────────────────────────────────

// stripApprovedAtLine removes the "## Approved at HH:MM:SS MST"
// line from a learned-*.md block so the byte-equality test can
// compare everything else without racing the wall clock.
func stripApprovedAtLine(s string) string {
	out := make([]string, 0, 16)
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "## Approved at ") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
