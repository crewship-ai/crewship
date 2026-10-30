package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/journal"
)

func TestArtifactProjectCommentTaskRunOwnershipAndRecovery(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	f := artifactProjectOriginalRunFixture(t)
	token := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "artifact-task-owner")
	human := func(body string, slug string, handler func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		r := pagesRequest(t, "POST", "/api/v1/pages/"+slug, f.a.WorkspaceID, f.a.HumanUserID, "OWNER", body)
		r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindCLIToken))
		r.Header.Set("Authorization", "Bearer "+token)
		r.SetPathValue("slug", slug)
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	create := `{"slug":"task-project","name":"Task project",` + strings.TrimPrefix(artifactProjectBody(0, "First"), "{")
	if w := human(create, "task-project", f.pages.CreateArtifactProject); w.Code != 201 {
		t.Fatalf("project %d %s", w.Code, w.Body)
	}
	comment := human(`{"version":1,"path":"index.html","selector":"#hero","quote":"First","text":"Change the heading","send_to_agent":true}`, "task-project", f.pages.CreateArtifactProjectComment)
	if comment.Code != 201 {
		t.Fatalf("comment %d %s", comment.Code, comment.Body)
	}
	var c struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(comment.Body.Bytes(), &c) != nil || c.ID == "" {
		t.Fatal("comment ID absent")
	}
	var page string
	if f.rig.db.QueryRow(`SELECT id FROM pages WHERE slug='task-project'`).Scan(&page) != nil {
		t.Fatal("project ID absent")
	}
	intent := artifacts.ProjectIntent{SubmissionID: "original-comment-request", PageID: page, CommentID: c.ID, BaseVersion: 1}
	if err := f.pages.RecordArtifactProjectIntent(t.Context(), f.receipt, f.a.MessageID, intent); err != nil {
		t.Fatal("record original task", err)
	}
	writer := journal.NewWriter(f.rig.db, newTestLogger(), journal.WriterOptions{ChainKey: []byte("synthetic-artifact-task-chain"), FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	h := NewInternalHandler(f.rig.db, "synthetic-task-master", newTestLogger())
	h.SetJournal(writer)
	call := func(run, message string, purpose artifacts.ProjectIntent) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{"id": run, "workspace_id": f.a.WorkspaceID, "agent_id": f.a.AgentID, "chat_id": f.a.ChatID, "trigger_type": "USER", "human_authority": f.receipt, "trigger_message_id": message, "artifact_project_intent": purpose})
		w := httptest.NewRecorder()
		h.CreateRun(w, httptest.NewRequest("POST", "/api/v1/internal/runs", bytes.NewReader(raw)))
		return w
	}
	for i := 0; i < 2; i++ {
		if w := call(f.a.RunID, f.a.MessageID, intent); w.Code != 201 {
			t.Fatalf("original admission/retry %d %s", w.Code, w.Body)
		}
	}
	var status, active string
	if f.rig.db.QueryRow(`SELECT status,active_run_id FROM artifact_project_comment_tasks WHERE comment_id=?`, c.ID).Scan(&status, &active) != nil || status != "working" || active != f.a.RunID {
		t.Fatal("task was not atomically bound", status, active)
	}
	next := intent
	next.SubmissionID = "explicit-recovery-request"
	if err := f.pages.RecordArtifactProjectIntent(t.Context(), f.receipt, "principal-next-message", next); err == nil {
		t.Fatal("live task stolen by another human send")
	}
	if _, err := writer.EmitSync(t.Context(), journal.Entry{ID: "artifact-task-failed", WorkspaceID: f.a.WorkspaceID, CrewID: f.a.CrewID, AgentID: f.a.AgentID, TraceID: f.a.RunID, Type: journal.EntryRunFailed, ActorType: journal.ActorSystem, Summary: "synthetic terminal proof"}); err != nil {
		t.Fatal(err)
	}
	if err := f.pages.RecordArtifactProjectIntent(t.Context(), f.receipt, "principal-next-message", next); err != nil {
		t.Fatal("explicit retry after terminal proof", err)
	}
	if w := call("recovered-task-run", "principal-next-message", next); w.Code != 201 {
		t.Fatalf("recovered task admission %d %s", w.Code, w.Body)
	}
	if f.rig.db.QueryRow(`SELECT status,active_run_id FROM artifact_project_comment_tasks WHERE comment_id=?`, c.ID).Scan(&status, &active) != nil || status != "working" || active != "recovered-task-run" {
		t.Fatal("terminal task was not rebound", status, active)
	}
}

