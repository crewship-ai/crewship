package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth"
)

func craftNativePreviewPeer(t *testing.T, f *pagesTransferRig, level string) context.Context {
	t.Helper()
	user := "native-preview-" + level
	mustExec(t, f.db, `INSERT INTO users(id,email) VALUES(?,?)`, user, user+"@example.test")
	mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), f.wsID, user)
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-page','user',?,?,?)`, user, level, f.userID)
	expiry := time.Now().Add(time.Hour).UTC()
	mustExec(t, f.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES(?,?,?)`, user+"-source", user, expiry.Format(time.RFC3339Nano))
	u := &AuthUser{ID: user, SessionID: user + "-source", CredentialExpiresAt: expiry}
	source, err := captureOriginalHumanCredential(t.Context(), f.db, u, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	u.OriginalCredential = source
	return auth.WithOriginalCredential(context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxUser, u), ctxAuthKind, AuthKindSession), ctxWorkspaceID, f.wsID), *source)
}
func craftNativePreviewCall(d *craftNativeDocumentsHTTP, ctx context.Context) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/pages/native-page/craft/preview", nil).WithContext(ctx)
	r.SetPathValue("slug", "native-page")
	w := httptest.NewRecorder()
	d.preview(w, r)
	return w
}
func TestCraftNativePreviewPinnedSharedReadCommentAndEdit(t *testing.T) {
	for _, level := range []string{"read", "comment", "write"} {
		t.Run(level, func(t *testing.T) {
			f, _, d, _, _, _, _ := craftNativeDocumentsFixture(t)
			ctx := craftNativePreviewPeer(t, f, level)
			request := httptest.NewRequest(http.MethodGet, "/api/v1/pages/native-page/craft/availability", nil).WithContext(ctx)
			request.SetPathValue("slug", "native-page")
			available := httptest.NewRecorder()
			d.human.availability(available, request)
			var capability map[string]any
			if available.Code != 200 || json.Unmarshal(available.Body.Bytes(), &capability) != nil || capability["preview_enabled"] != true || level == "comment" && capability["enabled"] != false {
				t.Fatalf("wrong observer availability: %d %s", available.Code, available.Body)
			}

			if w := craftNativePreviewCall(d, ctx); w.Code != 404 {
				t.Fatalf("missing shared pin: %d %s", w.Code, w.Body)
			}
			store := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "wordcraft"})
			k := artifacts.VersionKey{WorkspaceID: f.wsID, PageID: "native-page"}
			allow := func(context.Context, *sql.Tx) (string, error) { return "fixture-owner", nil }
			if err := store.Share(t.Context(), k, 1, 1, allow); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Apply(t.Context(), k, artifacts.VersionRequest{BaseVersion: 1, EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: []byte(`{"private":"version two"}`), MediaType: "application/json"}}}, allow); err != nil {
				t.Fatal(err)
			}
			w := craftNativePreviewCall(d, ctx)
			var snapshot craftNativePreviewResponse
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &snapshot) != nil || snapshot.Version != "1" || string(snapshot.Bytes) != `{"v":1}` {
				t.Fatalf("shared preview leaked latest: %d %s", w.Code, w.Body)
			}
			for _, key := range []string{"quota", "latest", "history", "session_token", "session_id", "generation"} {
				if strings.Contains(w.Body.String(), key) {
					t.Fatalf("preview contains private %s: %s", key, w.Body)
				}
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("private preview is cacheable")
			}
			mustExec(t, f.db, `DELETE FROM page_grants WHERE page_id='native-page' AND subject_id=?`, UserFromContext(ctx).ID)
			if w := craftNativePreviewCall(d, ctx); w.Code != 403 {
				t.Fatalf("revoked preview: %d %s", w.Code, w.Body)
			}
		})
	}
}
func TestCraftNativePreviewRejectsWrongWorkspaceProfileAndCredential(t *testing.T) {
	f, _, d, _, _, _, _ := craftNativeDocumentsFixture(t)
	ctx := craftNativePreviewPeer(t, f, "comment")
	for _, changed := range []context.Context{context.WithValue(ctx, ctxWorkspaceID, "different-workspace"), context.WithValue(ctx, ctxUser, &AuthUser{ID: UserFromContext(ctx).ID})} {
		if w := craftNativePreviewCall(d, changed); w.Code != 403 {
			t.Fatalf("invalid preview authority: %d %s", w.Code, w.Body)
		}
	}
	profile := d.applications["wordcraft"]
	profile.Store.EditorBundle = strings.Repeat("d", 64)
	d.applications["wordcraft"] = profile
	if w := craftNativePreviewCall(d, ctx); w.Code != 403 {
		t.Fatalf("mismatched immutable profile: %d %s", w.Code, w.Body)
	}
	d.human.enabled = false
	if w := craftNativePreviewCall(d, ctx); w.Code != 503 {
		t.Fatalf("disabled preview: %d %s", w.Code, w.Body)
	}
}
func craftNativeShareCall(t *testing.T, d *craftNativeDocumentsHTTP, ctx context.Context, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pages/native-page/craft/document/share", strings.NewReader(string(raw))).WithContext(ctx)
	r.SetPathValue("slug", "native-page")
	r.Header.Set("X-Craft-Session", token)
	w := httptest.NewRecorder()
	d.shareSnapshot(w, r)
	return w
}
func TestCraftNativeShareRequiresCurrentLeaseCASAndExistingVersion(t *testing.T) {
	f, h, d, ctx, token, id, body := craftNativeDocumentsFixture(t)
	share := craftNativeShareBody{WorkspaceID: f.wsID, PageID: "native-page", ApplicationID: "wordcraft", EnginePin: fmt.Sprint(body["enginePin"]), SessionID: id, Generation: "1", BaseVersion: "1", TargetVersion: "1"}
	if w := craftNativeShareCall(t, d, ctx, token, share); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	pin := func() int64 {
		t.Helper()
		var pin int64
		if err := f.db.QueryRow(`SELECT shared_version FROM artifact_version_heads WHERE page_id='native-page'`).Scan(&pin); err != nil {
			t.Fatal(err)
		}
		return pin
	}
	if pin() != 1 {
		t.Fatal("not shared")
	}
	if w := craftNativeDocumentCall(t, d, ctx, token, "save", body); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := craftNativeShareCall(t, d, ctx, token, share); w.Code != 409 {
		t.Fatalf("stale base accepted: %d %s", w.Code, w.Body)
	}
	share.BaseVersion = "2"
	share.TargetVersion = "3"
	if w := craftNativeShareCall(t, d, ctx, token, share); w.Code == 200 {
		t.Fatal("missing version shared")
	}
	if pin() != 1 {
		t.Fatal("refused share changed the pin")
	}
	share.TargetVersion = "2"
	otherToken, otherID := craftNativeIssued(t, h, ctx)
	share.SessionID = otherID
	if w := craftNativeShareCall(t, d, ctx, otherToken, share); w.Code != 409 {
		t.Fatalf("other tab shared: %d %s", w.Code, w.Body)
	}
	if pin() != 1 {
		t.Fatal("nonholder changed pin")
	}
	share.SessionID = id
	if w := craftNativeShareCall(t, d, ctx, token, share); w.Code != 200 || pin() != 2 {
		t.Fatalf("writer share: %d %s", w.Code, w.Body)
	}
	// Downgrade and ABA cannot keep the captured write admission alive.
	mustExec(t, f.db, `UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=?`, f.wsID)
	if w := craftNativeShareCall(t, d, ctx, token, share); w.Code != 403 {
		t.Fatalf("revoked admission shared: %d %s", w.Code, w.Body)
	}
	if pin() != 2 {
		t.Fatal("revoked share changed pin")
	}
}

func TestCraftNativeShareFinalCredentialRevocationRollsBackPin(t *testing.T) {
	f, _, d, ctx, token, id, body := craftNativeDocumentsFixture(t)
	mustExec(t, f.db, `CREATE TRIGGER native_share_revoke_after_pin AFTER UPDATE OF shared_version ON artifact_version_heads BEGIN UPDATE user_sessions SET revoked_at='2026-10-28T00:00:00Z' WHERE id='native-source'; END`)
	share := craftNativeShareBody{WorkspaceID: f.wsID, PageID: "native-page", ApplicationID: "wordcraft", EnginePin: fmt.Sprint(body["enginePin"]), SessionID: id, Generation: "1", BaseVersion: "1", TargetVersion: "1"}
	if w := craftNativeShareCall(t, d, ctx, token, share); w.Code != 403 {
		t.Fatalf("late revoked credential shared: %d %s", w.Code, w.Body)
	}
	var pin int64
	if err := f.db.QueryRow(`SELECT shared_version FROM artifact_version_heads WHERE page_id='native-page'`).Scan(&pin); err != nil || pin != 0 {
		t.Fatalf("refused share left pin %d: %v", pin, err)
	}
}
func TestCraftNativePreviewRejectsEpochChangeAtWriterFence(t *testing.T) {
	f, _, d, _, _, _, _ := craftNativeDocumentsFixture(t)
	ctx := craftNativePreviewPeer(t, f, "comment")
	store := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "wordcraft"})
	if err := store.Share(t.Context(), artifacts.VersionKey{WorkspaceID: f.wsID, PageID: "native-page"}, 1, 1, func(context.Context, *sql.Tx) (string, error) { return "fixture-owner", nil }); err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.db, `CREATE TRIGGER native_preview_revoke_at_fence AFTER UPDATE ON document_writer_fence BEGIN UPDATE workspaces SET authority_revision=authority_revision+1; END`)
	if w := craftNativePreviewCall(d, ctx); w.Code != 403 {
		t.Fatalf("preflight permission reused after epoch change: %d %s", w.Code, w.Body)
	}
}
func TestCraftNativeReadAndCommentObserversCannotMutate(t *testing.T) {
	f, h, d, _, _, _, body := craftNativeDocumentsFixture(t)
	ctx := craftNativePreviewPeer(t, f, "comment")
	// Existing Read plus Comment permits legacy observer admission; neither grant
	// is Edit, so even a valid observer token must fail every mutation boundary.
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-page','user',?,'read',?)`, UserFromContext(ctx).ID, f.userID)
	token, id := craftNativeIssued(t, h, ctx)
	for _, op := range []string{"acquire", "renew", "release", "request", "handoff"} {
		if w := craftNativeLeaseCall(h, ctx, token, "native-page", op, `{"generation":"1","request_id":"missing"}`); w.Code != 403 {
			t.Fatalf("observer %s got %d %s", op, w.Code, w.Body)
		}
	}
	body["sessionId"] = id
	body["targetVersion"] = "1"
	for _, op := range []string{"save", "import", "restore", "reconcile"} {
		if w := craftNativeDocumentCall(t, d, ctx, token, op, body); w.Code != 403 {
			t.Fatalf("observer %s got %d %s", op, w.Code, w.Body)
		}
	}
	share := craftNativeShareBody{WorkspaceID: f.wsID, PageID: "native-page", ApplicationID: "wordcraft", EnginePin: fmt.Sprint(body["enginePin"]), SessionID: id, Generation: "1", BaseVersion: "1", TargetVersion: "1"}
	if w := craftNativeShareCall(t, d, ctx, token, share); w.Code != 403 {
		t.Fatalf("observer share got %d %s", w.Code, w.Body)
	}
	var latest, shared int64
	if err := f.db.QueryRow(`SELECT latest_version,shared_version FROM artifact_version_heads WHERE page_id='native-page'`).Scan(&latest, &shared); err != nil || latest != 1 || shared != 0 {
		t.Fatalf("observer changed versions %d/%d %v", latest, shared, err)
	}
}

