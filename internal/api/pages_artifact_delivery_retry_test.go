package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestArtifactProjectDeliveryRetryEligibility(t *testing.T) {
	for _, change := range []string{"pending", "many pending", "accepted before admission", "working", "answered", "missing origin", "changed origin author", "group origin", "deleted origin agent", "policy revoked", "old base", "read-only credential", "revoked accepted intent", "shared reader", "invalid sealed origin", "mismatched task"} {
		t.Run(change, func(t *testing.T) {
			t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
			f := artifactProjectOriginalRunFixture(t)
			token := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "retry-owner")
			caller := f.a.HumanUserID
			call := func(method, body string, handler func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
				r := pagesRequest(t, method, "/api/v1/pages/retry-project/artifact", f.a.WorkspaceID, caller, "OWNER", body)
				r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindCLIToken))
				r.Header.Set("Authorization", "Bearer "+token)
				r.SetPathValue("slug", "retry-project")
				w := httptest.NewRecorder()
				handler(w, r)
				return w
			}
			create := `{"slug":"retry-project","name":"Retry project",` + strings.TrimPrefix(artifactProjectBody(0, "First"), "{")
			if w := call("POST", create, f.pages.CreateArtifactProject); w.Code != 201 {
				t.Fatalf("create: %d %s", w.Code, w.Body)
			}
			var page string
			if err := f.rig.db.QueryRow(`SELECT id FROM pages WHERE slug='retry-project' AND workspace_id=?`, f.a.WorkspaceID).Scan(&page); err != nil {
				t.Fatal(err)
			}
			mustExec(t, f.rig.db, `INSERT INTO artifact_project_origins(page_id,workspace_id,human_user_id,chat_id,agent_id,message_id,created_at) VALUES(?,?,?,?,?,?,datetime('now'))`, page, f.a.WorkspaceID, f.a.HumanUserID, f.a.ChatID, f.a.AgentID, f.a.MessageID)
			w := call("POST", `{"version":1,"path":"index.html","selector":"#hero","quote":"First","text":"Change heading","send_to_agent":true}`, f.pages.CreateArtifactProjectComment)
			if w.Code != 201 {
				t.Fatalf("comment %d %s", w.Code, w.Body)
			}
			var comment artifactProjectComment
			if err := json.Unmarshal(w.Body.Bytes(), &comment); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "accepted before admission", "revoked accepted intent":
				intent := artifacts.ProjectIntent{SubmissionID: "artifact-comment-" + comment.ID, PageID: page, CommentID: comment.ID, BaseVersion: 1}
				if err := f.pages.RecordArtifactProjectIntent(t.Context(), f.receipt, f.a.MessageID, intent); err != nil {
					t.Fatal(err)
				}
				if change == "revoked accepted intent" {
					mustExec(t, f.rig.db, `UPDATE artifact_project_message_intents SET revoked_at=datetime('now') WHERE comment_id=?`, comment.ID)
				}
			case "working":
				mustExec(t, f.rig.db, `UPDATE artifact_project_comment_tasks SET status='working',active_run_id='claimed-run' WHERE comment_id=?`, comment.ID)
			case "answered":
				mustExec(t, f.rig.db, `UPDATE artifact_project_comment_tasks SET status='answered' WHERE comment_id=?`, comment.ID)
			case "missing origin":
				mustExec(t, f.rig.db, `DELETE FROM artifact_project_origins WHERE page_id=?`, page)
			case "changed origin author":
				mustExec(t, f.rig.db, `UPDATE artifact_project_origins SET human_user_id='someone-else' WHERE page_id=?`, page)
			case "group origin":
				mustExec(t, f.rig.db, `UPDATE chats SET visibility='group' WHERE id=?`, f.a.ChatID)
			case "deleted origin agent":
				mustExec(t, f.rig.db, `UPDATE agents SET deleted_at=datetime('now') WHERE id=?`, f.a.AgentID)
			case "policy revoked":
				mustExec(t, f.rig.db, `UPDATE workspace_artifact_creation_policy SET enabled=0 WHERE workspace_id=?`, f.a.WorkspaceID)
			case "old base":
				if w := call("POST", artifactProjectBody(1, "New head"), f.pages.PublishArtifactProject); w.Code != 200 {
					t.Fatalf("publish: %d %s", w.Code, w.Body)
				}
			case "shared reader":
				if w := call("POST", `{"base_version":1,"version":1}`, f.pages.ShareArtifactProject); w.Code != 200 {
					t.Fatalf("share: %d %s", w.Code, w.Body)
				}
				caller = "retry-reader"
				token = seedRoleMemberToken(t, f.rig.db, f.a.WorkspaceID, caller, "VIEWER", "retry-reader")
				mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,'read',?,datetime('now'))`, page, caller, f.a.HumanUserID)
			case "invalid sealed origin":
				mustExec(t, f.rig.db, `INSERT INTO users(id,email) VALUES('wrong-private-author','wrong-private-author@fixture.invalid')`)
				mustExec(t, f.rig.db, `UPDATE chats SET private_artifact_author_user_id='wrong-private-author' WHERE id=?`, f.a.ChatID)
			case "mismatched task":
				mustExec(t, f.rig.db, `UPDATE artifact_project_comment_tasks SET base_version=2 WHERE comment_id=?`, comment.ID)
			case "read-only credential":
				token = mintIngressScopedToken(t, f.rig.db, f.a.HumanUserID, "retry-read-only", "pages:read")
			}
			expectedCount := 1
			if change == "many pending" {
				for range 15 {
					extra := call("POST", `{"version":1,"path":"index.html","selector":"#hero","quote":"First","text":"Another waiting comment","send_to_agent":true}`, f.pages.CreateArtifactProjectComment)
					if extra.Code != 201 {
						t.Fatalf("extra comment: %d %s", extra.Code, extra.Body)
					}
					expectedCount++
				}
			}
			eligibilityQueries := 0
			if change == "pending" || change == "many pending" {
				var file string
				if err := f.rig.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&file); err != nil {
					t.Fatal(err)
				}
				counted, counter := testutil.CountingSQLDB(t, file, 1)
				f.pages.db = counted
				counter.BeforeQuery(func(query string) {
					if strings.Contains(query, "SELECT c.id FROM artifact_project_comments c") {
						eligibilityQueries++
					}
				})
			}
			w = call("GET", "", f.pages.GetArtifactProject)
			if w.Code != 200 {
				t.Fatalf("read: %d %s", w.Code, w.Body)
			}
			var response struct {
				Comments []map[string]any `json:"comments"`
				Origin   any              `json:"origin"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Comments) != expectedCount {
				t.Fatalf("comments: %v %s", err, w.Body)
			}
			if change == "shared reader" && (response.Origin != nil || response.Comments[0]["task_id"] != nil || response.Comments[0]["status"] != "comment") {
				t.Fatalf("private delivery metadata leaked: %s", w.Body)
			}
			want := change == "pending" || change == "many pending"
			for _, item := range response.Comments {
				got, ok := item["delivery_retryable"].(bool)
				if !ok || got != want {
					t.Fatalf("delivery_retryable=%v present=%v expected=%v", got, ok, want)
				}
			}
			if want && eligibilityQueries != 1 {
				t.Fatalf("%d comments used %d eligibility queries, expected one batch", expectedCount, eligibilityQueries)
			}

		})
	}
}
