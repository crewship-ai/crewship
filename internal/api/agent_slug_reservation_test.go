package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func slugReq(t *testing.T, method, path, userID, wsID string, body map[string]any) *http.Request {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	return req.WithContext(ctx)
}

func slugFixture(t *testing.T) (*AgentHandler, string, string) {
	t.Helper()
	h, userID, wsID := newAgentHandlerForQueryTest(t)
	seedCrewRow(t, h.db, "crew-a", wsID, "A", "a")
	seedCrewRow(t, h.db, "crew-b", wsID, "B", "b")
	return h, userID, wsID
}

func createAgent(t *testing.T, h *AgentHandler, userID, wsID, crew, slug string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.Create(rr, slugReq(t, "POST", "/api/v1/agents", userID, wsID, map[string]any{
		"name": strings.ToUpper(slug), "slug": slug, "crew_id": crew, "cli_adapter": "CLAUDE_CODE",
	}))
	return rr
}

func deleteAgent(t *testing.T, h *AgentHandler, userID, wsID, id string) {
	t.Helper()
	req := httptest.NewRequest("DELETE", "/api/v1/agents/"+id, nil)
	req.SetPathValue("agentId", id)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete %s: %d %s", id, rr.Code, rr.Body.String())
	}
}

func patchAgent(t *testing.T, h *AgentHandler, userID, wsID, id string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	req := slugReq(t, "PATCH", "/api/v1/agents/"+id, userID, wsID, body)
	req.SetPathValue("agentId", id)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	return rr
}

// A09 interim: a new agent must not take a slug whose memory and output
// still belong to a deleted agent of the same crew. Another crew is a
// different data root, so the slug stays free there.
func TestAgentSlugReservation_DeleteThenRecreate(t *testing.T) {
	h, userID, wsID := slugFixture(t)
	seedAgentRow(t, h.db, "ag-eng", wsID, "crew-a", "Eng", "eng", "AGENT")
	deleteAgent(t, h, userID, wsID, "ag-eng")

	if rr := createAgent(t, h, userID, wsID, "crew-a", "eng"); rr.Code != http.StatusConflict {
		t.Fatalf("same crew recreate = %d %s, want 409", rr.Code, rr.Body.String())
	}
	if rr := createAgent(t, h, userID, wsID, "crew-b", "eng"); rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("other crew create = %d %s", rr.Code, rr.Body.String())
	}
}

// Renaming or moving an agent leaves its data under the old slug of the old
// crew; nobody else may take that slug there. The agent itself may go back.
func TestAgentSlugReservation_RenameAndMove(t *testing.T) {
	h, userID, wsID := slugFixture(t)
	seedAgentRow(t, h.db, "ag-r", wsID, "crew-a", "R", "rena", "AGENT")
	seedAgentRow(t, h.db, "ag-m", wsID, "crew-a", "M", "mover", "AGENT")

	if rr := patchAgent(t, h, userID, wsID, "ag-r", map[string]any{"slug": "renb"}); rr.Code != http.StatusOK {
		t.Fatalf("rename = %d %s", rr.Code, rr.Body.String())
	}
	if rr := createAgent(t, h, userID, wsID, "crew-a", "rena"); rr.Code != http.StatusConflict {
		t.Fatalf("take renamed-away slug = %d, want 409", rr.Code)
	}
	if rr := patchAgent(t, h, userID, wsID, "ag-r", map[string]any{"slug": "rena"}); rr.Code != http.StatusOK {
		t.Fatalf("rename back by the same agent = %d %s", rr.Code, rr.Body.String())
	}

	if rr := patchAgent(t, h, userID, wsID, "ag-m", map[string]any{"crew_id": "crew-b"}); rr.Code != http.StatusOK {
		t.Fatalf("move = %d %s", rr.Code, rr.Body.String())
	}
	seedAgentRow(t, h.db, "ag-other", wsID, "crew-a", "O", "other", "AGENT")
	if rr := patchAgent(t, h, userID, wsID, "ag-other", map[string]any{"slug": "mover"}); rr.Code != http.StatusConflict {
		t.Fatalf("rename onto moved-away slug = %d, want 409", rr.Code)
	}
}

// Every insert path is covered by the trigger, not only the create handler
// (hire, templates, manifests and onboarding insert agents directly).
func TestAgentSlugReservation_TriggerGuardsDirectInserts(t *testing.T) {
	h, userID, wsID := slugFixture(t)
	seedAgentRow(t, h.db, "ag-x", wsID, "crew-a", "X", "xslug", "AGENT")
	deleteAgent(t, h, userID, wsID, "ag-x")
	if _, err := h.db.Exec(`UPDATE agents SET slug = slug || '_deleted_' || id WHERE id = 'ag-x'`); err != nil {
		t.Fatalf("tombstone release must stay possible: %v", err)
	}
	_, err := h.db.Exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag-y', 'crew-a', ?, 'Y', 'xslug')`, wsID)
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("direct insert of a reserved slug: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag-z', 'crew-b', ?, 'Z', 'xslug')`, wsID); err != nil {
		t.Fatalf("other crew insert: %v", err)
	}
}