func TestArtifactProjectChatAdmissionAtomicAndExact(t *testing.T) {
	for _, change := range []string{"accepted", "missing consent", "raw metadata", "changed intent", "crew IPC", "revoked credential", "SQL failure"} {
		t.Run(change, func(t *testing.T) {
			t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
			f := artifactProjectOriginalRunFixture(t)
			intent := artifacts.ProjectIntent{SubmissionID: "original-artifact-request"}
			if change != "missing consent" && change != "raw metadata" {
				if err := f.pages.RecordArtifactProjectIntent(t.Context(), f.receipt, f.a.MessageID, intent); err != nil {
					t.Fatal("record trusted intent", err)
				}
			}
			writer := journal.NewWriter(f.rig.db, newTestLogger(), journal.WriterOptions{ChainKey: []byte("synthetic-artifact-admission-chain"), FlushSize: 1})
			t.Cleanup(func() { _ = writer.Close() })
			h := NewInternalHandler(f.rig.db, "synthetic-host-master", newTestLogger())
			h.SetJournal(writer)
			body := map[string]any{"id": f.a.RunID, "workspace_id": f.a.WorkspaceID, "agent_id": f.a.AgentID, "chat_id": f.a.ChatID, "trigger_type": "USER", "human_authority": f.receipt, "trigger_message_id": f.a.MessageID, "artifact_project_intent": intent}
			ctx := t.Context()
			switch change {
			case "raw metadata":
				delete(body, "artifact_project_intent")
				body["metadata"] = map[string]any{"artifact_project_intent": intent}
			case "changed intent":
				body["artifact_project_intent"] = artifacts.ProjectIntent{SubmissionID: "different"}
			case "crew IPC":
				ctx = context.WithValue(ctx, ctxInternalTokenCrew, f.a.CrewID)
			case "revoked credential":
				mustExec(t, f.rig.db, `UPDATE user_sessions SET revoked_at='2026-10-27T00:00:00Z' WHERE id='original-session'`)
			case "SQL failure":
				mustExec(t, f.rig.db, `CREATE TRIGGER fail_artifact_admission BEFORE INSERT ON artifact_project_run_consents BEGIN SELECT RAISE(ABORT,'synthetic artifact admission fault'); END`)
			}
			raw, _ := json.Marshal(body)
			call := func() *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				h.CreateRun(w, httptest.NewRequest("POST", "/api/v1/internal/runs", bytes.NewReader(raw)).WithContext(ctx))
				return w
			}
			w := call()
			if change == "accepted" {
				if w.Code != 201 {
					t.Fatalf("admit %d %s", w.Code, w.Body)
				}
				if w = call(); w.Code != 201 {
					t.Fatalf("exact retry %d %s", w.Code, w.Body)
				}
				for _, table := range []string{"artifact_project_run_consents", "artifact_run_principals"} {
					var n int
					if f.rig.db.QueryRow("SELECT count(*) FROM "+table+" WHERE run_id=?", f.a.RunID).Scan(&n) != nil || n != 1 {
						t.Fatal("missing atomic record", table, n)
					}
				}
				var original, stored string
				if f.rig.db.QueryRow(`SELECT p.expires_at,c.expires_at FROM artifact_run_principals p JOIN artifact_project_run_consents c ON c.run_id=p.run_id WHERE p.run_id=?`, f.a.RunID).Scan(&original, &stored) != nil || original != stored {
					t.Fatal("consent expiry differs from immutable original")
				}
				if _, err := f.rig.db.Exec(`UPDATE artifact_project_run_consents SET source_kind='manual' WHERE run_id=?`, f.a.RunID); err == nil {
					t.Fatal("typed consent source became mutable")
				}
				mustExec(t, f.rig.db, `DELETE FROM artifact_project_message_intents WHERE run_id=?`, f.a.RunID)
				if err := f.pages.checkArtifactProjectRunConsent(context.WithValue(t.Context(), ctxWorkspaceID, f.a.WorkspaceID), &f.a, ""); err == nil {
					t.Fatal("deleting typed intent widened its retained consent to manual grant")
				}
			} else if change == "raw metadata" {
				var n int
				if f.rig.db.QueryRow(`SELECT count(*) FROM artifact_project_run_consents`).Scan(&n) != nil || n != 0 {
					t.Fatal("generic metadata minted artifact permission")
				}
			} else {
				if w.Code < 400 {
					t.Fatalf("invalid admission accepted %d %s", w.Code, w.Body)
				}
				for _, query := range []string{`SELECT count(*) FROM journal_entries WHERE entry_type='run.started'`, `SELECT count(*) FROM artifact_run_principals`, `SELECT count(*) FROM artifact_project_run_consents`} {
					var n int
					if f.rig.db.QueryRow(query).Scan(&n) != nil || n != 0 {
						t.Fatal("refused admission partly committed", query, n)
					}
				}
			}
		})
	}
}

