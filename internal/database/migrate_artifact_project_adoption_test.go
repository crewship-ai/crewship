package database

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestArtifactProjectMigrationsAdoptOriginalPilotWithoutChangingData(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "pilot-adoption.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err = ensureMigrationsTable(t.Context(), db.DB); err != nil {
		t.Fatal(err)
	}
	// Apply the real pre-pilot registry, rather than deleting ledger rows while
	// leaving the new schema installed. Original pilot SQL follows verbatim.
	var baseline []migration
	for _, m := range migrations {
		if m.version != 20261027175100 && m.version != 20261027175101 {
			baseline = append(baseline, m)
		}
	}
	if err = applyRegistry(t.Context(), db.DB, baseline, logger); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, old := range []struct {
		file, name string
		version    int
	}{
		{"pilot_20261027100940_artifact_project_comments.sql", "artifact_project_comments", 20261027100940},
		{"pilot_20261027101000_artifact_project_run_consents.sql", "artifact_project_run_consents", 20261027101000},
	} {
		sql, err := os.ReadFile(filepath.Join("testdata", old.file))
		if err != nil {
			t.Fatal(err)
		}
		exec(string(sql))
		exec(`INSERT INTO _migrations(version,name) VALUES(?,?)`, old.version, old.name)
	}
	exec(`INSERT INTO users(id,email,full_name) VALUES('pilot-user','pilot@example.test','Pilot')`)
	exec(`INSERT INTO workspaces(id,name,slug) VALUES('pilot-ws','Pilot','pilot-adoption')`)
	exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('pilot-crew','pilot-ws','Crew','pilot')`)
	exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('pilot-agent','pilot-ws','pilot-crew','Agent','pilot')`)
	exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('pilot-chat','pilot-ws','pilot-agent','pilot-user','private')`)
	exec(`INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts,author_user_id) VALUES('pilot-message','pilot-chat','pilot-agent','user','Original instruction',datetime('now'),'pilot-user')`)
	exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES('pilot-page','pilot-ws','pilot-project','Existing project','pilot-user','{}',1)`)
	exec(`INSERT INTO artifact_version_heads(workspace_id,page_id,latest_version,shared_version) VALUES('pilot-ws','pilot-page',1,1)`)
	exec(`INSERT INTO artifact_versions(workspace_id,page_id,version,manifest_json,actor,created_at) VALUES('pilot-ws','pilot-page',1,'{"entry_point":"index.html","files":{}}','pilot-user',datetime('now'))`)
	exec(`INSERT INTO artifact_version_blobs(workspace_id,page_id,hash,content) VALUES('pilot-ws','pilot-page','original-bytes',x'00ff80')`)
	exec(`INSERT INTO artifact_project_comments(id,workspace_id,page_id,version,path,selector,quote,text,author,created_at) VALUES('pilot-comment','pilot-ws','pilot-page',1,'index.html','#hero','Original title','Change title','pilot-user',datetime('now'))`)
	exec(`INSERT INTO artifact_project_comment_tasks(id,comment_id,workspace_id,page_id,base_version,status,active_run_id,created_at) VALUES('pilot-task','pilot-comment','pilot-ws','pilot-page',1,'working','pilot-run',datetime('now'))`)
	exec(`INSERT INTO artifact_project_comment_replies(id,comment_id,version,text,author,created_at) VALUES('pilot-reply','pilot-comment',1,'Original reply','pilot-agent',datetime('now'))`)
	exec(`INSERT INTO artifact_project_origins(page_id,workspace_id,human_user_id,chat_id,agent_id,message_id,created_at) VALUES('pilot-page','pilot-ws','pilot-user','pilot-chat','pilot-agent','pilot-message',datetime('now'))`)
	exec(`INSERT INTO artifact_run_principals(run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at) SELECT 'pilot-run','pilot-ws','pilot-agent','pilot-crew','pilot-user','pilot-chat','pilot-message','pilot-member',1,authority_generation,authority_revision,'2099-01-01T00:00:00Z' FROM chats WHERE id='pilot-chat'`)
	exec(`INSERT INTO artifact_project_run_consents(run_id,workspace_id,human_user_id,chat_id,message_id,source_kind,expires_at,result_page_id,created_at) VALUES('pilot-run','pilot-ws','pilot-user','pilot-chat','pilot-message','chat','2099-01-01T00:00:00Z','pilot-page',datetime('now'))`)
	exec(`INSERT INTO artifact_project_message_intents(message_id,workspace_id,human_user_id,chat_id,submission_id,page_id,comment_id,base_version,run_id,created_at) VALUES('pilot-message','pilot-ws','pilot-user','pilot-chat','pilot-submission','pilot-page','pilot-comment',1,'pilot-run',datetime('now'))`)
	exec(`UPDATE feature_flags SET enabled=1 WHERE key='artifact_projects'`)
	snapshot := func() map[string]string {
		t.Helper()
		result := map[string]string{}
		for _, table := range []string{"artifact_version_heads", "artifact_versions", "artifact_version_blobs", "artifact_project_comments", "artifact_project_comment_tasks", "artifact_project_comment_replies", "artifact_project_origins", "artifact_project_run_consents", "artifact_project_message_intents", "feature_flags"} {
			rows, err := db.QueryContext(t.Context(), "SELECT * FROM "+table+" ORDER BY 1")
			if err != nil {
				t.Fatal(err)
			}
			columns, err := rows.Columns()
			if err != nil {
				t.Fatal(err)
			}
			var all [][]any
			for rows.Next() {
				values := make([]any, len(columns))
				pointers := make([]any, len(columns))
				for i := range values {
					pointers[i] = &values[i]
				}
				if err = rows.Scan(pointers...); err != nil {
					t.Fatal(err)
				}
				all = append(all, values)
			}
			if err = rows.Err(); err != nil {
				t.Fatal(err)
			}
			_ = rows.Close()
			encoded, err := json.Marshal(all)
			if err != nil {
				t.Fatal(err)
			}
			result[table] = string(encoded)
		}
		return result
	}
	before := snapshot()
	for pass := 1; pass <= 2; pass++ {
		if err = Migrate(t.Context(), db.DB, logger); err != nil {
			t.Fatalf("adopt original pilot, pass%d: %v", pass, err)
		}
		if after := snapshot(); !reflect.DeepEqual(before, after) {
			t.Fatalf("pilot content/permission changed on pass%d: before%v after%v", pass, before, after)
		}
	}
	var recorded int
	if err = db.QueryRow(`SELECT count(*) FROM _migrations WHERE version IN (20261027100940,20261027101000,20261027175100,20261027175101)`).Scan(&recorded); err != nil || recorded != 4 {
		t.Fatalf("pilot/adoption ledger changed: %d: %v", recorded, err)
	}
}
