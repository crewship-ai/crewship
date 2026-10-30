package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

func TestLocalDraftKeyRecoveryRequiresSameHumanAndCurrentDesignRead(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a", 64))
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "v1")
	f := newDesignAuthorityFixture(t)
	f.addUser(t, "draft-reader")
	f.addUser(t, "draft-admin")
	mustExec(t, f.h.db, `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id='draft-admin'`, f.workspace)
	if w := f.grant(t, "draft-reader", "read"); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := f.call(t, f.owner, "share", `{"version":1}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	router, err := NewRouter(f.h.db, "synthetic-local-draft-router-secret-long-enough", newTestLogger(), WithInternalToken("synthetic-local-draft-internal-token-long-enough"))
	if err != nil {
		t.Fatal(err)
	}
	defer router.Shutdown()
	call := func(user, page string, body any) *httptest.ResponseRecorder {
		scoped := map[string]string{"human": user}
		for field, value := range body.(map[string]string) {
			scoped[field] = value
		}
		raw, _ := json.Marshal(scoped)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/pages/@"+page+"/draft-key", strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Workspace-ID", f.workspace)
		if user != "" {
			req.Header.Set("Authorization", "Bearer "+f.token(t, user))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	wrap := func(user string) string {
		t.Helper()
		w := call(user, f.page, map[string]string{"operation": "wrap", "key": key})
		if w.Code != 200 {
			t.Fatalf("wrap %s: %d %s", user, w.Code, w.Body)
		}
		var v struct {
			Envelope string `json:"envelope"`
		}
		if json.Unmarshal(w.Body.Bytes(), &v) != nil || !strings.HasPrefix(v.Envelope, "v1:") || strings.Contains(w.Body.String(), key) {
			t.Fatalf("not a sealed key: %s", w.Body)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("key response can be cached")
		}
		return v.Envelope
	}
	if w := call(f.owner, f.page, map[string]string{"human": "draft-reader", "operation": "wrap", "key": key}); w.Code != 403 || !strings.Contains(w.Body.String(), "draft_identity_mismatch") {
		t.Fatal("stale client identity was rebound to current login", w.Code, w.Body)
	}
	ownerEnvelope := wrap(f.owner)
	mustExec(t, f.h.db, `UPDATE cli_tokens SET scopes='["pages:read"]' WHERE user_id=?`, f.owner)
	body := map[string]string{"operation": "unwrap", "envelope": ownerEnvelope}
	if w := call(f.owner, f.page, body); w.Code != 200 || !strings.Contains(w.Body.String(), key) {
		t.Fatal("own authorized recovery failed", w.Code, w.Body)
	}
	if w := call("draft-reader", f.page, body); w.Code != 403 || !strings.Contains(w.Body.String(), "draft_identity_mismatch") || strings.Contains(w.Body.String(), key) {
		t.Fatal("other identity recovered private key", w.Code, w.Body)
	}
	if w := call("", f.page, body); w.Code != 401 {
		t.Fatal("unauthenticated recovery", w.Code, w.Body)
	}
	if w := call("draft-admin", f.page, map[string]string{"operation": "wrap", "key": key}); w.Code != 200 {
		t.Fatal("workspace admin could not protect their own Design draft", w.Code, w.Body)
	}
	mustExec(t, f.h.db, `UPDATE pages SET private_audience=1 WHERE id=?`, f.page)
	if w := call("draft-admin", f.page, map[string]string{"operation": "wrap", "key": key}); w.Code != 200 {
		t.Error("admin could not protect own draft of a private Design", w.Code, w.Body)
	}
	mustExec(t, f.h.db, `UPDATE pages SET private_audience=0 WHERE id=?`, f.page)
	readerEnvelope := wrap("draft-reader")
	mustExec(t, f.h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id='draft-reader'`, f.page)
	if w := call("draft-reader", f.page, map[string]string{"operation": "unwrap", "envelope": readerEnvelope}); w.Code != 403 || !strings.Contains(w.Body.String(), "draft_read_revoked") || strings.Contains(w.Body.String(), key) {
		t.Fatal("revoked reader recovered private key", w.Code, w.Body)
	}
	if w := call(f.owner, f.page, map[string]string{"operation": "unwrap", "envelope": ownerEnvelope + "corrupt"}); w.Code != 400 {
		t.Fatal("tampered envelope accepted", w.Code, w.Body)
	}
	if w := call(f.owner, f.page, map[string]string{"operation": "wrap", "key": "not-a-32-byte-key"}); w.Code != 400 {
		t.Fatal("invalid key accepted", w.Code, w.Body)
	}
	t.Setenv("ENCRYPTION_KEY", "")
	if w := call(f.owner, f.page, map[string]string{"operation": "wrap", "key": key}); w.Code != 503 || strings.Contains(w.Body.String(), key) {
		t.Fatal("missing master key did not fail closed", w.Code, w.Body)
	}
}

