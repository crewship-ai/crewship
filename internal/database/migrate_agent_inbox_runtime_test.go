package database

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// Read actual index prefixes through SQLite. The general legacy policy helper
// also treats later composite-PK columns as indexed, which cannot prove that
// agent_id has a usable leading index when workspace_id is unknown.
func TestAgentInboxRuntimeForeignKeysHaveLeadingIndexes(t *testing.T) {
	db := migrateChainSetup(t)
	for _, table := range []string{"agent_inbox_receipts", "agent_inbox_run_sources"} {
		for _, column := range []string{"workspace_id", "agent_id"} {
			t.Run(table+"."+column, func(t *testing.T) {
				var n int
				err := db.QueryRow(`SELECT COUNT(*) FROM pragma_index_list(?) AS ix JOIN pragma_index_info(ix.name) AS col ON col.seqno=0 WHERE col.name=? AND ix.partial=0`, table, column).Scan(&n)
				if err != nil {
					t.Fatal(err)
				}
				if n == 0 {
					t.Errorf("%s.%s has no real leading non-partial index", table, column)
				}
			})
		}
	}
}

func TestAgentInboxRuntimeForeignKeysCascadeDeletedParents(t *testing.T) {
	db := migrateChainSetup(t)
	for _, table := range []string{"agent_inbox_receipts", "agent_inbox_run_sources"} {
		for _, column := range []string{"workspace_id", "agent_id"} {
			t.Run(table+"."+column, func(t *testing.T) {
				var parent, action string
				if err := db.QueryRow(`SELECT "table",on_delete FROM pragma_foreign_key_list(?) WHERE "from"=?`, table, column).Scan(&parent, &action); err != nil {
					t.Fatal(err)
				}
				expected := "workspaces"
				if column == "agent_id" {
					expected = "agents"
				}
				if parent != expected || action != "CASCADE" {
					t.Errorf("%s.%s references %s ON DELETE %s, want %s CASCADE", table, column, parent, action, expected)
				}
			})
		}
	}
}

func TestAgentInboxUpgradeKeepsPendingQueuedAndRunningJobs(t *testing.T) {
	db, err := Open("file:" + filepath.Join(t.TempDir(), "legacy-agent-jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := applyMigrationsUpTo(t.Context(), db.DB, 20261117211048, logger); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('legacy-ws','Legacy','legacy')`,
		`INSERT INTO users(id,email) VALUES('legacy-user','legacy@example.test')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('legacy-crew','legacy-ws','Legacy','legacy')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('legacy-agent','legacy-ws','legacy-crew','Legacy','legacy')`,
		`INSERT INTO chats(id,workspace_id,agent_id) VALUES('legacy-chat','legacy-ws','legacy-agent')`,
		`INSERT INTO workspace_conversations(id,workspace_id,kind,title,created_at,updated_at)
		 VALUES('legacy-channel','legacy-ws','channel','Legacy','2026-11-17','2026-11-17')`,
		`INSERT INTO workspace_conversation_messages(id,conversation_id,sequence,author_user_id,client_id,content,created_at)
		 VALUES('pending-message','legacy-channel',1,'legacy-user','pending','pending','2026-11-17'),
		       ('queued-message','legacy-channel',2,'legacy-user','queued','queued','2026-11-17'),
		       ('running-message','legacy-channel',3,'legacy-user','running','running','2026-11-17')`,
		`INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status)
		 VALUES('queued-assignment','legacy-ws','legacy-chat','legacy-agent','legacy-agent','queued','QUEUED'),
		       ('running-assignment','legacy-ws','legacy-chat','legacy-agent','legacy-agent','running','RUNNING')`,
		`INSERT INTO workspace_conversation_agent_jobs(id,conversation_id,message_id,agent_id,requested_by_user_id,state,assignment_id,created_at,updated_at)
		 VALUES('pending-job','legacy-channel','pending-message','legacy-agent','legacy-user','pending',NULL,'2026-11-17','2026-11-17'),
		       ('queued-job','legacy-channel','queued-message','legacy-agent','legacy-user','queued','queued-assignment','2026-11-17','2026-11-17'),
		       ('running-job','legacy-channel','running-message','legacy-agent','legacy-user','queued','running-assignment','2026-11-17','2026-11-17')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(t.Context(), db.DB, logger); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"pending-job": "pending", "queued-job": "QUEUED", "running-job": "RUNNING"} {
		var state string
		var legacy bool
		err := db.QueryRow(`SELECT COALESCE(a.status,j.state), j.read_authority_json IS NULL AND j.read_source_sha256 IS NULL
			FROM workspace_conversation_agent_jobs j LEFT JOIN assignments a ON a.id=j.assignment_id WHERE j.id=?`, id).Scan(&state, &legacy)
		if err != nil || state != want || !legacy {
			t.Fatalf("%s state=%q want=%q legacy=%v err=%v", id, state, want, legacy, err)
		}
	}
}
