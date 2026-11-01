package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth"
)

// craftNativeVersionsUser adds one person to the fixture and returns a request
// context bound to that person's own original credential.
func craftNativeVersionsUser(t *testing.T, f *pagesTransferRig, id, name string, member bool, grant string) context.Context {
	t.Helper()
	mustExec(t, f.db, `INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, id, id+"@example.test", name)
	if member {
		mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), f.wsID, id)
	}
	if grant != "" {
		mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-page','user',?,?,?)`, id, grant, f.userID)
	}
	expiry := time.Now().Add(time.Hour).UTC()
	mustExec(t, f.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES(?,?,?)`, id+"-session", id, expiry.Format(time.RFC3339Nano))
	u := &AuthUser{ID: id, SessionID: id + "-session", CredentialExpiresAt: expiry}
	source, err := captureOriginalHumanCredential(t.Context(), f.db, u, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	u.OriginalCredential = source
	return auth.WithOriginalCredential(context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxUser, u), ctxAuthKind, AuthKindSession), ctxWorkspaceID, f.wsID), *source)
}

func craftNativeEnableVersionHistory(t *testing.T, db *sql.DB, enabled bool) {
	t.Helper()
	value := 0
	if enabled {
		value = 1
	}
	mustExec(t, db, `INSERT INTO feature_flags(id,key,description,enabled,percentage,created_at,updated_at) VALUES('ff-craft-history',?,'',?,100,datetime('now'),datetime('now')) ON CONFLICT(key) DO UPDATE SET enabled=excluded.enabled`, CraftVersionHistoryFlag, value)
}

func craftNativeVersionsCall(d *craftNativeDocumentsHTTP, ctx context.Context, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/pages/native-page/craft/document/versions", nil).WithContext(ctx)
	r.SetPathValue("slug", "native-page")
	if token != "" {
		r.Header.Set("X-Craft-Session", token)
	}
	w := httptest.NewRecorder()
	d.listVersions(w, r)
	return w
}

type craftNativeVersionsBody struct {
	CurrentVersion string `json:"currentVersion"`
	Truncated      bool   `json:"truncated"`
	Versions       []struct {
		Version      string `json:"version"`
		CreatedAt    string `json:"createdAt"`
		RestoredFrom string `json:"restoredFrom"`
		Current      bool   `json:"current"`
		Author       struct {
			ActorID     string `json:"actorId"`
			Kind        string `json:"kind"`
			DisplayName string `json:"displayName"`
		} `json:"author"`
	} `json:"versions"`
}

func decodeCraftNativeVersions(t *testing.T, w *httptest.ResponseRecorder) craftNativeVersionsBody {
	t.Helper()
	var out craftNativeVersionsBody
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("versions body %s: %v", w.Body, err)
	}
	return out
}

// Listing uses the same native admission and live Read authority as the whole
// document read: every person gets exactly the status that read returns.
func TestCraftNativeVersionsListAuthorityMatchesDocumentRead(t *testing.T) {
	f, h, d, owner, ownerToken, _, _ := craftNativeDocumentsFixture(t)
	craftNativeEnableVersionHistory(t, f.db, true)
	editor := craftNativeVersionsUser(t, f, "native-history-editor", "Editor Eva", true, "write")
	reader := craftNativeVersionsUser(t, f, "native-history-reader", "Reader Rita", true, "read")
	commenter := craftNativeVersionsUser(t, f, "native-history-commenter", "Commenter Cyril", true, "comment")
	outsider := craftNativeVersionsUser(t, f, "native-history-outsider", "Outsider Olga", false, "")
	issued := func(ctx context.Context) string {
		w := craftNativeSessionCall(h, ctx)
		if w.Code != 201 {
			return ""
		}
		var admission struct {
			Token string `json:"session_token"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &admission); err != nil {
			t.Fatal(err)
		}
		return admission.Token
	}
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		token  string
		status int
	}{
		{"owner", owner, ownerToken, 200},
		{"editor", editor, issued(editor), 200},
		{"reader", reader, issued(reader), 200},
		{"comment only", commenter, issued(commenter), 403},
		{"non-member", outsider, issued(outsider), 403},
		{"non-member replaying the owner's admission", outsider, ownerToken, 403},
		{"no admission", owner, "", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := craftNativeDocumentCall(t, d, tc.ctx, tc.token, "read", nil)
			list := craftNativeVersionsCall(d, tc.ctx, tc.token)
			if read.Code != tc.status || list.Code != read.Code {
				t.Fatalf("list %d %s must equal read %d (want %d)", list.Code, list.Body, read.Code, tc.status)
			}
			if list.Code != 200 {
				if strings.Contains(list.Body.String(), "version") && strings.Contains(list.Body.String(), "createdAt") {
					t.Fatalf("refusal leaked history: %s", list.Body)
				}
				return
			}
			if got := list.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("history cacheable: %q", got)
			}
			body := decodeCraftNativeVersions(t, list)
			if body.CurrentVersion != "1" || len(body.Versions) != 1 || !body.Versions[0].Current || body.Versions[0].Author.Kind != "human" || body.Versions[0].Author.ActorID != "user:"+f.userID || body.Versions[0].CreatedAt == "" {
				t.Fatalf("history %+v", body)
			}
		})
	}
	// A reader whose grant is revoked loses the listing on the same admission.
	readerToken := issued(reader)
	mustExec(t, f.db, `DELETE FROM page_grants WHERE page_id='native-page' AND subject_id='native-history-reader'`)
	read := craftNativeDocumentCall(t, d, reader, readerToken, "read", nil)
	if list := craftNativeVersionsCall(d, reader, readerToken); list.Code == 200 || list.Code != read.Code {
		t.Fatalf("revoked reader listed history: list %d read %d", list.Code, read.Code)
	}
}