func TestArtifactProjectOriginalCLIScopeCannotWidenThroughChat(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	f := artifactProjectOriginalRunFixture(t)
	const token = "crewship_cli_artifact-original-scope"
	mustExec(t, f.rig.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('artifact-source-cli',?,'fixture',?,'STANDARD','["pages:read"]')`, f.a.HumanUserID, hashStandard(token))
	capture := func() {
		source, err := captureOriginalHumanCredential(t.Context(), f.rig.db, &AuthUser{ID: f.a.HumanUserID, TokenID: "artifact-source-cli"}, AuthKindCLIToken, token)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := NewInternalHandler(f.rig.db, "synthetic-scope-master", newTestLogger()).originalHumanChatReceipt(t.Context(), f.a.ChatID, f.a.HumanUserID, nil, source)
		if err != nil || receipt == nil {
			t.Fatal("original scoped receipt", receipt, err)
		}
		f.receipt = receipt
	}
	capture()
	intent := artifacts.ProjectIntent{SubmissionID: "explicit-scope-request"}
	if err := f.pages.RecordArtifactProjectIntent(t.Context(), f.receipt, f.a.MessageID, intent); err == nil {
		t.Fatal("read-only original CLI credential acquired artifact write mandate through chat")
	}
	mustExec(t, f.rig.db, `UPDATE cli_tokens SET scopes='["pages:read","pages:write"]' WHERE id='artifact-source-cli'`)
	capture()
	if err := f.pages.RecordArtifactProjectIntent(t.Context(), f.receipt, f.a.MessageID, intent); err != nil {
		t.Fatal("positive write scope calibration", err)
	}
	f.admit(t)
	mustExec(t, f.rig.db, `INSERT INTO artifact_project_run_consents(run_id,workspace_id,human_user_id,chat_id,message_id,expires_at,created_at) SELECT run_id,workspace_id,human_user_id,chat_id,message_id,expires_at,expires_at FROM artifact_run_principals WHERE run_id=?`, f.a.RunID)
	mustExec(t, f.rig.db, `UPDATE artifact_project_message_intents SET run_id=? WHERE message_id=?`, f.a.RunID, f.a.MessageID)
	if err := f.pages.checkArtifactProjectRunConsent(context.WithValue(t.Context(), ctxWorkspaceID, f.a.WorkspaceID), &f.a, ""); err != nil {
		t.Fatal("positive scoped consent", err)
	}
	tx, err := f.rig.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE cli_tokens SET scopes='["pages:read"]' WHERE id='artifact-source-cli'`); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(t.Context(), pageAuthReaderKey{}, tx)
	ctx = context.WithValue(ctx, ctxWorkspaceID, f.a.WorkspaceID)
	if err = f.pages.checkArtifactProjectRunConsent(ctx, &f.a, ""); err == nil {
		t.Fatal("publication writer failed to recheck original CLI write scope")
	}
}

