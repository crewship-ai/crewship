package api

import (
	"database/sql"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestArtifactSourceChatBackupForkAndChatDeletion(t *testing.T) {
	h, ws, user, _, token := artifactRunFixture(t)
	artifactExpect(t, artifactCreationOptIn(t, h, ws, user, "POST"), 201)
	artifactExpect(t, artifactCreateAgent(t, h, ws, token, creationBody), 201)
	artifactExpect(t, artifactPublishCreated(t, h, ws, token, "agent-created", `{"html":"<h1>Second linked source</h1>","base_rev":1,"mutation_id":"source-chat-second"}`), 201)
	var createdPage string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug='agent-created'`).Scan(&createdPage); err != nil {
		t.Fatal(err)
	}
	seedStorageBoardPin(t, h, ws, user, createdPage, 1)
	rawBoard := string(boardAPIElement(t))
	execOrFatal(t, h.db, `INSERT INTO page_board_elements(board_page_id,element_id,element_rev,element_json,author_user_id,updated_seq,author_agent_id,author_run_id) VALUES('storage-board',json_extract(?,'$.id'),1,?,?,1,'artifact-agent','artifact-run')`, rawBoard, rawBoard, user)
	execOrFatal(t, h.db, `INSERT INTO page_board_mutations(board_page_id,mutation_id,author_user_id,fingerprint,result_seq,created_at,author_agent_id,author_run_id) VALUES('storage-board','agent-board-history',?,'fingerprint',1,'2020-01-01T00:00:00.000Z','artifact-agent','artifact-run')`, user)
	execOrFatal(t, h.db, `INSERT INTO page_board_run_delegations(board_page_id,run_id,granted_by_user_id,granted_at,expires_at) SELECT 'storage-board',run_id,?,'2020-01-01T00:00:00.000Z',expires_at FROM artifact_run_principals WHERE run_id='artifact-run'`, user)
	const pass = "artifact-source-chat-roundtrip-password"
	created, err := backup.CreateBackup(t.Context(), h.db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: pass, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: h.artifactStoragePath})
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range []string{"", "source-chat-fork"} {
		t.Run(fork, func(t *testing.T) {
			target := setupTestDB(t)
			root := storageDir(t)
			if _, err := backup.RestoreBackup(t.Context(), target, backup.RestoreOptions{Path: created.Path, Passphrase: pass, AsWorkspace: fork, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: root}); err != nil {
				t.Fatal(err)
			}
			var pageID, chatID, agentID, targetWS, chatWS string
			var linked int
			if err := target.QueryRow(`SELECT p.id,v.source_chat_id,v.author_agent_id,p.workspace_id,c.workspace_id FROM page_artifact_revisions v JOIN pages p ON p.id=v.page_id JOIN chats c ON c.id=v.source_chat_id WHERE p.slug='agent-created' AND v.revision=1`).Scan(&pageID, &chatID, &agentID, &targetWS, &chatWS); err != nil {
				t.Fatal(err)
			}
			if targetWS != chatWS || chatID == "" || (fork != "" && (chatID == "artifact-chat" || pageID == "" || agentID == "artifact-agent")) {
				t.Fatalf("fork source provenance broken: %s %s %s %s", pageID, chatID, agentID, targetWS)
			}
			if err := target.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=? AND source_chat_id=?`, pageID, chatID).Scan(&linked); err != nil || linked != 2 {
				t.Fatalf("verified creation/publication links lost: %d %v", linked, err)
			}
			if _, err := target.Exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('replacement-source-chat',?,?,?,'private')`, targetWS, agentID, user); err != nil {
				t.Fatal(err)
			}
			if _, err := target.Exec(`UPDATE page_artifact_revisions SET source_chat_id='replacement-source-chat' WHERE page_id=?`, pageID); err == nil {
				t.Fatal("historical source chat reassigned")
			}
			if _, err := target.Exec(`UPDATE page_artifact_revisions SET source_chat_id=NULL WHERE page_id=?`, pageID); err == nil {
				t.Fatal("historical source chat cleared while chat exists")
			}
			if _, err := target.Exec(`DELETE FROM chats WHERE id=?`, chatID); err != nil {
				t.Fatalf("chat deletion could not clear historical link: %v", err)
			}
			var source sql.NullString
			if err := target.QueryRow(`SELECT source_chat_id FROM page_artifact_revisions WHERE page_id=? AND revision=1`, pageID).Scan(&source); err != nil || source.Valid {
				t.Fatalf("deleted chat linkage retained: %v %v", source, err)
			}
			var sha string
			if err := target.QueryRow(`SELECT sha256 FROM page_artifact_revisions WHERE page_id=? AND revision=1`, pageID).Scan(&sha); err != nil {
				t.Fatal(err)
			}
			data, err := readAttachmentBlob(root, targetWS, sha)
			if err != nil || string(data) != "<h1>Created by authorized run</h1>" {
				t.Fatalf("chat deletion lost immutable source bytes: %q %v", data, err)
			}
			var boardHistory int
			if err := target.QueryRow(`SELECT COUNT(*) FROM page_board_elements e JOIN page_board_mutations m ON m.board_page_id=e.board_page_id JOIN page_artifact_creations c ON c.run_id=e.author_run_id WHERE m.author_run_id=e.author_run_id AND e.author_agent_id=? AND m.author_agent_id=?`, agentID, agentID).Scan(&boardHistory); err != nil || boardHistory != 1 {
				t.Fatalf("board agent provenance mapping lost: %d %v", boardHistory, err)
			}
			var boardAuthority int
			if err := target.QueryRow(`SELECT COUNT(*) FROM page_board_run_delegations`).Scan(&boardAuthority); err != nil || boardAuthority != 0 {
				t.Fatalf("restored active board authority: %d %v", boardAuthority, err)
			}
			var authority int
			if err := target.QueryRow(`SELECT COUNT(*) FROM artifact_run_principals`).Scan(&authority); err != nil || authority != 0 {
				t.Fatalf("backup resurrected live authority: %d %v", authority, err)
			}
		})
	}
}

func TestArtifactSourceChatExplicitLinkOverridesRequestHistory(t *testing.T) {
	h, ws, user, page, token := artifactRunFixture(t)
	artifactExpect(t, artifactAgentSave(t, h, ws, token, `{"html":"<h1>Verified source chat</h1>","base_rev":0,"mutation_id":"linked"}`), 201)
	execOrFatal(t, h.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('conflicting-history-chat',?,'artifact-agent',?,'private')`, ws, user)
	execOrFatal(t, h.db, `INSERT INTO page_artifact_requests(id,page_id,human_user_id,chat_id,agent_id,base_rev,submission_id,fingerprint,instruction,snapshot_json,receipt_json,status,run_id,result_revision,created_at,updated_at) VALUES('conflicting-history',?,?,'conflicting-history-chat','artifact-agent',0,'conflicting','fingerprint','historical','{}','{}','succeeded','artifact-run',1,'2020-01-01T00:00:00.000Z','2020-01-01T00:00:00.000Z')`, page, user)
	if got := artifactResults(t, artifactResultsCall(t, h, ws, user, "OWNER", "conflicting-history-chat")); len(got) != 0 {
		t.Fatal("request history overrode verified source chat")
	}
	if got := artifactResults(t, artifactResultsCall(t, h, ws, user, "OWNER", "artifact-chat")); len(got) != 1 {
		t.Fatal("verified source chat lost its publication")
	}
}
