package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/diskusage"
	"github.com/crewship-ai/crewship/internal/storageheadroom"
)

func craftNativeVersionReadCall(d *craftNativeDocumentsHTTP, ctx context.Context, token, version string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/pages/native-page/craft/document/versions/"+version, nil).WithContext(ctx)
	r.SetPathValue("slug", "native-page")
	r.SetPathValue("version", version)
	if token != "" {
		r.Header.Set("X-Craft-Session", token)
	}
	w := httptest.NewRecorder()
	d.readVersion(w, r)
	return w
}

type craftNativeVersionDocumentBody struct {
	Version        string `json:"version"`
	CurrentVersion string `json:"currentVersion"`
	Current        bool   `json:"current"`
	CreatedAt      string `json:"createdAt"`
	RestoredFrom   string `json:"restoredFrom"`
	ApplicationID  string `json:"applicationId"`
	EnginePin      string `json:"enginePin"`
	EditorBundle   string `json:"editorBundle"`
	EditorURL      string `json:"editorURL"`
	Bytes          []byte `json:"bytes"`
	Author         struct {
		ActorID string `json:"actorId"`
		Kind    string `json:"kind"`
	} `json:"author"`
}

func decodeCraftNativeVersionDocument(t *testing.T, w *httptest.ResponseRecorder) craftNativeVersionDocumentBody {
	t.Helper()
	var out craftNativeVersionDocumentBody
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("version body %s: %v", w.Body, err)
	}
	return out
}

