package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// A Comment grant is feedback only. Queuing an agent task needs Edit, as for
// Design comments; the refusal stores neither the comment nor a task.
func TestArtifactCommentGrantCannotQueueAgentTask(t *testing.T) {
	h, page, owner, user, call := commentGrantFixture(t, "comment")
	got := call(user, "POST", "three-screens/artifact/comments", sharedAgentCommentBody)
	if got.Code != 403 {
		t.Fatalf("commenter queued an agent task: %d %s", got.Code, got.Body)
	}
	var comments, tasks int
	if err := h.db.QueryRow(`SELECT (SELECT count(*) FROM artifact_project_comments WHERE page_id=?),(SELECT count(*) FROM artifact_project_comment_tasks WHERE page_id=?)`, page, page).Scan(&comments, &tasks); err != nil || comments != 0 || tasks != 0 {
		t.Fatalf("refused send_to_agent persisted %d comments, %d tasks: %v", comments, tasks, err)
	}
	if plain := call(user, "POST", "three-screens/artifact/comments", sharedCommentBody); plain.Code != 201 {
		t.Fatalf("plain feedback %d %s", plain.Code, plain.Body)
	}
	if err := h.db.QueryRow(`SELECT count(*) FROM artifact_project_comment_tasks WHERE page_id=?`, page).Scan(&tasks); err != nil || tasks != 0 {
		t.Fatalf("plain feedback created a task: %d %v", tasks, err)
	}
	if queued := call(owner, "POST", "three-screens/artifact/comments", `{"version":2,"path":"index.html","selector":"#hero","quote":"Private two","text":"Owner task","send_to_agent":true}`); queued.Code != 201 {
		t.Fatalf("owner task %d %s", queued.Code, queued.Body)
	}
}

func TestArtifactEditGrantMayQueueAgentTask(t *testing.T) {
	h, page, _, editor, call := commentGrantFixture(t, "write")
	if got := call(editor, "POST", "three-screens/artifact/comments", `{"version":2,"path":"index.html","selector":"#hero","quote":"Private two","text":"Editor task","send_to_agent":true}`); got.Code != 201 {
		t.Fatalf("editor task %d %s", got.Code, got.Body)
	}
	var tasks int
	if err := h.db.QueryRow(`SELECT count(*) FROM artifact_project_comment_tasks WHERE page_id=?`, page).Scan(&tasks); err != nil || tasks != 1 {
		t.Fatalf("editor tasks %d %v", tasks, err)
	}
}