func TestCraftNativeShareRequiresAtomicAudit(t *testing.T) {
	f, h, d, ctx, token, id, body := craftNativeDocumentsFixture(t)
	share := craftNativeShareBody{WorkspaceID: f.wsID, PageID: "native-page", ApplicationID: "wordcraft", EnginePin: fmt.Sprint(body["enginePin"]), SessionID: id, Generation: "1", BaseVersion: "1", TargetVersion: "1"}
	var privateBefore bool
	if err := f.db.QueryRow(`SELECT private_audience FROM pages WHERE id='native-page'`).Scan(&privateBefore); err != nil {
		t.Fatal(err)
	}
	h.pages.SetJournal(artifactProjectUnchainedEmitter{})
	if w := craftNativeShareCall(t, d, ctx, token, share); w.Code != 500 {
		t.Fatalf("publication without atomic audit acknowledged: %d %s", w.Code, w.Body)
	}
	check := func(wantPin, wantAudit int) {
		t.Helper()
		var pin, audits, entries int
		var private bool
		if err := f.db.QueryRow(`SELECT v.shared_version,p.private_audience,(SELECT count(*) FROM audit_logs WHERE action='craft.native_preview_shared'),(SELECT count(*) FROM journal_entries WHERE entry_type='craft.native_preview_shared') FROM artifact_version_heads v JOIN pages p ON p.id=v.page_id WHERE p.id='native-page'`).Scan(&pin, &private, &audits, &entries); err != nil || pin != wantPin || audits != wantAudit || entries != wantAudit || private != privateBefore {
			t.Fatalf("publish pin/audience/audit/journal %d/%v/%d/%d want %d/%v/%d/%d: %v", pin, private, audits, entries, wantPin, privateBefore, wantAudit, wantAudit, err)
		}
	}
	check(0, 0)
	h.pages.SetJournal(nil)
	if w := craftNativeShareCall(t, d, ctx, token, share); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	check(1, 1)
	var actor string
	if err := f.db.QueryRow(`SELECT actor_id FROM journal_entries WHERE entry_type='craft.native_preview_shared'`).Scan(&actor); err != nil || actor != f.userID {
		t.Fatalf("untrusted publish actor %q %v", actor, err)
	}
}