// craftNativeHistoryState is everything a read must never change: every
// version row, the head (latest and shared pin) and the lease generation.
func craftNativeHistoryState(t *testing.T, db *sql.DB) string {
	t.Helper()
	var versions, latest, shared int64
	var sum string
	if err := db.QueryRow(`SELECT count(*),COALESCE(group_concat(version||':'||manifest_json,'|'),'') FROM artifact_versions WHERE page_id='native-page'`).Scan(&versions, &sum); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT latest_version,shared_version FROM artifact_version_heads WHERE page_id='native-page'`).Scan(&latest, &shared); err != nil {
		t.Fatal(err)
	}
	var leases string
	if err := db.QueryRow(`SELECT COALESCE(group_concat(session_id||':'||generation||':'||expires_ns,'|'),'') FROM mooring_edit_leases WHERE page_id='native-page'`).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal([]any{versions, sum, latest, shared, leases})
	return string(b)
}

// One historical version's bytes are readable by exactly the people who may
// read the current document, with the same status, and the read changes
// nothing: no version, head, shared pin or lease moves. A Read grantee is not
// one of them; the shared preview is their only content.
func TestCraftNativeVersionReadAuthorityMatchesDocumentRead(t *testing.T) {
	f, h, d, owner, ownerToken, _, body := craftNativeDocumentsFixture(t)
	craftNativeEnableVersionHistory(t, f.db, true)
	if w := craftNativeDocumentCall(t, d, owner, ownerToken, "save", body); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	editor := craftNativeVersionsUser(t, f, "native-version-editor", "Editor Eva", true, "write")
	reader := craftNativeVersionsUser(t, f, "native-version-reader", "Reader Rita", true, "read")
	commenter := craftNativeVersionsUser(t, f, "native-version-commenter", "Commenter Cyril", true, "comment")
	outsider := craftNativeVersionsUser(t, f, "native-version-outsider", "Outsider Olga", false, "")
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
	before := craftNativeHistoryState(t, f.db)
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		token  string
		status int
	}{
		{"owner", owner, ownerToken, 200},
		{"editor", editor, issued(editor), 200},
		{"reader", reader, issued(reader), 403},
		{"comment only", commenter, issued(commenter), 403},
		{"non-member", outsider, issued(outsider), 403},
		{"non-member replaying the owner's admission", outsider, ownerToken, 403},
		{"no admission", owner, "", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := craftNativeDocumentCall(t, d, tc.ctx, tc.token, "read", nil)
			old := craftNativeVersionReadCall(d, tc.ctx, tc.token, "1")
			if read.Code != tc.status || old.Code != read.Code {
				t.Fatalf("version read %d %s must equal document read %d (want %d)", old.Code, old.Body, read.Code, tc.status)
			}
			// An absent version answers 404 only to someone who may read; the
			// others get the same refusal, never an existence oracle.
			if missing := craftNativeVersionReadCall(d, tc.ctx, tc.token, "99"); tc.status == 200 && missing.Code != 404 || tc.status != 200 && missing.Code != tc.status {
				t.Fatalf("missing version %d %s", missing.Code, missing.Body)
			}
			if old.Code != 200 {
				if strings.Contains(old.Body.String(), "bytes") || strings.Contains(old.Body.String(), "createdAt") {
					t.Fatalf("refusal leaked a version: %s", old.Body)
				}
				return
			}
			if got := old.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("version cacheable: %q", got)
			}
			v := decodeCraftNativeVersionDocument(t, old)
			if v.Version != "1" || v.CurrentVersion != "2" || v.Current || string(v.Bytes) != `{"v":1}` || v.ApplicationID != "wordcraft" || v.EnginePin == "" || v.EditorBundle != strings.Repeat("c", 64) || !strings.HasSuffix(v.EditorURL, "/b/"+v.EditorBundle+"/index.html") || v.Author.Kind != "human" || v.Author.ActorID != "user:"+f.userID || v.CreatedAt == "" {
				t.Fatalf("version 1: %+v", v)
			}
			head := decodeCraftNativeVersionDocument(t, craftNativeVersionReadCall(d, tc.ctx, tc.token, "2"))
			if head.Version != "2" || !head.Current || string(head.Bytes) != `{"v":2}` {
				t.Fatalf("current version: %+v", head)
			}
		})
	}
	for _, bad := range []string{"0", "-1", "01", "abc", "9223372036854775808"} {
		if w := craftNativeVersionReadCall(d, owner, ownerToken, bad); w.Code != 400 {
			t.Fatalf("version %q: %d %s", bad, w.Code, w.Body)
		}
	}
	if after := craftNativeHistoryState(t, f.db); after != before {
		t.Fatalf("a version read changed history or lease:\n%s\n%s", before, after)
	}
	// An editor whose grant is revoked loses the version on the same admission.
	editorToken := issued(editor)
	if w := craftNativeVersionReadCall(d, editor, editorToken, "1"); w.Code != 200 {
		t.Fatalf("editor before revoke %d %s", w.Code, w.Body)
	}
	mustExec(t, f.db, `DELETE FROM page_grants WHERE page_id='native-page' AND subject_id='native-version-editor'`)
	read := craftNativeDocumentCall(t, d, editor, editorToken, "read", nil)
	if w := craftNativeVersionReadCall(d, editor, editorToken, "1"); w.Code == 200 || w.Code != read.Code || strings.Contains(w.Body.String(), "bytes") {
		t.Fatalf("revoked editor read a version: %d %s (document read %d)", w.Code, w.Body, read.Code)
	}
}

func TestCraftNativeVersionReadIsDefaultOff(t *testing.T) {
	f, _, d, owner, token, _, _ := craftNativeDocumentsFixture(t)
	if w := craftNativeVersionReadCall(d, owner, token, "1"); w.Code != 404 || strings.Contains(w.Body.String(), "bytes") {
		t.Fatalf("undefined flag must keep version reads off: %d %s", w.Code, w.Body)
	}
	craftNativeEnableVersionHistory(t, f.db, true)
	if w := craftNativeVersionReadCall(d, owner, token, "1"); w.Code != 200 {
		t.Fatalf("enabled flag: %d %s", w.Code, w.Body)
	}
	d.human.enabled = false
	if w := craftNativeVersionReadCall(d, owner, token, "1"); w.Code != 503 {
		t.Fatalf("native switch off: %d %s", w.Code, w.Body)
	}
}

// PRD §5: a workspace ADMIN reads another member's history and old versions,
// and every such read is audited exactly like the document read; the owner's
// own reads write no entry, and a failed audit releases nothing.
func TestCraftNativeVersionHistoryAdminReadIsAudited(t *testing.T) {
	for _, tc := range []struct {
		name    string
		private bool
		action  string
	}{
		{"private document", true, "artifact.private_owner_accessed"},
		{"workspace document", false, "artifact.workspace_admin_accessed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, h, d, owner, ownerToken, _, _ := craftNativeDocumentsFixture(t)
			craftNativeEnableVersionHistory(t, f.db, true)
			private := 0
			if tc.private {
				private = 1
			}
			mustExec(t, f.db, `UPDATE pages SET private_audience=? WHERE id='native-page'`, private)
			admin := craftNativeVersionsMember(t, f, "native-version-admin", "Admin Adam", "ADMIN", "")
			audits := func(user string) int {
				var n int
				if err := f.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action IN ('artifact.private_owner_accessed','artifact.workspace_admin_accessed') AND user_id=? AND entity_id='native-page'`, user).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			actionCount := func() int {
				var n int
				if err := f.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action=? AND user_id='native-version-admin' AND entity_id='native-page'`, tc.action).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			adminToken, _ := craftNativeIssued(t, h, admin)
			if w := craftNativeVersionsCall(d, admin, adminToken); w.Code != 200 {
				t.Fatalf("admin list %d %s", w.Code, w.Body)
			}
			if n := actionCount(); n != 1 {
				t.Fatalf("admin listing audited %d times as %s", n, tc.action)
			}
			if w := craftNativeVersionReadCall(d, admin, adminToken, "1"); w.Code != 200 || string(decodeCraftNativeVersionDocument(t, w).Bytes) != `{"v":1}` {
				t.Fatalf("admin version read %d %s", w.Code, w.Body)
			}
			if n := actionCount(); n != 2 {
				t.Fatalf("admin version read audited %d times as %s", n, tc.action)
			}
			// The custodian's own reads are not administrator reads.
			craftNativeVersionsCall(d, owner, ownerToken)
			craftNativeVersionReadCall(d, owner, ownerToken, "1")
			if n := audits(f.userID); n != 0 {
				t.Fatalf("owner read audited %d", n)
			}
			// No audit entry, no history.
			mustExec(t, f.db, `CREATE TRIGGER deny_native_history_audit BEFORE INSERT ON audit_logs WHEN NEW.entity_id='native-page' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
			for name, w := range map[string]*httptest.ResponseRecorder{"list": craftNativeVersionsCall(d, admin, adminToken), "version": craftNativeVersionReadCall(d, admin, adminToken, "1")} {
				if w.Code == 200 || strings.Contains(w.Body.String(), "createdAt") || strings.Contains(w.Body.String(), "bytes") {
					t.Fatalf("%s released without its audit: %d %s", name, w.Code, w.Body)
				}
			}
			// Reading never grants restore: an administrator without Edit rights
			// on a private document cannot even take the lease restore needs.
			if w := craftNativeLeaseCall(h, admin, adminToken, "native-page", "acquire", `{}`); tc.private && w.Code == 200 {
				t.Fatalf("admin read granted the lease: %s", w.Body)
			}
		})
	}
}