func TestCraftNativeVersionsListIsDefaultOff(t *testing.T) {
	f, _, d, owner, token, _, _ := craftNativeDocumentsFixture(t)
	if w := craftNativeVersionsCall(d, owner, token); w.Code != 404 {
		t.Fatalf("undefined flag must keep history off: %d %s", w.Code, w.Body)
	}
	craftNativeEnableVersionHistory(t, f.db, false)
	if w := craftNativeVersionsCall(d, owner, token); w.Code != 404 {
		t.Fatalf("disabled flag listed history: %d %s", w.Code, w.Body)
	}
	craftNativeEnableVersionHistory(t, f.db, true)
	if w := craftNativeVersionsCall(d, owner, token); w.Code != 200 {
		t.Fatalf("enabled flag: %d %s", w.Code, w.Body)
	}
	// The flag never bypasses the native instance switch.
	d.human.enabled = false
	if w := craftNativeVersionsCall(d, owner, token); w.Code != 503 {
		t.Fatalf("native switch off: %d %s", w.Code, w.Body)
	}
}

// Restore stays the existing fenced write: lease holder, generation and base
// version, identical to a save. It appends N+1 and deletes nothing.
func TestCraftNativeVersionsRestoreIsFencedAndAppends(t *testing.T) {
	f, h, d, owner, ownerToken, ownerID, body := craftNativeDocumentsFixture(t)
	craftNativeEnableVersionHistory(t, f.db, true)
	if w := craftNativeDocumentCall(t, d, owner, ownerToken, "save", body); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	mustExec(t, f.db, `INSERT INTO agents(id,workspace_id,name,slug) VALUES('native-history-agent',?,'Drafting Agent','drafting-agent')`, f.wsID)
	store := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "wordcraft"})
	if _, err := store.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: f.wsID, PageID: "native-page"}, artifacts.VersionRequest{BaseVersion: 2, EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: []byte(`{"v":3}`), MediaType: "application/json"}}}, func(context.Context, *sql.Tx) (string, error) { return "agent:native-history-agent", nil }); err != nil {
		t.Fatal(err)
	}
	editor := craftNativeVersionsUser(t, f, "native-history-editor", "Editor Eva", true, "write")
	reader := craftNativeVersionsUser(t, f, "native-history-reader", "Reader Rita", true, "read")
	session := func(ctx context.Context) (string, string) { return craftNativeIssued(t, h, ctx) }
	restore := func(base, generation, target, sessionID string) map[string]any {
		candidate := map[string]any{}
		for k, v := range body {
			if k != "bytes" && k != "editorRevision" && k != "contentHash" {
				candidate[k] = v
			}
		}
		candidate["baseVersion"], candidate["generation"], candidate["targetVersion"], candidate["sessionId"] = base, generation, target, sessionID
		return candidate
	}
	readerToken, readerID := session(reader)
	editorToken, editorID := session(editor)
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		token  string
		body   map[string]any
		status int
	}{
		{"reader", reader, readerToken, restore("3", "1", "1", readerID), 403},
		{"editor without the lease", editor, editorToken, restore("3", "1", "1", editorID), 409},
		{"holder with a stale generation", owner, ownerToken, restore("3", "2", "1", ownerID), 409},
		{"holder with a stale base version", owner, ownerToken, restore("2", "1", "1", ownerID), 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := craftNativeDocumentCall(t, d, tc.ctx, tc.token, "restore", tc.body); w.Code != tc.status {
				t.Fatalf("refused restore %d %s, want %d", w.Code, w.Body, tc.status)
			}
			var count int
			if err := f.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id='native-page'`).Scan(&count); err != nil || count != 3 {
				t.Fatalf("refused restore wrote history: %d %v", count, err)
			}
		})
	}
	restored := craftNativeDocumentCall(t, d, owner, ownerToken, "restore", restore("3", "1", "1", ownerID))
	if restored.Code != 200 || !strings.Contains(restored.Body.String(), `"version":"4"`) {
		t.Fatalf("holder restore %d %s", restored.Code, restored.Body)
	}
	read := craftNativeDocumentCall(t, d, reader, readerToken, "read", nil)
	var state struct {
		Version string `json:"version"`
		Bytes   []byte `json:"bytes"`
	}
	if err := json.Unmarshal(read.Body.Bytes(), &state); err != nil || state.Version != "4" || string(state.Bytes) != `{"v":1}` {
		t.Fatalf("restored head must carry version 1 content: %+v %v", state, err)
	}
	for _, viewer := range []struct {
		name  string
		ctx   context.Context
		token string
	}{{"owner", owner, ownerToken}, {"reader", reader, readerToken}} {
		list := craftNativeVersionsCall(d, viewer.ctx, viewer.token)
		if list.Code != 200 {
			t.Fatalf("%s list %d %s", viewer.name, list.Code, list.Body)
		}
		got := decodeCraftNativeVersions(t, list)
		want := []struct{ version, restoredFrom, kind, name string }{{"4", "1", "human", ""}, {"3", "", "agent", "Drafting Agent"}, {"2", "", "human", ""}, {"1", "", "human", ""}}
		if got.CurrentVersion != "4" || len(got.Versions) != len(want) {
			t.Fatalf("%s history %+v", viewer.name, got)
		}
		for i, w := range want {
			v := got.Versions[i]
			if v.Version != w.version || v.RestoredFrom != w.restoredFrom || v.Author.Kind != w.kind || v.Current != (i == 0) || w.name != "" && v.Author.DisplayName != w.name || v.Author.DisplayName == "" {
				t.Fatalf("%s version %d: %+v", viewer.name, i, v)
			}
		}
		if got.Versions[2].Author.ActorID != "user:"+f.userID || got.Versions[0].Author.ActorID != "user:"+f.userID {
			t.Fatalf("restore author: %+v", got.Versions)
		}
	}
}

func TestCraftNativeVersionsListIsBoundedWithTruncation(t *testing.T) {
	f, _, d, owner, token, _, _ := craftNativeDocumentsFixture(t)
	craftNativeEnableVersionHistory(t, f.db, true)
	store := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "wordcraft"})
	for base := int64(1); base < 205; base++ {
		if _, err := store.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: f.wsID, PageID: "native-page"}, artifacts.VersionRequest{BaseVersion: base, EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: []byte(`{"v":` + strconv.FormatInt(base+1, 10) + `}`), MediaType: "application/json"}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.userID, nil }); err != nil {
			t.Fatal(err)
		}
	}
	list := craftNativeVersionsCall(d, owner, token)
	if list.Code != 200 {
		t.Fatal(list.Code, list.Body)
	}
	body := decodeCraftNativeVersions(t, list)
	if len(body.Versions) != craftNativeVersionsLimit || !body.Truncated || body.CurrentVersion != "205" || body.Versions[0].Version != "205" || body.Versions[craftNativeVersionsLimit-1].Version != "6" {
		t.Fatalf("bounded listing: %d truncated %v current %s last %s", len(body.Versions), body.Truncated, body.CurrentVersion, body.Versions[len(body.Versions)-1].Version)
	}
}
