package api

import (
	"context"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// rebuildFake records EnqueueForCrew calls from the agent handlers.
type rebuildFake struct{ calls []string }

func (f *rebuildFake) EnqueueForCrew(_ context.Context, crewID, _ string) (EnqueueResult, error) {
	f.calls = append(f.calls, crewID)
	return EnqueueResult{}, nil
}

func setCrewImage(t *testing.T, rig *covACRig, cachedImage, requirements string) {
	t.Helper()
	if _, err := rig.db.Exec(`UPDATE crews SET cached_image = ?, cached_requirements = ? WHERE id = ?`,
		nullTextIfEmpty(cachedImage), nullTextIfEmpty(requirements), rig.crewID); err != nil {
		t.Fatalf("set crew image: %v", err)
	}
}

func nullTextIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func createAgentWith(t *testing.T, rig *covACRig, slug, adapter string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"name":"` + slug + `","slug":"` + slug + `","crew_id":"` + rig.crewID + `","agent_role":"AGENT","cli_adapter":"` + adapter + `"}`
	rec := httptest.NewRecorder()
	rig.h.Create(rec, rig.req(t, "OWNER", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: %d %s", slug, rec.Code, rec.Body.String())
	}
	return rec
}

func TestCrewAgentAdapters(t *testing.T) {
	rig := newCovACRig(t)
	if got, err := crewAgentAdapters(context.Background(), rig.db, rig.crewID); err != nil || len(got) != 0 {
		t.Fatalf("empty crew: got %v err %v", got, err)
	}
	createAgentWith(t, rig, "a1", "CODEX_CLI")
	createAgentWith(t, rig, "a2", "CLAUDE_CODE")
	createAgentWith(t, rig, "a3", "CLAUDE_CODE")
	got, err := crewAgentAdapters(context.Background(), rig.db, rig.crewID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "CLAUDE_CODE,CODEX_CLI" {
		t.Errorf("adapters = %v, want distinct and sorted", got)
	}
}

func TestEnsureCrewImageHasAdapter(t *testing.T) {
	tests := []struct {
		name         string
		cachedImage  string
		requirements string
		adapter      string
		wantRebuild  bool
	}{
		{"no image yet: the first build reads the agents", "", "", "CLAUDE_CODE", false},
		{"image verified for the adapter", "crewship-cache:abc", `{"adapterBinaries":["claude"]}`, "CLAUDE_CODE", false},
		{"image verified for another adapter", "crewship-cache:abc", `{"adapterBinaries":["claude"]}`, "CODEX_CLI", true},
		{"image built before verification existed", "crewship-cache:abc", `{"loginPath":"/usr/bin"}`, "CLAUDE_CODE", true},
		{"image with unreadable requirements", "crewship-cache:abc", `{not json`, "CLAUDE_CODE", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newCovACRig(t)
			fake := &rebuildFake{}
			rig.h.SetProvisioner(fake)
			setCrewImage(t, rig, tc.cachedImage, tc.requirements)
			createAgentWith(t, rig, "agent-x", tc.adapter)
			if got := len(fake.calls) > 0; got != tc.wantRebuild {
				t.Errorf("rebuild enqueued = %v, want %v (calls %v)", got, tc.wantRebuild, fake.calls)
			}
			if tc.wantRebuild && fake.calls[0] != rig.crewID {
				t.Errorf("rebuild enqueued for %q, want %q", fake.calls[0], rig.crewID)
			}
		})
	}

	t.Run("no provisioner wired: create still succeeds", func(t *testing.T) {
		rig := newCovACRig(t)
		setCrewImage(t, rig, "crewship-cache:abc", `{"adapterBinaries":["claude"]}`)
		createAgentWith(t, rig, "agent-y", "CODEX_CLI")
	})
}

// Changing an agent's adapter onto one the image was not verified for
// rebuilds the crew; changing something else does not.
func TestUpdateAgentAdapterRebuilds(t *testing.T) {
	rig := newCovACRig(t)
	fake := &rebuildFake{}
	rig.h.SetProvisioner(fake)
	setCrewImage(t, rig, "crewship-cache:abc", `{"adapterBinaries":["claude"]}`)
	createAgentWith(t, rig, "agent-z", "CLAUDE_CODE")
	if len(fake.calls) != 0 {
		t.Fatalf("covered adapter must not rebuild: %v", fake.calls)
	}
	var agentID string
	if err := rig.db.QueryRow(`SELECT id FROM agents WHERE slug = 'agent-z'`).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	patch := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/agents/"+agentID, strings.NewReader(body))
		req.SetPathValue("agentId", agentID)
		req = withWorkspaceUser(req, rig.userID, rig.wsID, "OWNER")
		rec := httptest.NewRecorder()
		rig.h.Update(rec, req)
		return rec
	}
	if rec := patch(`{"description":"renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if len(fake.calls) != 0 {
		t.Errorf("a description change must not rebuild: %v", fake.calls)
	}
	if rec := patch(`{"cli_adapter":"GEMINI_CLI"}`); rec.Code != http.StatusOK {
		t.Fatalf("update adapter: %d %s", rec.Code, rec.Body.String())
	}
	if len(fake.calls) != 1 || fake.calls[0] != rig.crewID {
		t.Errorf("adapter change must rebuild the crew once: %v", fake.calls)
	}
}

// Every adapter the handlers accept must have an install recipe in the
// devcontainer catalogue — a fifth adapter added to validCLIAdapters alone
// would be accepted and never installed.
func TestValidCLIAdaptersHaveInstallRecipes(t *testing.T) {
	for adapter := range validCLIAdapters {
		if _, ok := devcontainer.AdapterCLIFor(adapter); !ok {
			t.Errorf("%s is accepted by the agent handlers but has no adapter CLI recipe", adapter)
		}
	}
}

// crewImageReady is the dispatch gate's truth table.
func TestCrewImageReady(t *testing.T) {
	present := func(string) bool { return true }
	absent := func(string) bool { return false }
	tests := []struct {
		name         string
		cfg          string // devcontainer_config column ("" = NULL → read-time default)
		agents       []string
		cachedImage  string
		reqs         string
		imagePresent func(string) bool
		wantNeeds    bool
		wantReady    bool
	}{
		{"no agents, bare image, no customisation", `{"image":"debian"}`, nil, "", "", present, false, true},
		{"plain image but an agent needs claude: build required", `{"image":"debian"}`, []string{"CLAUDE_CODE"}, "", "", present, true, false},
		{"image built and verified", `{"image":"debian"}`, []string{"CLAUDE_CODE"}, "crewship-cache:a", `{"adapterBinaries":["claude"]}`, present, true, true},
		{"image built for claude, codex agent added later", `{"image":"debian"}`, []string{"CLAUDE_CODE", "CODEX_CLI"}, "crewship-cache:a", `{"adapterBinaries":["claude"]}`, present, true, false},
		{"image from before verification", `{"image":"debian"}`, []string{"CLAUDE_CODE"}, "crewship-cache:a", `{"loginPath":"/usr/bin"}`, present, true, false},
		{"image gone from the daemon", `{"image":"debian"}`, []string{"CLAUDE_CODE"}, "crewship-cache:a", `{"adapterBinaries":["claude"]}`, absent, true, false},
		{"NULL config defaults to the claude-code image and still needs verification", "", []string{"CLAUDE_CODE"}, "crewship-cache:a", "", present, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newCovACRig(t)
			if tc.cfg != "" {
				if _, err := rig.db.Exec(`UPDATE crews SET devcontainer_config = ? WHERE id = ?`, tc.cfg, rig.crewID); err != nil {
					t.Fatal(err)
				}
			}
			setCrewImage(t, rig, tc.cachedImage, tc.reqs)
			for i, a := range tc.agents {
				createAgentWith(t, rig, "ag"+string(rune('a'+i)), a)
			}
			needs, ready, _, err := crewImageReady(context.Background(), rig.db, rig.crewID, rig.wsID, tc.imagePresent)
			if err != nil {
				t.Fatal(err)
			}
			if needs != tc.wantNeeds || ready != tc.wantReady {
				t.Errorf("needsBuild=%v ready=%v, want %v/%v", needs, ready, tc.wantNeeds, tc.wantReady)
			}
		})
	}
}