// Restore appends a new version with the old content; every newer version
// keeps its exact bytes and stays readable. It is refused under a full disk
// reserve or history quota without writing, while reads keep working.
func TestCraftNativeVersionRestorePreservesNewerVersionsAndStorageLimits(t *testing.T) {
	f, _, d, owner, ownerToken, ownerID, body := craftNativeDocumentsFixture(t)
	craftNativeEnableVersionHistory(t, f.db, true)
	if w := craftNativeDocumentCall(t, d, owner, ownerToken, "save", body); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	restore := func(base, target string) map[string]any {
		candidate := map[string]any{}
		for k, v := range body {
			if k != "bytes" && k != "editorRevision" && k != "contentHash" {
				candidate[k] = v
			}
		}
		candidate["baseVersion"], candidate["targetVersion"], candidate["sessionId"] = base, target, ownerID
		return candidate
	}
	count := func() int {
		var n int
		if err := f.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id='native-page'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Disk reserve: refused as the storage refusal, nothing written; reads work.
	undo := storageheadroom.SetUsageForTest(func(string) (diskusage.Stats, error) { return diskusage.Stats{}, nil })
	w := craftNativeDocumentCall(t, d, owner, ownerToken, "restore", restore("2", "1"))
	if w.Code != 413 || !strings.Contains(w.Body.String(), "artifact_storage_quota") || count() != 2 {
		undo()
		t.Fatalf("restore under a full disk reserve: %d %s, %d versions", w.Code, w.Body, count())
	}
	if r := craftNativeVersionReadCall(d, owner, ownerToken, "1"); r.Code != 200 {
		undo()
		t.Fatalf("a version read must not need write headroom: %d %s", r.Code, r.Body)
	}
	undo()

	// Per-document history quota: same refusal class as a save, nothing written.
	tight := *d
	tight.applications = map[string]craftNativeApplication{}
	for app, p := range d.applications {
		p.Store.MaxHistoryBytes = 1
		tight.applications[app] = p
	}
	if w := craftNativeDocumentCall(t, &tight, owner, ownerToken, "restore", restore("2", "1")); w.Code != 413 || count() != 2 {
		t.Fatalf("restore over the history quota: %d %s, %d versions", w.Code, w.Body, count())
	}

	// Stale base: someone saved meanwhile. Refused, nothing written.
	if w := craftNativeDocumentCall(t, d, owner, ownerToken, "restore", restore("1", "1")); w.Code != 409 || count() != 2 {
		t.Fatalf("restore on a stale base: %d %s", w.Code, w.Body)
	}

	if w := craftNativeDocumentCall(t, d, owner, ownerToken, "restore", restore("2", "1")); w.Code != 200 {
		t.Fatalf("restore %d %s", w.Code, w.Body)
	}
	for _, want := range []struct{ version, bytes, restoredFrom string }{{"1", `{"v":1}`, ""}, {"2", `{"v":2}`, ""}, {"3", `{"v":1}`, "1"}} {
		r := craftNativeVersionReadCall(d, owner, ownerToken, want.version)
		got := decodeCraftNativeVersionDocument(t, r)
		if r.Code != 200 || string(got.Bytes) != want.bytes || got.RestoredFrom != want.restoredFrom || got.Current != (want.version == "3") || got.CurrentVersion != "3" {
			t.Fatalf("after restore, version %s: %d %+v", want.version, r.Code, got)
		}
	}
}

// The whole save → restore → reopen sequence through the HTTP handlers: two
// saves (v2, v3), restore v2 as v4, then every version and the reopened
// document read return exactly the bytes they were saved with.
func TestCraftNativeVersionSaveRestoreReopenKeepsEveryVersion(t *testing.T) {
	f, _, d, owner, ownerToken, ownerID, body := craftNativeDocumentsFixture(t)
	craftNativeEnableVersionHistory(t, f.db, true)
	save := func(base, revision string, raw []byte) {
		t.Helper()
		next := map[string]any{}
		for k, v := range body {
			next[k] = v
		}
		next["baseVersion"], next["editorRevision"], next["contentHash"], next["bytes"] = base, revision, fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), raw
		if w := craftNativeDocumentCall(t, d, owner, ownerToken, "save", next); w.Code != 200 {
			t.Fatalf("save on %s: %d %s", base, w.Code, w.Body)
		}
	}
	save("1", "10:47:3", []byte(`{"v":2}`))
	save("2", "11:47:3", []byte(`{"v":3}`))
	restore := map[string]any{}
	for k, v := range body {
		if k != "bytes" && k != "editorRevision" && k != "contentHash" {
			restore[k] = v
		}
	}
	restore["baseVersion"], restore["targetVersion"], restore["sessionId"] = "3", "2", ownerID
	if w := craftNativeDocumentCall(t, d, owner, ownerToken, "restore", restore); w.Code != 200 || !strings.Contains(w.Body.String(), `"version":"4"`) {
		t.Fatalf("restore v2 on v3: %d %s", w.Code, w.Body)
	}
	for _, want := range []struct{ version, bytes, restoredFrom string }{{"1", `{"v":1}`, ""}, {"2", `{"v":2}`, ""}, {"3", `{"v":3}`, ""}, {"4", `{"v":2}`, "2"}} {
		r := craftNativeVersionReadCall(d, owner, ownerToken, want.version)
		got := decodeCraftNativeVersionDocument(t, r)
		if r.Code != 200 || string(got.Bytes) != want.bytes || got.RestoredFrom != want.restoredFrom || got.CurrentVersion != "4" || got.Current != (want.version == "4") {
			t.Fatalf("version %s: %d %+v", want.version, r.Code, got)
		}
	}
	// Reopen: the document read is the restored head with v2's bytes.
	read := craftNativeDocumentCall(t, d, owner, ownerToken, "read", nil)
	var state struct {
		Version string `json:"version"`
		Bytes   []byte `json:"bytes"`
	}
	if err := json.Unmarshal(read.Body.Bytes(), &state); err != nil || read.Code != 200 || state.Version != "4" || string(state.Bytes) != `{"v":2}` {
		t.Fatalf("reopened document: %d %+v %v", read.Code, state, err)
	}
	// The history lists all four, newest first, nothing removed.
	list := decodeCraftNativeVersions(t, craftNativeVersionsCall(d, owner, ownerToken))
	if list.CurrentVersion != "4" || len(list.Versions) != 4 || list.Versions[0].RestoredFrom != "2" || list.Versions[3].Version != "1" {
		t.Fatalf("history after restore: %+v", list)
	}
}