// A run whose intent names no comment may answer feedback only with a version
// it published. It may not close a task with needs_clarification, and no run
// may answer a task that another run holds.
func TestArtifactReplyRunWithoutCommentTarget(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	f := artifactProjectOriginalRunFixture(t)
	f.admit(t)
	f.pages.artifactMasterToken = "synthetic-artifact-project-master"
	humanToken := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "artifact-reply-target")
	human := func(path string, payload string, handler http.HandlerFunc) *httptest.ResponseRecorder {
		r := pagesRequest(t, "POST", path, f.a.WorkspaceID, f.a.HumanUserID, "OWNER", payload)
		r.Header.Set("Authorization", "Bearer "+humanToken)
		r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindCLIToken))
		r.SetPathValue("slug", "agent-screens")
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	runToken := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	tool := func(operation, payload string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/internal/pages/artifact-tool", strings.NewReader(fmt.Sprintf(`{"operation":%q,"slug":"agent-screens","payload":%s}`, operation, payload)))
		r.Header.Set("Authorization", "Bearer "+runToken)
		ctx := context.WithValue(r.Context(), ctxInternalTokenWS, f.a.WorkspaceID)
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, f.a.CrewID)
		w := httptest.NewRecorder()
		f.pages.ArtifactProjectTool(w, r.WithContext(ctx))
		return w
	}
	if grant := human("/api/v1/pages/artifacts/consent", `{"run_id":"principal-run"}`, f.pages.GrantArtifactProjectRunConsent); grant.Code != 200 {
		t.Fatalf("consent %d %s", grant.Code, grant.Body)
	}
	if created := tool("artifact_create", `{"name":"Agent screens",`+strings.TrimPrefix(artifactProjectBody(0, "First"), "{")); created.Code != 201 {
		t.Fatalf("create %d %s", created.Code, created.Body)
	}
	// The run's intent is Page-level: it names no comment.
	mustExec(t, f.rig.db, `INSERT INTO artifact_project_message_intents(message_id,workspace_id,human_user_id,chat_id,submission_id,base_version,run_id,created_at) VALUES(?,?,?,?,?,0,?,datetime('now'))`, f.a.MessageID, f.a.WorkspaceID, f.a.HumanUserID, f.a.ChatID, "page-level-intent", f.a.RunID)
	comment := human("/api/v1/pages/agent-screens/artifact/comments", `{"version":1,"path":"index.html","selector":"#hero","quote":"First","text":"Owner task","send_to_agent":true}`, f.pages.CreateArtifactProjectComment)
	var c artifactProjectComment
	if comment.Code != 201 || json.Unmarshal(comment.Body.Bytes(), &c) != nil || c.TaskID == "" {
		t.Fatalf("owner task %d %s", comment.Code, comment.Body)
	}
	state := func() (string, int) {
		t.Helper()
		var status string
		var replies int
		if err := f.rig.db.QueryRow(`SELECT status,(SELECT count(*) FROM artifact_project_comment_replies WHERE comment_id=?) FROM artifact_project_comment_tasks WHERE comment_id=?`, c.ID, c.ID).Scan(&status, &replies); err != nil {
			t.Fatal(err)
		}
		return status, replies
	}
	if w := tool("artifact_reply", fmt.Sprintf(`{"comment_id":%q,"version":1,"text":"Which one?","needs_clarification":true}`, c.ID)); w.Code != 403 {
		t.Fatalf("untargeted run closed a task with needs_clarification: %d %s", w.Code, w.Body)
	}
	if status, replies := state(); status != "pending" || replies != 0 {
		t.Fatalf("refused clarification changed task: %s/%d", status, replies)
	}
	if w := tool("artifact_publish", artifactProjectBody(1, "Second")); w.Code != 200 {
		t.Fatalf("publish v2 %d %s", w.Code, w.Body)
	}
	mustExec(t, f.rig.db, `UPDATE artifact_project_comment_tasks SET status='working',active_run_id='another-run' WHERE comment_id=?`, c.ID)
	if w := tool("artifact_reply", fmt.Sprintf(`{"comment_id":%q,"version":2,"text":"Changed the heading"}`, c.ID)); w.Code != 403 {
		t.Fatalf("run answered a task held by another run: %d %s", w.Code, w.Body)
	}
	if status, replies := state(); status != "working" || replies != 0 {
		t.Fatalf("refused answer changed task: %s/%d", status, replies)
	}
	// Calibration: an unbound task is still answerable with the run's own version.
	mustExec(t, f.rig.db, `UPDATE artifact_project_comment_tasks SET status='pending',active_run_id=NULL WHERE comment_id=?`, c.ID)
	if w := tool("artifact_reply", fmt.Sprintf(`{"comment_id":%q,"version":2,"text":"Changed the heading"}`, c.ID)); w.Code != 200 && w.Code != 201 {
		t.Fatalf("own-version answer %d %s", w.Code, w.Body)
	}
	if status, replies := state(); status != "answered" || replies != 1 {
		t.Fatalf("answer not recorded: %s/%d", status, replies)
	}
}

// B15 (G4): with 500 comments already stored, the comment just created must
// still be listed (the newest 500, oldest first), or `--wait` cannot find it.
func TestArtifactCommentListKeepsNewestWhenOverCap(t *testing.T) {
	h, page, owner, _, call := commentGrantFixture(t, "comment")
	var ws string
	if err := h.db.QueryRow(`SELECT workspace_id FROM pages WHERE id=?`, page).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		mustExec(t, h.db, `INSERT INTO artifact_project_comments(id,workspace_id,page_id,version,path,selector,quote,text,author,created_at) VALUES(?,?,?,2,'index.html','#hero','Private two','old',?,?)`,
			fmt.Sprintf("old-%03d", i), ws, page, owner, fmt.Sprintf("2020-01-01T00:00:00.%03dZ", i))
	}
	created := call(owner, "POST", "three-screens/artifact/comments", `{"version":2,"path":"index.html","selector":"#hero","quote":"Private two","text":"newest"}`)
	if created.Code != 201 {
		t.Fatalf("create %d %s", created.Code, created.Body)
	}
	var made struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &made); err != nil || made.ID == "" {
		t.Fatalf("created comment: %v %s", err, created.Body)
	}
	got := call(owner, "GET", "three-screens/artifact/comments?version=2", "")
	if got.Code != 200 {
		t.Fatalf("list %d %s", got.Code, got.Body)
	}
	var listing struct {
		Comments []struct {
			ID string `json:"id"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Comments) != 500 {
		t.Fatalf("listed %d comments, want the 500 newest", len(listing.Comments))
	}
	if last := listing.Comments[len(listing.Comments)-1].ID; last != made.ID {
		t.Fatalf("last listed comment = %q, want the newest %q", last, made.ID)
	}
	if first := listing.Comments[0].ID; first != "old-001" {
		t.Fatalf("first listed comment = %q, want old-001 (oldest of the newest 500)", first)
	}
}
