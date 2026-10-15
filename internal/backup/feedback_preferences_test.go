package backup_test

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/usermodel"
)

func TestFeedbackPreferenceForkPreservesSourcesAndTombstones(t *testing.T) {
	db := openMigratedDB(t)
	ws := seedWorkspace(t, db)
	for _, q := range []string{
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('pref-member','ws_e2e_1','u_admin','OWNER')`,
		`UPDATE agents SET memory_enabled=1`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,title) VALUES('pref-chat','ws_e2e_1','a_alice','u_admin','Private')`,
		`INSERT INTO conversation_messages(id,session_id,agent_id,role,content) VALUES('pref-msg','pref-chat','a_alice','assistant','Result')`,
		`INSERT INTO message_feedback(id,workspace_id,chat_id,message_id,user_id,signal,reason) VALUES('pref-saved','ws_e2e_1','pref-chat','pref-msg','u_admin','helpful','Next time, keep answers concise.')`,
		`INSERT INTO message_feedback(id,workspace_id,chat_id,message_id,user_id,signal,reason) VALUES('pref-withdrawn','ws_e2e_1','pref-chat','pref-msg','u_admin','not_helpful','Next time, start with the conclusion.')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := usermodel.ProcessFeedbackPreferences(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE agent_user_preferences SET status='withdrawn',value='',reason='',revision=revision+1 WHERE id='pref-withdrawn'`); err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(t.Context(), db, ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := backup.RemapIDs(t.Context(), db, dump); err != nil {
		t.Fatal(err)
	}
	fork := dump.Tables["workspaces"][0]["id"].(string)
	dump.Tables["workspaces"][0]["slug"] = "preference-fork"
	if err := backup.RestoreDump(t.Context(), db, dump); err != nil {
		t.Fatal(err)
	}
	var total, withdrawn, linked int
	if err := db.QueryRow(`SELECT count(*),sum(status='withdrawn') FROM agent_user_preferences WHERE workspace_id=?`, fork).Scan(&total, &withdrawn); err != nil || total != 2 || withdrawn != 1 {
		t.Fatalf("restore recreated a candidate or lost tombstone: %d/%d %v", total, withdrawn, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM agent_user_preferences p JOIN message_feedback f ON f.id=p.feedback_id JOIN chats c ON c.id=p.chat_id WHERE p.workspace_id=? AND f.workspace_id=? AND c.workspace_id=?`, fork, fork, fork).Scan(&linked); err != nil || linked != 2 {
		t.Fatalf("source references did not follow fork: %d %v", linked, err)
	}
	var agent string
	if err := db.QueryRow(`SELECT agent_id FROM agent_user_preferences WHERE workspace_id=? LIMIT 1`, fork).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	body, err := usermodel.ReadFeedbackPreferences(t.Context(), db, fork, agent, "u_admin")
	if err != nil || body != "" {
		t.Fatalf("unrestored mirror did not suppress recall: %q %v", body, err)
	}
	assertNoFKViolations(t, db, "preference fork")
}
