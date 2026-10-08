package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// #3032: the skills catalog is global, assignments are per workspace. Every
// agent-derived field the skills endpoints return must only see the caller's
// workspace, and usage/credential readiness come from the same scope.

type skillScopeFixture struct {
	db           *sql.DB
	h            *SkillHandler
	userID       string
	wsA, wsB     string
	agentA       string
	agentB       string
	skillID      string
	otherSkillID string
}

func newSkillScopeFixture(t *testing.T) skillScopeFixture {
	t.Helper()
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsA := seedTestWorkspace(t, db, userID)
	wsB := "ws-skill-scope-b"
	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other-skill-scope')`, wsB)
	execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-b', ?, ?, 'OWNER')`, wsB, userID)
	seedCrewRow(t, db, "crew-a", wsA, "Crew A", "crew-a")
	seedCrewRow(t, db, "crew-b", wsB, "Crew B", "crew-b")
	agentA := seedAgentRow(t, db, "agent-a", wsA, "crew-a", "Ava", "ava", "AGENT")
	agentB := seedAgentRow(t, db, "agent-b", wsB, "crew-b", "Ben", "ben", "AGENT")

	execOrFatal(t, db, `INSERT INTO skills (id, name, slug, display_name, version, category, source, verification,
		downloads, rating_count, pricing_tier, featured, tags, content, credential_requirements)
		VALUES ('sk-scope', 'scope', 'scope', 'Scope', '1.0.0', 'CODING', 'CUSTOM', 'UNVERIFIED', 0, 0, 'FREE', 0, '[]', '# s',
		'["GH_TOKEN","OTHER_TOKEN"]')`)
	execOrFatal(t, db, `INSERT INTO skills (id, name, slug, display_name, version, category, source, verification,
		downloads, rating_count, pricing_tier, featured, tags, content)
		VALUES ('sk-other', 'other', 'other', 'Other', '1.0.0', 'CODING', 'CUSTOM', 'UNVERIFIED', 0, 0, 'FREE', 0, '[]', '# o')`)
	// sk-scope is held only by workspace A's agent; sk-other only by B's.
	execOrFatal(t, db, `INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES ('as-a', ?, 'sk-scope', 1)`, agentA)
	execOrFatal(t, db, `INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES ('as-b', ?, 'sk-other', 1)`, agentB)

	// Agent A is delivered GH_TOKEN but not OTHER_TOKEN.
	enc, err := encryption.Encrypt("ghp-test")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	execOrFatal(t, db, `INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope,
		security_level, status, created_by) VALUES ('cred-gh', ?, 'GH_TOKEN', ?, 'CLI_TOKEN', 'NONE', 'WORKSPACE', 1, 'ACTIVE', ?)`,
		wsA, enc, userID)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('grant-gh', ?, 'cred-gh', 'GH_TOKEN', 0, datetime('now'))`, agentA)

	// Invocations: A has two recent (one failed) and one old; B has one.
	execOrFatal(t, db, `INSERT INTO skill_invocations (id, skill_id, agent_id, workspace_id, invoked_at, exit_code) VALUES
		('inv-1', 'sk-scope', ?, ?, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-1 hours'), 0),
		('inv-2', 'sk-scope', ?, ?, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-2 days'), 1),
		('inv-3', 'sk-scope', ?, ?, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-30 days'), 0),
		('inv-4', 'sk-scope', ?, ?, strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-1 hours'), 0)`,
		agentA, wsA, agentA, wsA, agentA, wsA, agentB, wsB)

	return skillScopeFixture{db: db, h: NewSkillHandler(db, newTestLogger()), userID: userID,
		wsA: wsA, wsB: wsB, agentA: agentA, agentB: agentB, skillID: "sk-scope", otherSkillID: "sk-other"}
}

func (f skillScopeFixture) list(t *testing.T, wsID, query string) map[string]skillResponse {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/skills"+query, nil)
	req = withWorkspaceUser(req, f.userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	f.h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list %s = %d: %s", query, rr.Code, rr.Body.String())
	}
	var rows []skillResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out := map[string]skillResponse{}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}

func (f skillScopeFixture) get(t *testing.T, wsID, skillID string) skillDetailResponse {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/skills/"+skillID, nil)
	req.SetPathValue("skillId", skillID)
	req = withWorkspaceUser(req, f.userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	f.h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get = %d: %s", rr.Code, rr.Body.String())
	}
	var d skillDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return d
}

func TestSkillsList_AgentDataScopedToWorkspace(t *testing.T) {
	f := newSkillScopeFixture(t)
	tests := []struct {
		name        string
		ws          string
		query       string
		wantPresent []string
		wantAbsent  []string
		wantAgents  map[string][]string // skill → agent ids in installed_on
	}{
		{name: "browse from A sees only A's agent", ws: f.wsA, wantPresent: []string{f.skillID, f.otherSkillID},
			wantAgents: map[string][]string{f.skillID: {f.agentA}, f.otherSkillID: nil}},
		{name: "browse from B sees only B's agent", ws: f.wsB, wantPresent: []string{f.skillID, f.otherSkillID},
			wantAgents: map[string][]string{f.skillID: nil, f.otherSkillID: {f.agentB}}},
		{name: "installed=1 from B excludes A's skill", ws: f.wsB, query: "?installed=1",
			wantPresent: []string{f.otherSkillID}, wantAbsent: []string{f.skillID}},
		{name: "installed=1 from A excludes B's skill", ws: f.wsA, query: "?installed=1",
			wantPresent: []string{f.skillID}, wantAbsent: []string{f.otherSkillID}},
		{name: "installed_for_agent_id of another workspace's agent returns nothing", ws: f.wsB,
			query: "?installed_for_agent_id=" + "agent-a", wantAbsent: []string{f.skillID, f.otherSkillID}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := f.list(t, tc.ws, tc.query)
			for _, id := range tc.wantPresent {
				if _, ok := got[id]; !ok {
					t.Errorf("%s missing", id)
				}
			}
			for _, id := range tc.wantAbsent {
				if _, ok := got[id]; ok {
					t.Errorf("%s present, want absent", id)
				}
			}
			for skill, want := range tc.wantAgents {
				var ids []string
				for _, a := range got[skill].InstalledOn {
					ids = append(ids, a.AgentID)
				}
				if len(ids) != len(want) || (len(want) == 1 && ids[0] != want[0]) {
					t.Errorf("%s installed_on = %v, want %v", skill, ids, want)
				}
			}
		})
	}
}

func TestSkillGet_AgentCountScopedToWorkspace(t *testing.T) {
	f := newSkillScopeFixture(t)
	tests := []struct {
		name      string
		ws        string
		wantCount int
	}{
		{"owner workspace counts its agent", f.wsA, 1},
		{"other workspace counts none", f.wsB, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := f.get(t, tc.ws, f.skillID)
			if d.AgentCount != tc.wantCount {
				t.Errorf("agent_count = %d, want %d", d.AgentCount, tc.wantCount)
			}
			if len(d.InstalledOn) != tc.wantCount {
				t.Errorf("installed_on = %d entries, want %d", len(d.InstalledOn), tc.wantCount)
			}
		})
	}
}

func TestSkills_UsageAndCredentialReadiness(t *testing.T) {
	f := newSkillScopeFixture(t)
	tests := []struct {
		name        string
		ws          string
		wantUses7d  int
		wantErrors  int
		wantTotal   int
		wantLastSet bool
	}{
		{"workspace A counts its own invocations", f.wsA, 2, 1, 3, true},
		{"workspace B counts only B's invocation", f.wsB, 1, 0, 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for label, u := range map[string]skillUsage{
				"list":   f.list(t, tc.ws, "")[f.skillID].Usage,
				"detail": f.get(t, tc.ws, f.skillID).Usage,
			} {
				if u.Uses7d != tc.wantUses7d || u.Errors7d != tc.wantErrors || u.UsesTotal != tc.wantTotal {
					t.Errorf("%s usage = %+v, want 7d=%d errors=%d total=%d", label, u, tc.wantUses7d, tc.wantErrors, tc.wantTotal)
				}
				if (u.LastUsedAt != nil) != tc.wantLastSet {
					t.Errorf("%s last_used_at = %v", label, u.LastUsedAt)
				}
			}
		})
	}

	t.Run("needs and missing credentials", func(t *testing.T) {
		row := f.list(t, f.wsA, "")[f.skillID]
		if len(row.NeedsCredentials) != 2 {
			t.Fatalf("needs_credentials = %v", row.NeedsCredentials)
		}
		if len(row.InstalledOn) != 1 {
			t.Fatalf("installed_on = %+v", row.InstalledOn)
		}
		if m := row.InstalledOn[0].MissingCredentials; len(m) != 1 || m[0] != "OTHER_TOKEN" {
			t.Errorf("missing_credentials = %v, want [OTHER_TOKEN]", m)
		}
		other := f.list(t, f.wsB, "")[f.otherSkillID]
		if other.NeedsCredentials == nil || len(other.NeedsCredentials) != 0 {
			t.Errorf("needs_credentials for a skill without requirements = %#v, want []", other.NeedsCredentials)
		}
	})

	t.Run("agent skills list carries readiness", func(t *testing.T) {
		h := NewAgentHandler(f.db, newTestLogger())
		req := httptest.NewRequest("GET", "/api/v1/agents/"+f.agentA+"/skills", nil)
		req.SetPathValue("agentId", f.agentA)
		req = withWorkspaceUser(req, f.userID, f.wsA, "MEMBER")
		rr := httptest.NewRecorder()
		h.ListSkills(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("list = %d: %s", rr.Code, rr.Body.String())
		}
		var rows []agentSkillResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(rows) != 1 || len(rows[0].MissingCredentials) != 1 || rows[0].MissingCredentials[0] != "OTHER_TOKEN" {
			t.Errorf("rows = %+v", rows)
		}
	})
}