func TestLocalDraftKeyEnvelopeRequiresAllScopeBindings(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a", 64))
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "v1")
	f := newDesignAuthorityFixture(t)
	router, err := NewRouter(f.h.db, "synthetic-local-draft-bindings-secret-long-enough", newTestLogger(), WithInternalToken("synthetic-local-draft-bindings-internal-long-enough"))
	if err != nil {
		t.Fatal(err)
	}
	defer router.Shutdown()
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	// Even another encrypted value must not inherit omitted scope fields from
	// this request. Only a complete domain-specific key envelope is accepted.
	raw, _ := json.Marshal(map[string]string{"domain": localDraftKeyDomain, "key": key})
	envelope, err := encryption.Encrypt(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"operation": "unwrap", "envelope": envelope, "human": f.owner})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pages/@"+f.page+"/draft-key", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+f.token(t, f.owner))
	req.Header.Set("X-Workspace-ID", f.workspace)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 400 || strings.Contains(w.Body.String(), key) {
		t.Fatal("incomplete binding inherited request authority", w.Code, w.Body)
	}
}

func TestLocalDraftKeyPrivateCustodyAndImmutableScope(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a", 64))
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "v1")
	h, _, _, workspace, owner := newPagesFixture(t)
	page, other := generateCUID(), generateCUID()
	for i, id := range []string{page, other} {
		slug := []string{"private-draft", "other-private"}[i]
		mustExec(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES(?,?,?,'Private draft',?,'{}',1)`, id, workspace, slug, owner)
	}
	token := mintTokenFor(t, h.db, owner, "local-draft-private-custody")
	router, err := NewRouter(h.db, "synthetic-draft-custody-router-secret-long-enough", newTestLogger(), WithInternalToken("synthetic-draft-custody-internal-long-enough"))
	if err != nil {
		t.Fatal(err)
	}
	defer router.Shutdown()
	call := func(ref string, body any) *httptest.ResponseRecorder {
		scoped := map[string]string{"human": owner}
		for field, value := range body.(map[string]string) {
			scoped[field] = value
		}
		raw, _ := json.Marshal(scoped)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/pages/"+ref+"/draft-key", strings.NewReader(string(raw)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Workspace-ID", workspace)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	w := call("@"+page, map[string]string{"operation": "wrap", "key": key})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var wrapped struct {
		Envelope string `json:"envelope"`
	}
	if json.Unmarshal(w.Body.Bytes(), &wrapped) != nil || wrapped.Envelope == "" {
		t.Fatal(w.Body)
	}
	if w := call("private-draft", map[string]string{"operation": "wrap", "key": key}); w.Code != 400 {
		t.Fatal("slug aliases a draft scope", w.Code, w.Body)
	}
	if w := call("@"+other, map[string]string{"operation": "unwrap", "envelope": wrapped.Envelope}); w.Code != 400 {
		t.Fatal("key crossed immutable Page", w.Code, w.Body)
	}
	mustExec(t, h.db, `UPDATE workspace_members SET role='MEMBER',access_mode='restricted' WHERE workspace_id=? AND user_id=?`, workspace, owner)
	if w := call("@"+page, map[string]string{"operation": "unwrap", "envelope": wrapped.Envelope}); w.Code != 200 || !strings.Contains(w.Body.String(), key) {
		t.Fatal("current private custodian could not recover own draft", w.Code, w.Body)
	}
	// Middleware admission alone cannot survive a credential revoked while
	// waiting for the handler's SQL writer fence.
	mustExec(t, h.db, `CREATE TRIGGER draft_revoke_credential AFTER UPDATE ON document_writer_fence BEGIN UPDATE cli_tokens SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'); END`)
	if w := call("@"+page, map[string]string{"operation": "unwrap", "envelope": wrapped.Envelope}); w.Code != 401 || strings.Contains(w.Body.String(), key) {
		t.Fatal("revoked credential released the key", w.Code, w.Body)
	}
}

func TestLocalDraftKeyOperationalFailureNeverProvesRevocation(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a", 64))
	f := newDesignAuthorityFixture(t)
	f.addUser(t, "recovery-reader")
	if w := f.grant(t, "recovery-reader", "read"); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	router, err := NewRouter(f.h.db, "synthetic-draft-fault-router-secret-long-enough", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer router.Shutdown()
	call := func(human string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]string{"human": human, "operation": "wrap", "key": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))})
		req := httptest.NewRequest("POST", "/api/v1/pages/@"+f.page+"/draft-key", strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+f.token(t, "recovery-reader"))
		req.Header.Set("X-Workspace-ID", f.workspace)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	if w := call("recovery-reader"); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	t.Run("ACL storage fault retains draft", func(t *testing.T) {
		mustExec(t, f.h.db, `ALTER TABLE page_grants RENAME TO page_grants_broken`)
		defer mustExec(t, f.h.db, `ALTER TABLE page_grants_broken RENAME TO page_grants`)
		if w := call("recovery-reader"); w.Code < 500 || strings.Contains(w.Body.String(), "draft_read_revoked") {
			t.Fatal("storage error authorized deleting the draft", w.Code, w.Body)
		}
	})
	t.Run("membership removal proves revocation", func(t *testing.T) {
		mustExec(t, f.h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id='recovery-reader'`, f.workspace)
		if w := call("recovery-reader"); w.Code != 403 || !strings.Contains(w.Body.String(), "draft_read_revoked") {
			t.Fatal("removed membership was not reported as proven read loss", w.Code, w.Body)
		}
		if w := call(f.owner); w.Code != 403 || !strings.Contains(w.Body.String(), "draft_identity_mismatch") {
			t.Fatal("another identity could trigger draft deletion", w.Code, w.Body)
		}
		t.Setenv("CREWSHIP_ARTIFACTS_DESIGN_ENABLED", "false")
		if w := call("recovery-reader"); w.Code != 403 || !strings.Contains(w.Body.String(), "draft_read_revoked") {
			t.Fatal("disabled surface masked proven membership removal", w.Code, w.Body)
		}
	})
}

func TestLocalDraftKeyRestrictedCeilingAndMembershipRemoval(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a", 64))
	h, _, _, workspace, owner := newPagesFixture(t)
	page := generateCUID()
	mustExec(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES(?,?,'recovery-restricted','Recovery',?,'{}',0)`, page, workspace, owner)
	mustExec(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, workspace, owner)
	token := mintTokenFor(t, h.db, owner, "draft-key-restricted")
	router, err := NewRouter(h.db, "synthetic-draft-restricted-secret-long-enough", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer router.Shutdown()
	call := func() *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]string{"human": owner, "operation": "wrap", "key": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))})
		req := httptest.NewRequest("POST", "/api/v1/pages/@"+page+"/draft-key", strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Workspace-ID", workspace)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	if w := call(); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	t.Run("restricted ceiling is fresh under fence", func(t *testing.T) {
		mustExec(t, h.db, `CREATE TRIGGER draft_restrict_after_fence AFTER UPDATE ON document_writer_fence BEGIN UPDATE workspace_members SET access_mode='restricted'; END`)
		w := call()
		mustExec(t, h.db, `DROP TRIGGER draft_restrict_after_fence`)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "draft_read_revoked") {
			t.Error("restricted principal recovered ordinary Page key", w.Code, w.Body)
		}
	})
	t.Run("mixed restricted membership still reports revoked target", func(t *testing.T) {
		mustExec(t, h.db, `UPDATE pages SET private_audience=1 WHERE id=?`, page)
		other := generateCUID()
		mustExec(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES(?,'Other','draft-other')`, other)
		mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES(?,?,?,'MEMBER','restricted')`, generateCUID(), other, owner)
		if w := call(); w.Code != 200 {
			t.Fatal("private custodian lost current recovery", w.Code, w.Body)
		}
		mustExec(t, h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, workspace, owner)
		if w := call(); w.Code != 403 || !strings.Contains(w.Body.String(), "draft_read_revoked") {
			t.Fatal("mixed membership hid proven revocation", w.Code, w.Body)
		}
	})
}
