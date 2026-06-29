package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

const authoredSkillBody = `{"content":"---\nname: deploy-staging\ndescription: Use when deploying the app to the staging environment.\ncategory: DEVOPS\n---\n# Deploy to staging\n\n## When to Use\n- Shipping a build to staging.\n\n## Procedure\n1. Build the image.\n2. Push and roll.\n"}`

func authorRequestFor(crewID, body string) *http.Request {
	return httptest.NewRequest("POST", "/api/v1/internal/skills/author?crew_id="+crewID, bytes.NewBufferString(body))
}

func TestSkillAuthor_StagesSkillForReview(t *testing.T) {
	h, _, userID, wsID, crewID, slug := newSkillProposedHandlerTest(t, "author-crew")
	root := t.TempDir()
	h.SetCrewMemoryRoot(root)

	req := withWorkspaceUser(authorRequestFor(crewID, authoredSkillBody), userID, wsID, "AGENT")
	rr := httptest.NewRecorder()
	h.Author(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		FileName   string `json:"file_name"`
		Slug       string `json:"slug"`
		ScanStatus string `json:"scan_status"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Slug != "deploy-staging" {
		t.Errorf("slug = %q, want deploy-staging", got.Slug)
	}
	if got.ScanStatus != "CLEAN" {
		t.Errorf("scan_status = %q, want CLEAN", got.ScanStatus)
	}
	// The skill must land in the crew's .proposed directory, where the
	// existing review/approve flow already looks.
	staged := filepath.Join(root, slug, "topics", ".proposed", "skill-deploy-staging.md")
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("expected staged file at %s: %v", staged, err)
	}
}

func TestSkillAuthor_MissingContent_400(t *testing.T) {
	h, _, userID, wsID, crewID, _ := newSkillProposedHandlerTest(t, "empty-author-crew")
	h.SetCrewMemoryRoot(t.TempDir())

	req := withWorkspaceUser(authorRequestFor(crewID, `{"content":""}`), userID, wsID, "AGENT")
	rr := httptest.NewRecorder()
	h.Author(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestSkillAuthor_UnknownCrew_404(t *testing.T) {
	h, _, userID, wsID, _, _ := newSkillProposedHandlerTest(t, "real-crew")
	h.SetCrewMemoryRoot(t.TempDir())

	req := withWorkspaceUser(authorRequestFor("crew_does_not_exist", authoredSkillBody), userID, wsID, "AGENT")
	rr := httptest.NewRecorder()
	h.Author(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// The whole point of staging into .proposed is that the existing approve flow
// promotes an agent-authored skill into the live registry with no special
// casing. This exercises author -> approve end to end.
func TestSkillAuthor_StagedSkillIsApprovable(t *testing.T) {
	h, db, userID, wsID, crewID, _ := newSkillProposedHandlerTest(t, "promote-crew")
	h.SetCrewMemoryRoot(t.TempDir())

	// 1. Agent authors the skill (staged).
	authReq := withWorkspaceUser(authorRequestFor(crewID, authoredSkillBody), userID, wsID, "AGENT")
	authRR := httptest.NewRecorder()
	h.Author(authRR, authReq)
	if authRR.Code != http.StatusCreated {
		t.Fatalf("author status = %d, body=%s", authRR.Code, authRR.Body.String())
	}
	var authored struct {
		FileName string `json:"file_name"`
	}
	if err := json.Unmarshal(authRR.Body.Bytes(), &authored); err != nil {
		t.Fatalf("decode author: %v", err)
	}

	// 2. A manager approves it -> imported into the live skills registry.
	approveBody, _ := json.Marshal(map[string]string{"crew_id": crewID, "file_name": authored.FileName})
	apprReq := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/skills/proposed/approve", bytes.NewReader(approveBody)),
		userID, wsID, "MANAGER")
	apprRR := httptest.NewRecorder()
	h.Approve(apprRR, apprReq)
	if apprRR.Code != http.StatusOK {
		t.Fatalf("approve status = %d, body=%s", apprRR.Code, apprRR.Body.String())
	}

	// 3. The skill now exists in the registry.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM skills WHERE slug = ?`, "deploy-staging").Scan(&count); err != nil {
		t.Fatalf("query skills: %v", err)
	}
	if count != 1 {
		t.Fatalf("approved skill not in registry: count = %d", count)
	}
}
