package backup

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// newRemapTestDB builds a target DB whose schema mirrors the
// foreign-key edges RemapIDs is meant to follow. The data is
// irrelevant to RemapIDs (it only introspects PRAGMA
// foreign_key_list); we just need the FK declarations to live in
// the schema so the introspection returns non-empty edges.
func newRemapTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := t.TempDir() + "/remap.db"
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`
		CREATE TABLE workspaces (id TEXT PRIMARY KEY, slug TEXT);
		CREATE TABLE crews (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id),
			slug TEXT
		);
		CREATE TABLE agents (
			id TEXT PRIMARY KEY,
			crew_id TEXT NOT NULL REFERENCES crews(id),
			name TEXT
		);
		CREATE TABLE skills (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id),
			name TEXT
		);
		CREATE TABLE chats (
			id TEXT PRIMARY KEY,
			agent_id TEXT NOT NULL REFERENCES agents(id),
			body TEXT
		);
	`)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestRemapIDs_RewritesPKsAndFKs(t *testing.T) {
	db := newRemapTestDB(t)
	dump := &DBDump{
		WorkspaceID: "ws_old",
		Tables: map[string][]map[string]any{
			"workspaces": {
				{"id": "ws_old", "slug": "old"},
			},
			"crews": {
				{"id": "crew_a", "workspace_id": "ws_old", "slug": "a"},
				{"id": "crew_b", "workspace_id": "ws_old", "slug": "b"},
			},
			"agents": {
				{"id": "agent_1", "crew_id": "crew_a", "name": "Alice"},
				{"id": "agent_2", "crew_id": "crew_b", "name": "Bob"},
			},
			"skills": {
				{"id": "skill_1", "workspace_id": "ws_old", "name": "git"},
			},
			"chats": {
				{"id": "chat_1", "agent_id": "agent_1", "body": "hi"},
			},
		},
	}

	if err := RemapIDs(context.Background(), db, dump); err != nil {
		t.Fatalf("RemapIDs: %v", err)
	}

	// Workspace got a new id.
	wsRow := dump.Tables["workspaces"][0]
	newWsID, _ := wsRow["id"].(string)
	if newWsID == "" || newWsID == "ws_old" {
		t.Errorf("workspace id not regenerated: %v", newWsID)
	}

	// Crews got new ids and their workspace_id points at the new ws.
	for _, c := range dump.Tables["crews"] {
		id, _ := c["id"].(string)
		ws, _ := c["workspace_id"].(string)
		if id == "" || id == "crew_a" || id == "crew_b" {
			t.Errorf("crew id not regenerated: %v", id)
		}
		if ws != newWsID {
			t.Errorf("crew.workspace_id not rewritten: got %q want %q", ws, newWsID)
		}
	}

	// Agents got new ids and their crew_id points at the (new) crew ids.
	crewIDByOld := map[string]string{}
	for _, c := range dump.Tables["crews"] {
		// Reverse-derive: we only know the new ids in dump now, but we
		// recorded slug → new id implicitly by using the slug column.
		// Use slug as the stable handle to verify FK rewrite.
		crewIDByOld[c["slug"].(string)] = c["id"].(string)
	}
	for _, a := range dump.Tables["agents"] {
		id, _ := a["id"].(string)
		ck, _ := a["crew_id"].(string)
		if id == "" || id == "agent_1" || id == "agent_2" {
			t.Errorf("agent id not regenerated: %v", id)
		}
		// crew_a's agent should now point at the new id of crew with slug a.
		switch a["name"].(string) {
		case "Alice":
			if ck != crewIDByOld["a"] {
				t.Errorf("Alice agent_id not rewritten correctly: got %q want %q", ck, crewIDByOld["a"])
			}
		case "Bob":
			if ck != crewIDByOld["b"] {
				t.Errorf("Bob agent_id not rewritten correctly: got %q want %q", ck, crewIDByOld["b"])
			}
		}
	}

	// Skills.workspace_id points at the new ws.
	for _, s := range dump.Tables["skills"] {
		ws, _ := s["workspace_id"].(string)
		if ws != newWsID {
			t.Errorf("skill.workspace_id not rewritten: got %q want %q", ws, newWsID)
		}
	}

	// Agent_chats.agent_id points at the new agent id.
	newAgentIDs := map[string]string{}
	for _, a := range dump.Tables["agents"] {
		newAgentIDs[a["name"].(string)] = a["id"].(string)
	}
	chat := dump.Tables["chats"][0]
	got, _ := chat["agent_id"].(string)
	if got != newAgentIDs["Alice"] {
		t.Errorf("chats.agent_id not rewritten: got %q want %q", got, newAgentIDs["Alice"])
	}
}

func TestRemapIDs_NilDumpNoop(t *testing.T) {
	db := newRemapTestDB(t)
	if err := RemapIDs(context.Background(), db, nil); err != nil {
		t.Errorf("nil dump should be no-op, got %v", err)
	}
}

func TestRemapIDs_EmptyTablesNoop(t *testing.T) {
	db := newRemapTestDB(t)
	dump := &DBDump{Tables: map[string][]map[string]any{}}
	if err := RemapIDs(context.Background(), db, dump); err != nil {
		t.Errorf("empty dump should be no-op, got %v", err)
	}
}

func TestRemapIDs_UnmappedFKLeftAlone(t *testing.T) {
	// agents.crew_id whose value is not in the map (because crews
	// table was empty in the dump) keeps its old value rather than
	// being silently dropped.
	db := newRemapTestDB(t)
	dump := &DBDump{
		Tables: map[string][]map[string]any{
			"agents": {
				{"id": "agent_x", "crew_id": "crew_unknown", "name": "X"},
			},
		},
	}
	if err := RemapIDs(context.Background(), db, dump); err != nil {
		t.Fatalf("RemapIDs: %v", err)
	}
	a := dump.Tables["agents"][0]
	if a["crew_id"] != "crew_unknown" {
		t.Errorf("unmapped FK should be left alone, got %v", a["crew_id"])
	}
	if a["id"] == "agent_x" {
		t.Errorf("agent id still should be regenerated, got %v", a["id"])
	}
}
