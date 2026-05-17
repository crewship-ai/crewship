package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// stagedSkillFixture writes a parseable SKILL.md under the crew's
// .proposed directory. Returns (root, file name) so the test can build
// approve/reject request bodies without re-deriving the layout.
func stagedSkillFixture(t *testing.T, crewSlug, name string) (string, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, crewSlug, "topics", ".proposed")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir staged: %v", err)
	}
	body := "---\n" +
		"name: " + name + "\n" +
		"description: Use when the user asks about staged skill testing (auto-promoted memory rule)\n" +
		"category: CUSTOM\n" +
		"runtime: INSTRUCTIONS\n" +
		"maturity: EXPERIMENTAL\n" +
		"---\n\n" +
		"# Staged skill body for tests\n"
	fileName := "skill-" + name + ".md"
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(body), 0o644); err != nil {
		t.Fatalf("write staged: %v", err)
	}
	return root, fileName
}

// seedCrewWithSlug inserts a crew whose slug is predictable so the
// handler's slug lookup hits exactly the directory the fixture wrote.
func seedCrewWithSlug(t *testing.T, db *sql.DB, workspaceID, slug string) string {
	t.Helper()
	crewID := "crew_" + slug
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, ?, ?)`,
		crewID, workspaceID, "Crew "+slug, slug); err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	return crewID
}

func newSkillProposedHandlerTest(t *testing.T, crewSlug string) (*SkillProposedHandler, *sql.DB, string, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewWithSlug(t, db, wsID, crewSlug)
	h := NewSkillProposedHandler(db, newTestLogger())
	return h, db, userID, wsID, crewID, crewSlug
}

func TestSkillProposed_List_HappyPath(t *testing.T) {
	h, _, userID, wsID, crewID, slug := newSkillProposedHandlerTest(t, "alpha-crew")
	root, _ := stagedSkillFixture(t, slug, "rule-one")
	stagedSkillFixture(t, slug, "rule-two") // second file under same dir; t.TempDir dedupe — re-use root
	// stagedSkillFixture builds its own root; write a second file into
	// the same crew dir by hand.
	dir := filepath.Join(root, slug, "topics", ".proposed")
	body := "---\nname: rule-two\ndescription: Use when something else is encountered in the codebase\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "skill-rule-two.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	h.SetCrewMemoryRoot(root)

	req := httptest.NewRequest("GET", "/api/v1/skills/proposed?crew_id="+crewID, nil)
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var got []ProposedSkillSummary
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 staged skills, got %d: %+v", len(got), got)
	}
	if got[0].FileName != "skill-rule-one.md" || got[1].FileName != "skill-rule-two.md" {
		t.Errorf("expected sorted by filename, got %v / %v", got[0].FileName, got[1].FileName)
	}
	if got[0].Name != "rule-one" {
		t.Errorf("name parse miss: %+v", got[0])
	}
}

func TestSkillProposed_List_EmptyCrew_ReturnsEmptyArray(t *testing.T) {
	h, _, userID, wsID, crewID, _ := newSkillProposedHandlerTest(t, "no-skills-crew")
	h.SetCrewMemoryRoot(t.TempDir()) // root exists but crew dir doesn't

	req := httptest.NewRequest("GET", "/api/v1/skills/proposed?crew_id="+crewID, nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Body.String(); got != "[]\n" {
		t.Errorf("body = %q, want empty array", got)
	}
}

func TestSkillProposed_List_NonManager_Returns403(t *testing.T) {
	h, _, userID, wsID, crewID, _ := newSkillProposedHandlerTest(t, "rbac-crew")
	h.SetCrewMemoryRoot(t.TempDir())

	req := httptest.NewRequest("GET", "/api/v1/skills/proposed?crew_id="+crewID, nil)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestSkillProposed_Approve_HappyPath_ImportsAndDeletesFile(t *testing.T) {
	h, db, userID, wsID, crewID, slug := newSkillProposedHandlerTest(t, "approve-crew")
	root, fileName := stagedSkillFixture(t, slug, "approval-test")
	h.SetCrewMemoryRoot(root)

	bodyBytes, _ := json.Marshal(approveBody{CrewID: crewID, FileName: fileName})
	req := httptest.NewRequest("POST", "/api/v1/skills/proposed/approve", bytes.NewReader(bodyBytes))
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		SkillID  string `json:"skill_id"`
		FileName string `json:"file_name"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.SkillID == "" || resp.FileName != fileName {
		t.Errorf("response = %+v", resp)
	}

	// Staging file removed.
	if _, err := os.Stat(filepath.Join(root, slug, "topics", ".proposed", fileName)); !os.IsNotExist(err) {
		t.Errorf("staging file should be removed; stat err=%v", err)
	}
	// Imported skill row exists.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM skills WHERE id = ?`, resp.SkillID).Scan(&count); err != nil {
		t.Fatalf("count skills: %v", err)
	}
	if count != 1 {
		t.Errorf("imported skill row count = %d, want 1", count)
	}
}

func TestSkillProposed_Approve_Missing_Returns404(t *testing.T) {
	h, _, userID, wsID, crewID, _ := newSkillProposedHandlerTest(t, "missing-crew")
	h.SetCrewMemoryRoot(t.TempDir())

	bodyBytes, _ := json.Marshal(approveBody{CrewID: crewID, FileName: "skill-does-not-exist.md"})
	req := httptest.NewRequest("POST", "/api/v1/skills/proposed/approve", bytes.NewReader(bodyBytes))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
}

func TestSkillProposed_Approve_PathTraversal_Rejected(t *testing.T) {
	h, _, userID, wsID, crewID, _ := newSkillProposedHandlerTest(t, "traversal-crew")
	h.SetCrewMemoryRoot(t.TempDir())

	bodyBytes, _ := json.Marshal(approveBody{CrewID: crewID, FileName: "../../../etc/passwd"})
	req := httptest.NewRequest("POST", "/api/v1/skills/proposed/approve", bytes.NewReader(bodyBytes))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("path traversal must 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSkillProposed_Reject_DeletesFile(t *testing.T) {
	h, _, userID, wsID, crewID, slug := newSkillProposedHandlerTest(t, "reject-crew")
	root, fileName := stagedSkillFixture(t, slug, "to-reject")
	h.SetCrewMemoryRoot(root)

	bodyBytes, _ := json.Marshal(approveBody{CrewID: crewID, FileName: fileName})
	req := httptest.NewRequest("POST", "/api/v1/skills/proposed/reject", bytes.NewReader(bodyBytes))
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.Reject(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, slug, "topics", ".proposed", fileName)); !os.IsNotExist(err) {
		t.Errorf("reject must delete the file; stat err=%v", err)
	}
}

func TestSkillProposed_Reject_AlreadyGone_Idempotent(t *testing.T) {
	h, _, userID, wsID, crewID, _ := newSkillProposedHandlerTest(t, "idem-crew")
	h.SetCrewMemoryRoot(t.TempDir())

	bodyBytes, _ := json.Marshal(approveBody{CrewID: crewID, FileName: "skill-gone.md"})
	req := httptest.NewRequest("POST", "/api/v1/skills/proposed/reject", bytes.NewReader(bodyBytes))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Reject(rr, req)

	// Idempotent reject — gone is still 200 with removed:false.
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Removed bool `json:"removed"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Removed {
		t.Errorf("removed=true on missing file; want false")
	}
}
