package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

	// 3. The skill now exists in the registry, tagged GENERATED so the
	// catalog UI surfaces it in the Generated tab with the agent-origin badge
	// (not as an indistinguishable manual import).
	var source string
	if err := db.QueryRow(`SELECT source FROM skills WHERE slug = ?`, "deploy-staging").Scan(&source); err != nil {
		t.Fatalf("query skills: %v", err)
	}
	if source != "GENERATED" {
		t.Fatalf("approved agent-authored skill source = %q, want GENERATED", source)
	}
}

// An agent-authored skill must surface in the inbox as a MANAGER-visible
// review item, so a human approves it in the UI (not only via the CLI).
func TestSkillAuthor_SurfacesInboxReviewItem(t *testing.T) {
	h, db, userID, wsID, crewID, _ := newSkillProposedHandlerTest(t, "inbox-crew")
	h.SetCrewMemoryRoot(t.TempDir())

	req := withWorkspaceUser(authorRequestFor(crewID, authoredSkillBody), userID, wsID, "AGENT")
	rr := httptest.NewRecorder()
	h.Author(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("author status = %d, body=%s", rr.Code, rr.Body.String())
	}

	src := "skillprop:" + crewID + ":skill-deploy-staging.md"
	var state, payload, targetRole string
	err := db.QueryRow(
		`SELECT state, payload_json, COALESCE(target_role,'') FROM inbox_items WHERE kind='escalation' AND source_id=?`,
		src).Scan(&state, &payload, &targetRole)
	if err != nil {
		t.Fatalf("inbox review item not created: %v", err)
	}
	if state != "unread" {
		t.Errorf("inbox state = %q, want unread", state)
	}
	if targetRole != "MANAGER" {
		t.Errorf("inbox target_role = %q, want MANAGER", targetRole)
	}
	if !strings.Contains(payload, "skill_proposal") || !strings.Contains(payload, "deploy-staging") {
		t.Errorf("inbox payload missing discriminator/slug: %s", payload)
	}
}

// Approving the proposal must clear its inbox item (whether the approval came
// from the inbox card or the CLI), so it leaves the manager's queue.
func TestSkillAuthor_ApproveResolvesInboxItem(t *testing.T) {
	h, db, userID, wsID, crewID, _ := newSkillProposedHandlerTest(t, "inbox-resolve-crew")
	h.SetCrewMemoryRoot(t.TempDir())

	authReq := withWorkspaceUser(authorRequestFor(crewID, authoredSkillBody), userID, wsID, "AGENT")
	authRR := httptest.NewRecorder()
	h.Author(authRR, authReq)
	if authRR.Code != http.StatusCreated {
		t.Fatalf("author status = %d", authRR.Code)
	}

	approveBody, _ := json.Marshal(map[string]string{"crew_id": crewID, "file_name": "skill-deploy-staging.md"})
	apprReq := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/skills/proposed/approve", bytes.NewReader(approveBody)),
		userID, wsID, "MANAGER")
	apprRR := httptest.NewRecorder()
	h.Approve(apprRR, apprReq)
	if apprRR.Code != http.StatusOK {
		t.Fatalf("approve status = %d, body=%s", apprRR.Code, apprRR.Body.String())
	}

	src := "skillprop:" + crewID + ":skill-deploy-staging.md"
	var state string
	if err := db.QueryRow(`SELECT state FROM inbox_items WHERE kind='escalation' AND source_id=?`, src).Scan(&state); err != nil {
		t.Fatalf("query inbox item: %v", err)
	}
	if state != "resolved" {
		t.Fatalf("inbox item state after approve = %q, want resolved", state)
	}
}