func TestArtifactProjectAdmissionFinalTaskWriteRevocation(t *testing.T) {
	for _, change := range []string{"original_credential", "typed_message_intent"} {
		t.Run(change, func(t *testing.T) {
			t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
			f := artifactProjectOriginalRunFixture(t)
			token := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "artifact-task-owner")
			human := func(body string, slug string, handler func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
				r := pagesRequest(t, "POST", "/api/v1/pages/"+slug, f.a.WorkspaceID, f.a.HumanUserID, "OWNER", body)
				r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindCLIToken))
				r.Header.Set("Authorization", "Bearer "+token)
				r.SetPathValue("slug", slug)
				w := httptest.NewRecorder()
				handler(w, r)
				return w
			}
			create := `{"slug":"task-project","name":"Task project",` + strings.TrimPrefix(artifactProjectBody(0, "First"), "{")
			if w := human(create, "task-project", f.pages.CreateArtifactProject); w.Code != 201 {
				t.Fatalf("project %d %s", w.Code, w.Body)
			}
			comment := human(`{"version":1,"path":"index.html","selector":"#hero","quote":"First","text":"Change the heading","send_to_agent":true}`, "task-project", f.pages.CreateArtifactProjectComment)
			if comment.Code != 201 {
				t.Fatalf("comment %d %s", comment.Code, comment.Body)
			}
			var c struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(comment.Body.Bytes(), &c) != nil || c.ID == "" {
				t.Fatal("comment ID absent")
			}
			var page string
			if f.rig.db.QueryRow(`SELECT id FROM pages WHERE slug='task-project'`).Scan(&page) != nil {
				t.Fatal("project ID absent")
			}
			intent := artifacts.ProjectIntent{SubmissionID: "original-comment-request", PageID: page, CommentID: c.ID, BaseVersion: 1}
			if err := f.pages.RecordArtifactProjectIntent(t.Context(), f.receipt, f.a.MessageID, intent); err != nil {
				t.Fatal("record original task", err)
			}
			writer := journal.NewWriter(f.rig.db, newTestLogger(), journal.WriterOptions{ChainKey: []byte("synthetic-artifact-task-chain"), FlushSize: 1})
			t.Cleanup(func() { _ = writer.Close() })
			h := NewInternalHandler(f.rig.db, "synthetic-task-master", newTestLogger())
			h.SetJournal(writer)
			call := func(run, message string, purpose artifacts.ProjectIntent) *httptest.ResponseRecorder {
				raw, _ := json.Marshal(map[string]any{"id": run, "workspace_id": f.a.WorkspaceID, "agent_id": f.a.AgentID, "chat_id": f.a.ChatID, "trigger_type": "USER", "human_authority": f.receipt, "trigger_message_id": message, "artifact_project_intent": purpose})
				w := httptest.NewRecorder()
				h.CreateRun(w, httptest.NewRequest("POST", "/api/v1/internal/runs", bytes.NewReader(raw)))
				return w
			}
			changeSQL := `UPDATE user_sessions SET revoked_at='2026-10-27T00:00:00Z' WHERE id='original-session';`
			if change == "typed_message_intent" {
				changeSQL = `UPDATE artifact_project_message_intents SET revoked_at='2026-10-27T00:00:00Z';`
			}
			mustExec(t, f.rig.db, `CREATE TRIGGER review_revoke_after_task_assignment AFTER UPDATE OF status ON artifact_project_comment_tasks WHEN NEW.status='working' BEGIN `+changeSQL+` END`)
			if w := call(f.a.RunID, f.a.MessageID, intent); w.Code < 400 {
				t.Errorf("admission committed after authority revoked by final task write: %d %s", w.Code, w.Body)
			}
			for _, query := range []string{`SELECT count(*) FROM journal_entries WHERE entry_type='run.started'`, `SELECT count(*) FROM artifact_run_principals`, `SELECT count(*) FROM artifact_project_run_consents`} {
				var n int
				if err := f.rig.db.QueryRow(query).Scan(&n); err != nil || n != 0 {
					t.Fatalf("refused admission partly committed: %s count%d err%v", query, n, err)
				}
			}

		})
	}
}

func TestArtifactProjectIntentRecorderFinalSealRevocation(t *testing.T) {
	for _, change := range []string{"accepted", "original_credential", "typed_message_intent"} {
		t.Run(change, func(t *testing.T) {
			t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
			f := artifactProjectOriginalRunFixture(t)
			intent := artifacts.ProjectIntent{SubmissionID: "review-original-create"}
			if change != "accepted" {
				changeSQL := `UPDATE user_sessions SET revoked_at='2026-10-27T00:00:00Z' WHERE id='original-session';`
				if change == "typed_message_intent" {
					changeSQL = `UPDATE artifact_project_message_intents SET revoked_at='2026-10-27T00:00:00Z';`
				}
				mustExec(t, f.rig.db, `CREATE TRIGGER review_revoke_after_chat_seal AFTER UPDATE OF private_artifact_author_user_id ON chats BEGIN `+changeSQL+` END`)
			}
			err := f.pages.RecordArtifactProjectIntent(t.Context(), f.receipt, f.a.MessageID, intent)
			if change == "accepted" {
				if err != nil {
					t.Fatal("positive recorder calibration", err)
				}
				return
			}
			if err == nil {
				t.Error("recorder committed after final chat seal revoked original authority")
			}
			var n int
			if err := f.rig.db.QueryRow(`SELECT count(*) FROM artifact_project_message_intents`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("refused recorder retained intent: count%d err%v", n, err)
			}
		})
	}
}

