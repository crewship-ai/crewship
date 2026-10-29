package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func artifactFeedbackHTTPFixture(t *testing.T) (*PageHandler, string, string, func(string, string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	h, ws, owner, seed := artifactProjectFixture(t)
	body := strings.TrimPrefix(artifactProjectBody(0, "Shared title"), "{")
	if got := seed(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Screens",`+body); got.Code != 201 {
		t.Fatalf("seed: %d %s", got.Code, got.Body)
	}
	router, err := NewRouter(h.db, "synthetic-feedback-http-secret-for-tests", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	token := mintIngressScopedToken(t, h.db, owner, "feedback-http-owner", "*")
	calibration := httptest.NewRequest("GET", "/api/v1/pages/three-screens", nil)
	calibration.Header.Set("Authorization", "Bearer "+token)
	calibration.Header.Set("X-Workspace-ID", ws)
	calibrated := httptest.NewRecorder()
	router.ServeHTTP(calibrated, calibration)
	if calibrated.Code != 200 {
		t.Fatalf("ordinary Page owner calibration: %d %s", calibrated.Code, calibrated.Body)
	}
	call := func(credential, method, suffix, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/pages/three-screens/artifact"+suffix, strings.NewReader(body))
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		req.Header.Set("X-Workspace-ID", ws)
		req.Header.Set("Content-Type", "application/json")
		got := httptest.NewRecorder()
		router.ServeHTTP(got, req)
		return got
	}
	return h, ws, token, call
}

const artifactFeedbackComment = `{"version":1,"path":"index.html","selector":"#hero","quote":"Shared title","text":"Please change the title","send_to_agent":true}`

func TestArtifactFeedbackHTTPActualRoutesAndCAS(t *testing.T) {
	h, _, token, call := artifactFeedbackHTTPFixture(t)
	if got := call(token, "GET", "/comments", ""); got.Code != 200 {
		t.Fatalf("comments route: %d %s", got.Code, got.Body)
	}
	created := call(token, "POST", "/comments", artifactFeedbackComment)
	if created.Code != 201 {
		t.Fatalf("comment route: %d %s", created.Code, created.Body)
	}
	var comment artifactProjectComment
	if err := json.Unmarshal(created.Body.Bytes(), &comment); err != nil || comment.TaskID == "" {
		t.Fatalf("version-bound task missing: %s %v", created.Body, err)
	}
	if got := call(token, "POST", "/comments/"+comment.ID+"/reply", `{"version":1,"text":"Which shade?","needs_clarification":true}`); got.Code != 200 {
		t.Fatalf("reply route: %d %s", got.Code, got.Body)
	}
	if got := call(token, "POST", "/restore", `{"base_version":1,"version":1}`); got.Code != 200 {
		t.Fatalf("restore route: %d %s", got.Code, got.Body)
	}
	for _, suffix := range []string{"/restore", "/share"} {
		if got := call(token, "POST", suffix, `{"base_version":1,"version":1}`); got.Code != 409 {
			t.Errorf("stale %s must preserve candidate: %d %s", suffix, got.Code, got.Body)
		}
	}
	if got := call(token, "POST", "/share", `{"base_version":2,"version":1}`); got.Code != 200 {
		t.Fatalf("share route: %d %s", got.Code, got.Body)
	}
	var latest, shared, tasks int
	if err := h.db.QueryRow(`SELECT latest_version,shared_version FROM artifact_version_heads`).Scan(&latest, &shared); err != nil || latest != 2 || shared != 1 {
		t.Fatalf("restore/share state: latest=%d shared=%d err=%v", latest, shared, err)
	}
	if err := h.db.QueryRow(`SELECT count(*) FROM artifact_project_comment_tasks WHERE base_version=1`).Scan(&tasks); err != nil || tasks != 1 {
		t.Fatalf("exact task state: %d %v", tasks, err)
	}
	if got := call(token, "POST", "/comments", artifactFeedbackComment); got.Code != 409 {
		t.Fatalf("old anchor must ask for reselection: %d %s", got.Code, got.Body)
	}
}

func TestArtifactFeedbackHTTPDefaultOffAndWriteScope(t *testing.T) {
	h, ws, token, call := artifactFeedbackHTTPFixture(t)
	var owner string
	if err := h.db.QueryRow(`SELECT owner_user_id FROM pages WHERE slug='three-screens'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	read := mintIngressScopedToken(t, h.db, owner, "feedback-http-readonly", "pages:read")
	if got := call(read, "GET", "/comments", ""); got.Code != 200 {
		t.Fatalf("read-only owner calibration: %d %s", got.Code, got.Body)
	}
	if got := call(token, "POST", "/share", `{"base_version":1,"version":1}`); got.Code != 200 {
		t.Fatalf("shared pin calibration: %d %s", got.Code, got.Body)
	}
	// Selecting a shared pin must not expose the Page to an ungranted member.
	outsider := seedRoleMemberToken(t, h.db, ws, "feedback-http-outsider", "MEMBER", "feedback-http-outsider")
	for _, tc := range []struct{ method, suffix, body string }{
		{"GET", "/comments", ""},
		{"POST", "/comments", artifactFeedbackComment},
		{"POST", "/restore", `{"base_version":1,"version":1}`},
		{"POST", "/share", `{"base_version":1,"version":1}`},
		{"POST", "/comments/absent/reply", `{"version":1,"text":"forged","needs_clarification":true}`},
	} {
		if got := call("", tc.method, tc.suffix, tc.body); got.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated %s: %d", tc.suffix, got.Code)
		}
		if got := call(outsider, tc.method, tc.suffix, tc.body); got.Code != 404 {
			t.Errorf("private project leaked through %s: %d %s", tc.suffix, got.Code, got.Body)
		}
		if tc.method == "POST" {
			if got := call(read, tc.method, tc.suffix, tc.body); got.Code != 403 {
				t.Errorf("read-only write %s: %d %s", tc.suffix, got.Code, got.Body)
			}
		}
	}
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "false")
	for _, suffix := range []string{"/comments", "/restore", "/share", "/comments/absent/reply"} {
		method := "POST"
		if suffix == "/comments" {
			if got := call(token, "GET", suffix, ""); got.Code != 404 {
				t.Errorf("default-off read: %d", got.Code)
			}
		}
		if got := call(token, method, suffix, `{}`); got.Code != 404 {
			t.Errorf("default-off %s: %d %s", suffix, got.Code, got.Body)
		}
	}
	var latest, comments int
	if err := h.db.QueryRow(`SELECT latest_version FROM artifact_version_heads`).Scan(&latest); err != nil || latest != 1 {
		t.Fatalf("denied writes changed head: %d %v", latest, err)
	}
	if err := h.db.QueryRow(`SELECT count(*) FROM artifact_project_comments`).Scan(&comments); err != nil || comments != 0 {
		t.Fatalf("denied writes persisted comments: %d %v", comments, err)
	}
}

func TestArtifactFeedbackHTTPReaderCannotSeePrivateReplyOrWrite(t *testing.T) {
	h, ws, token, call := artifactFeedbackHTTPFixture(t)
	created := call(token, "POST", "/comments", artifactFeedbackComment)
	var comment artifactProjectComment
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &comment) != nil {
		t.Fatalf("comment: %d %s", created.Code, created.Body)
	}
	for _, suffix := range []string{"/share", "/restore"} {
		if got := call(token, "POST", suffix, `{"base_version":1,"version":1}`); got.Code != 200 {
			t.Fatalf("prepare %s: %d %s", suffix, got.Code, got.Body)
		}
	}
	if got := call(token, "POST", "/comments/"+comment.ID+"/reply", `{"version":2,"text":"Unpublished private explanation"}`); got.Code != 200 {
		t.Fatalf("private reply: %d %s", got.Code, got.Body)
	}
	reader := seedRoleMemberToken(t, h.db, ws, "feedback-granted-reader", "MEMBER", "feedback-granted-reader")
	mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) SELECT id,'user','feedback-granted-reader','read',owner_user_id,datetime('now') FROM pages WHERE slug='three-screens'`)
	got := call(reader, "GET", "/comments", "")
	var snapshot struct {
		Comments []artifactProjectComment `json:"comments"`
	}
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &snapshot) != nil || len(snapshot.Comments) != 1 {
		t.Fatalf("shared comments: %d %s", got.Code, got.Body)
	}
	visible := snapshot.Comments[0]
	if strings.Contains(got.Body.String(), "Unpublished private explanation") || visible.AnsweredVersion != 0 || visible.Status == "answered" || visible.Status == "working" || len(visible.Replies) != 0 {
		t.Fatalf("private reply metadata leaked: %s", got.Body)
	}
	if hidden := call(reader, "GET", "/comments?version=2", ""); hidden.Code != 404 {
		t.Fatalf("comments query exposed unpublished version: %d %s", hidden.Code, hidden.Body)
	}
	for _, suffix := range []string{"/restore", "/share", "/comments", "/comments/" + comment.ID + "/reply"} {
		if denied := call(reader, "POST", suffix, `{}`); denied.Code != 403 {
			t.Errorf("reader write %s: %d %s", suffix, denied.Code, denied.Body)
		}
	}
}
