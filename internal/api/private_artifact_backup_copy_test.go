package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// These are real admission/classification and legacy backup-handler boundaries.
// The last-Page case uses SQL DELETE and real FK cascades, not an HTTP delete.
// No bundle is created: malformed body/missing path prove audience checks run
// before encryption selection, filesystem access or backup execution.
func TestPrivateArtifactBackupCopiesRequireRetainedAudience(t *testing.T) {
	for _, lifecycle := range []string{"last Page deleted", "consent only", "ordinary never sealed"} {
		t.Run(lifecycle, func(t *testing.T) {
			var pages *PageHandler
			var workspace, author, owner, admin string
			if lifecycle == "last Page deleted" {
				var saved documentCreationResult
				pages, workspace, author, owner, admin, saved = privateConversationAuditFixture(t)
				execOrFatal(t, pages.db, `DELETE FROM pages WHERE id=?`, saved.PageID)
			} else {
				var f *artifactPrincipalFixture
				if lifecycle == "consent only" {
					f, _ = documentCreationAdmissionFixture(t)
				} else {
					f = newArtifactPrincipalFixture(t)
				}
				pages, workspace, author = f.pages, f.a.WorkspaceID, f.a.HumanUserID
				owner, admin = "backup-context-owner", "backup-context-admin"
				pagesSeedUser(t, pages, workspace, owner, "backup-context-owner@example.test", "OWNER")
				pagesSeedUser(t, pages, workspace, admin, "backup-context-admin@example.test", "ADMIN")
			}
			var privatePages, creations, retainedMessages, sealed int
			if err := pages.db.QueryRow(`SELECT
 (SELECT count(*) FROM pages WHERE workspace_id=? AND private_audience=1),
 (SELECT count(*) FROM document_creations WHERE workspace_id=?),
 (SELECT count(*) FROM conversation_messages WHERE session_id='principal-chat'),
 (SELECT count(*) FROM chats WHERE id='principal-chat' AND workspace_id=? AND private_artifact_author_user_id=?)`, workspace, workspace, workspace, author).Scan(&privatePages, &creations, &retainedMessages, &sealed); err != nil {
				t.Fatal(err)
			}
			wantSealed := 1
			if lifecycle == "ordinary never sealed" {
				wantSealed = 0
			}
			if privatePages != 0 || creations != 0 || retainedMessages == 0 || sealed != wantSealed {
				t.Fatalf("lifecycle setup: privatePages=%d creations=%d messages=%d sealed=%d", privatePages, creations, retainedMessages, sealed)
			}
			handler := NewBackupHandler(pages.db, newTestLogger(), nil, "fixture")
			for _, surface := range []string{"create", "download"} {
				for _, actor := range []string{"ADMIN", "OWNER", "instance admin"} {
					t.Run(surface+"/"+actor, func(t *testing.T) {
						user, role := admin, "ADMIN"
						if actor == "OWNER" {
							user, role = owner, "OWNER"
						}
						method, path := http.MethodPost, "/api/v1/admin/backups"
						if surface == "download" {
							method, path = http.MethodGet, path+"/download"
						}
						request := pagesRequest(t, method, path, workspace, user, role, "{")
						if actor == "instance admin" {
							request = request.WithContext(context.WithValue(request.Context(), ctxInstanceAdmin, true))
						}
						response := httptest.NewRecorder()
						if surface == "create" {
							handler.Create(response, request)
						} else {
							handler.Download(response, request)
						}
						want := http.StatusBadRequest // Positive control reached body/path validation.
						if actor == "ADMIN" && wantSealed == 1 {
							want = http.StatusForbidden
						}
						if response.Code != want {
							t.Errorf("retained source audience: status=%d want=%d", response.Code, want)
						}
					})
				}
			}
		})
	}
}
