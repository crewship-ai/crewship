package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/skills"
)

func TestSkillGitPolicySnapshotsBootAuthority(t *testing.T) {
	policy := skills.GitImportPolicy{PrivateHosts: []string{"git.internal.example"}}
	option := WithSkillGitImportPolicy(policy)
	policy.PrivateHosts[0] = "attacker.example"
	router := &Router{}
	option(router)
	if router.skillGitImportPolicy.PrivateHosts[0] != "git.internal.example" {
		t.Fatal("router policy followed mutable caller slice")
	}
	handler := NewSkillBulkImportHandler(nil, newTestLogger(), router.skillGitImportPolicy)
	router.skillGitImportPolicy.PrivateHosts[0] = "other.example"
	if handler.gitPolicy.PrivateHosts[0] != "git.internal.example" {
		t.Fatal("handler policy followed mutable router slice")
	}
}

func TestSkillGitRequestCannotExpandPrivateHosts(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	handler := NewSkillBulkImportHandler(db, newTestLogger())
	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"git_url":"https://127.0.0.1/repo.git","private_hosts":["127.0.0.1"],"git_policy":{"private_hosts":["127.0.0.1"]}}`))
	req.SetPathValue("workspaceId", ws)
	req = withWorkspaceUser(req, user, ws, "OWNER")
	response := httptest.NewRecorder()
	handler.Import(response, req)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("request changed private-host authority: status=%d body=%s", response.Code, response.Body.String())
	}
	if len(handler.gitPolicy.PrivateHosts) != 0 {
		t.Fatal("request mutated boot policy")
	}
}
