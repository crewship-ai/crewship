package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func TestRestrictedFileRoutesCurrentAuthorityAndMetadata(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	w := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('file-crew',?,'Crew','file-crew')`, w)
	seedAgentRow(t, db, "file-agent", w, "file-crew", "Files", "file-agent", "AGENT")
	store := access.Store{DB: db}
	const secret = "synthetic-file-route-jwt-secret-2026"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, user := range []string{"file-h1", "file-h2"} {
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, user, user+"@file.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, user, w, user)
		execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES(?,?,'file-agent',?,'private')`, user+"-chat", w, user)
		m, err := store.Membership(t.Context(), user, w)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Replace(t.Context(), owner, user, w, "restricted", m, []access.Right{{Kind: "agent", ID: "file-agent", Operation: "run"}}); err != nil {
			t.Fatal(err)
		}
		session, err := sessions.NewDBStore(db).Create(t.Context(), user, "test", "127.0.0.1", auth.RefreshTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		tokens[user], err = validator.IssueAccessToken(user, session.ID, user, user+"@file.test")
		if err != nil {
			t.Fatal(err)
		}
	}
	h, _, err := store.Admit(t.Context(), "file-h1", w, "file-agent", "file-h1-chat", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err := store.SaveFile(t.Context(), h, "private-canary.txt", []byte("PRIVATE_FILE_BYTES"))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteAttempt(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(db, secret, newTestLogger(), WithInternalToken("synthetic-file-route-internal"))
	if err != nil {
		t.Fatal(err)
	}
	request := func(user, chat, suffix string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/chats/"+chat+"/restricted-files"+suffix+"?workspace_id="+w, nil)
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	rec := request("file-h1", "file-h1-chat", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), v.ID) || strings.Contains(rec.Body.String(), "PRIVATE_FILE_BYTES") {
		t.Fatalf("metadata %d %s", rec.Code, rec.Body.String())
	}
	rec = request("file-h1", "file-h1-chat", "/"+v.ID+"/download")
	if rec.Code != 200 || rec.Body.String() != "PRIVATE_FILE_BYTES" || rec.Header().Get("Content-Type") != "application/octet-stream" || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download %d %s %v", rec.Code, rec.Body.String(), rec.Header())
	}
	for _, chat := range []string{"file-h1-chat", "file-h2-chat"} {
		rec = request("file-h2", chat, "/"+v.ID+"/download")
		if rec.Code != 404 || strings.Contains(rec.Body.String(), "CANARY") || strings.Contains(rec.Body.String(), "PRIVATE_FILE") {
			t.Fatalf("foreign output %d %s", rec.Code, rec.Body.String())
		}
	}
	rec = request("file-h2", "file-h2-chat", "")
	if rec.Code != 200 || rec.Body.String() != "{\"files\":[]}\n" {
		t.Fatalf("foreign metadata %d %s", rec.Code, rec.Body.String())
	}
	m, err := store.Membership(t.Context(), "file-h1", w)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "file-h1", w, "restricted", m, nil); err != nil {
		t.Fatal(err)
	}
	if rec = request("file-h1", "file-h1-chat", "/"+v.ID+"/download"); rec.Code != 404 {
		t.Fatalf("revoked download: %d", rec.Code)
	}
}
