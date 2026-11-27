package api

import (
	"database/sql"
	"testing"
)

func agentFileState(t *testing.T, db *sql.DB, agent, page string) (exists, removed bool) {
	t.Helper()
	var at sql.NullString
	err := db.QueryRow(`SELECT removed_at FROM agent_files WHERE agent_id=? AND page_id=?`, agent, page).Scan(&at)
	if err == sql.ErrNoRows {
		return false, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true, at.Valid
}

// A page a person attaches to a message stays with the chat's agent.
func TestAttachingAPageKeepsItWithTheAgent(t *testing.T) {
	s := newChatRunScenario(t, true)
	s.attachOnly("sheet-own")
	if exists, removed := agentFileState(t, s.f.rig.db, s.f.a.AgentID, "sheet-own"); !exists || removed {
		t.Fatalf("after attaching: %v %v", exists, removed)
	}
}