func TestArtifactProjectOwnerExplicitHandoffOfSharedComment(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	f := artifactProjectOriginalRunFixture(t)
	token := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "artifact-task-owner")
	human := func(body string, slug string, handler func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		r := pagesRequest(t, "POST", "/api/v1/pages/"+slug, f.a.WorkspaceID, f.a.HumanUserID, "OWNER", body)
		r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindCLIToken))
		r.Header.Set("Authorization", "Bearer "+token)
		r.SetPathValue("slug", slug)
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	create := `{"slug":"task-project","name":"Task project",` + strings.TrimPrefix(artifactProjectBody(0, "First"), "{")
	if w := human(create, "task-project", f.pages.CreateArtifactProject); w.Code != 201 {
		t.Fatalf("project %d %s", w.Code, w.Body)
	}
	if w := human(`{"base_version":1,"version":1}`, "task-project", f.pages.ShareArtifactProject); w.Code != 200 {
		t.Fatalf("share %d %s", w.Code, w.Body)
	}
	// A non-owner human with Edit leaves the feedback task.
	commenter := "explicit-commenter"
	mustExec(t, f.rig.db, `INSERT INTO users(id,email,full_name) VALUES(?, 'explicit-commenter@example.test','Commenter')`, commenter)
	mustExec(t, f.rig.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), f.a.WorkspaceID, commenter)
	var ownedPage string
	if err := f.rig.db.QueryRow(`SELECT id FROM pages WHERE slug='task-project'`).Scan(&ownedPage); err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,'write',?)`, ownedPage, commenter, f.a.HumanUserID)
	commenterToken := mintTokenFor(t, f.rig.db, commenter, "explicit-commenter")
	commenterRequest := pagesRequest(t, "POST", "/api/v1/pages/task-project/artifact/comments", f.a.WorkspaceID, commenter, "MEMBER", `{"version":1,"path":"index.html","selector":"#hero","quote":"First","text":"Feedback on shared content","send_to_agent":true}`)
	commenterRequest = commenterRequest.WithContext(context.WithValue(commenterRequest.Context(), ctxAuthKind, AuthKindCLIToken))
	commenterRequest.Header.Set("Authorization", "Bearer "+commenterToken)
	commenterRequest.SetPathValue("slug", "task-project")
	comment := httptest.NewRecorder()
	f.pages.CreateArtifactProjectComment(comment, commenterRequest)
	if comment.Code != 201 {
		t.Fatalf("comment %d %s", comment.Code, comment.Body)
	}
	// Queuing an agent task needs Edit; a Comment grant cannot. The owner then
	// moves on privately, so the task keeps its shared base version.
	if w := human(artifactProjectBody(1, "Private two"), "task-project", f.pages.PublishArtifactProject); w.Code != 200 {
		t.Fatalf("publish private %d %s", w.Code, w.Body)
	}
	var c struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(comment.Body.Bytes(), &c) != nil || c.ID == "" {
		t.Fatal("comment ID absent")
	}
	var page string
	if f.rig.db.QueryRow(`SELECT id FROM pages WHERE slug='task-project'`).Scan(&page) != nil {
		t.Fatal("project ID absent")
	}
	var savedAuthor string
	var savedVersion, intents int
	if err := f.rig.db.QueryRow(`SELECT author,version FROM artifact_project_comments WHERE id=?`, c.ID).Scan(&savedAuthor, &savedVersion); err != nil || savedAuthor != commenter || savedVersion != 1 {
		t.Fatalf("feedback provenance %q/%d: %v", savedAuthor, savedVersion, err)
	}
	if err := f.rig.db.QueryRow(`SELECT count(*) FROM artifact_project_message_intents WHERE page_id=?`, page).Scan(&intents); err != nil || intents != 0 {
		t.Fatalf("commenter triggered intent %d %v", intents, err)
	}
	mustExec(t, f.rig.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id=?`, page, commenter)
	intent := artifacts.ProjectIntent{SubmissionID: "original-comment-request", PageID: page, CommentID: c.ID, BaseVersion: 2}
	if err := f.pages.RecordArtifactProjectIntent(t.Context(), f.receipt, f.a.MessageID, intent); err != nil {
		t.Fatal("record original task", err)
	}
	keys, err := journal.LoadSigningKeysFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	writer := journal.NewWriter(f.rig.db, newTestLogger(), journal.WriterOptions{SigningKeys: keys, FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	h := NewInternalHandler(f.rig.db, "synthetic-task-master", newTestLogger())
	h.SetJournal(writer)
	call := func(run, message string, purpose artifacts.ProjectIntent) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{"id": run, "workspace_id": f.a.WorkspaceID, "agent_id": f.a.AgentID, "chat_id": f.a.ChatID, "trigger_type": "USER", "human_authority": f.receipt, "trigger_message_id": message, "artifact_project_intent": purpose})
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/v1/internal/runs", bytes.NewReader(raw))
		r = r.WithContext(journal.WithSigningKeys(r.Context(), keys))
		h.CreateRun(w, r)
		return w
	}
	// Correlated task/comment changes still violate the captured feedback tuple.
	mustExec(t, f.rig.db, `CREATE TRIGGER feedback_rebase_during_claim AFTER UPDATE OF status ON artifact_project_comment_tasks WHEN NEW.status='working' BEGIN UPDATE artifact_project_comments SET version=2 WHERE id=NEW.comment_id; UPDATE artifact_project_comment_tasks SET base_version=2 WHERE id=NEW.id; END`)
	if w := call(f.a.RunID, f.a.MessageID, intent); w.Code != 403 {
		t.Fatalf("feedback identity changed during bind: %d %s", w.Code, w.Body)
	}
	mustExec(t, f.rig.db, `DROP TRIGGER feedback_rebase_during_claim`)
	var starts int
	if err := f.rig.db.QueryRow(`SELECT count(*) FROM journal_entries WHERE trace_id=? AND entry_type='run.started'`, f.a.RunID).Scan(&starts); err != nil || starts != 0 {
		t.Fatalf("denied claim leaked run ACK: %d %v", starts, err)
	}
	for i := 0; i < 2; i++ {
		if w := call(f.a.RunID, f.a.MessageID, intent); w.Code != 201 {
			t.Fatalf("original admission/retry %d %s", w.Code, w.Body)
		}
	}
	var status, active string
	if f.rig.db.QueryRow(`SELECT status,active_run_id FROM artifact_project_comment_tasks WHERE comment_id=?`, c.ID).Scan(&status, &active) != nil || status != "working" || active != f.a.RunID {
		t.Fatal("task was not atomically bound", status, active)
	}
	var taskBase int
	if err := f.rig.db.QueryRow(`SELECT base_version FROM artifact_project_comment_tasks WHERE comment_id=?`, c.ID).Scan(&taskBase); err != nil || taskBase != 1 {
		t.Fatalf("feedback task base rewritten %d: %v", taskBase, err)
	}
	next := intent
	next.SubmissionID = "explicit-recovery-request"
	if err := f.pages.RecordArtifactProjectIntent(t.Context(), f.receipt, "principal-next-message", next); err == nil {
		t.Fatal("live task stolen by another human send")
	}
	if _, err := writer.EmitSync(t.Context(), journal.Entry{ID: "artifact-task-failed", WorkspaceID: f.a.WorkspaceID, CrewID: f.a.CrewID, AgentID: f.a.AgentID, TraceID: f.a.RunID, Type: journal.EntryRunFailed, ActorType: journal.ActorSystem, Summary: "synthetic terminal proof"}); err != nil {
		t.Fatal(err)
	}
	if err := f.pages.RecordArtifactProjectIntent(t.Context(), f.receipt, "principal-next-message", next); err != nil {
		t.Fatal("explicit retry after terminal proof", err)
	}
	if w := call("recovered-task-run", "principal-next-message", next); w.Code != 201 {
		t.Fatalf("recovered task admission %d %s", w.Code, w.Body)
	}
	if f.rig.db.QueryRow(`SELECT status,active_run_id FROM artifact_project_comment_tasks WHERE comment_id=?`, c.ID).Scan(&status, &active) != nil || status != "working" || active != "recovered-task-run" {
		t.Fatal("terminal task was not rebound", status, active)
	}
}