// The migration reserves slugs of existing tombstones, including ones already
// released by rename, but never assigns legacy data to a live holder.
func TestAgentSlugReservation_MigrationBackfill(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE workspaces (id TEXT PRIMARY KEY); CREATE TABLE crews (id TEXT PRIMARY KEY);
INSERT INTO workspaces VALUES('w'); INSERT INTO crews VALUES('c1');
CREATE TABLE agents (id TEXT PRIMARY KEY, crew_id TEXT, workspace_id TEXT, name TEXT, slug TEXT, deleted_at TEXT);
INSERT INTO agents VALUES
 ('t1','c1','w','T1','kept','2026-09-01'),
 ('t2','c1','w','T2','freed_deleted_t2','2026-09-02'),
 ('t3','c1','w','T3','taken_deleted_t3','2026-09-03'),
 ('live','c1','w','L','taken',NULL)`); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../database/migrations/20261001180000_agent_slug_reservations.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(b)); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	rows, _ := db.Query(`SELECT slug, agent_id FROM agent_slug_reservations`)
	for rows.Next() {
		var s, a string
		_ = rows.Scan(&s, &a)
		got[s] = a
	}
	rows.Close()
	if got["kept"] != "t1" || got["freed"] != "t2" {
		t.Errorf("tombstone slugs not reserved: %v", got)
	}
	if _, ok := got["taken"]; ok {
		t.Errorf("ambiguous legacy slug assigned: %v", got)
	}
}

// #4840: the agent-scope memory ledger key is workspace-wide
// ("agent:<slug>/<file>"), the file lives under the agent's crew. A new agent
// taking a deleted agent's slug in another crew inherited that agent's
// revision anchors, and every memory.write of the new agent failed the drift
// check ("edited outside the memory contract"). Freeing the slug moves the
// anchors with the tombstone; unrelated slugs stay untouched.
func TestAgentSlugReuse_MovesDeletedAgentMemoryAnchors(t *testing.T) {
	for _, via := range []string{"create", "rename"} {
		t.Run(via, func(t *testing.T) {
			h, userID, wsID := slugFixture(t)
			seedAgentRow(t, h.db, "ag-old", wsID, "crew-a", "Eng", "e_g", "AGENT")
			anchor := func(path string) {
				t.Helper()
				scope := path[:strings.Index(path, "/")]
				if _, err := h.db.Exec(`INSERT INTO memory_revisions (workspace_id, path, scope, tier, revision, content_sha256, bytes, last_mutation_id, updated_at)
					VALUES (?, ?, ?, 'agent', 1, 'c95f0e', 3, 'mm_x', '2026-11-02T17:51:01Z')`, wsID, path, scope); err != nil {
					t.Fatal(err)
				}
			}
			anchor("agent:e_g/AGENT.md")
			anchor("agent:e_g/pins.md")
			anchor("agent:exg/AGENT.md")  // '_' must not match as a wildcard
			anchor("agent:e_g2/AGENT.md") // a longer slug sharing the prefix
			deleteAgent(t, h, userID, wsID, "ag-old")

			switch via {
			case "create":
				if rr := createAgent(t, h, userID, wsID, "crew-b", "e_g"); rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
					t.Fatalf("other crew create = %d %s", rr.Code, rr.Body.String())
				}
			case "rename":
				seedAgentRow(t, h.db, "ag-new", wsID, "crew-b", "New", "fresh", "AGENT")
				if rr := patchAgent(t, h, userID, wsID, "ag-new", map[string]any{"slug": "e_g"}); rr.Code != http.StatusOK {
					t.Fatalf("rename onto freed slug = %d %s", rr.Code, rr.Body.String())
				}
			}

			got := map[string]string{}
			rows, err := h.db.Query(`SELECT path, scope FROM memory_revisions WHERE workspace_id = ? ORDER BY path`, wsID)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for rows.Next() {
				var path, scope string
				if err := rows.Scan(&path, &scope); err != nil {
					t.Fatal(err)
				}
				got[path] = scope
			}
			want := map[string]string{
				"agent:e_g_deleted_ag-old/AGENT.md": "agent:e_g_deleted_ag-old",
				"agent:e_g_deleted_ag-old/pins.md":  "agent:e_g_deleted_ag-old",
				"agent:exg/AGENT.md":                "agent:exg",
				"agent:e_g2/AGENT.md":               "agent:e_g2",
			}
			if len(got) != len(want) {
				t.Fatalf("anchors = %v, want %v", got, want)
			}
			for p, s := range want {
				if got[p] != s {
					t.Fatalf("anchors = %v, want %v", got, want)
				}
			}
			var tomb string
			if err := h.db.QueryRow(`SELECT slug FROM agents WHERE id = 'ag-old'`).Scan(&tomb); err != nil || tomb != "e_g_deleted_ag-old" {
				t.Fatalf("tombstone slug = %q %v", tomb, err)
			}
		})
	}
}
