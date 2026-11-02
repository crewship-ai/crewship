package api

import (
	"bufio"
	"context"
	"crypto/sha256"
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
	"github.com/crewship-ai/crewship/internal/mooring"
)

// craftNativeOpenStream opens the real SSE handler for this admission and
// returns a reader for its next event name and payload.
func craftNativeOpenStream(t *testing.T, f *pagesTransferRig, m *craftNativeHTTP, ctx context.Context, token string) func() (string, map[string]any) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bound := context.WithValue(context.WithValue(context.WithValue(r.Context(), ctxUser, UserFromContext(ctx)), ctxAuthKind, AuthKindSession), ctxWorkspaceID, f.wsID)
		r = r.WithContext(auth.WithOriginalCredential(bound, *auth.OriginalCredentialFromContext(ctx)))
		r.SetPathValue("slug", "native-page")
		m.events(w, r)
	}))
	t.Cleanup(server.Close)
	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Craft-Session", token)
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	if response.StatusCode != 200 {
		t.Fatalf("stream: %d", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	return func() (string, map[string]any) {
		t.Helper()
		event := ""
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				event = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				var data map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data); err != nil {
					t.Fatal(err)
				}
				return event, data
			}
		}
		t.Fatalf("stream ended: %v", scanner.Err())
		return "", nil
	}
}

// #4348: removing only write permission retires the open writer admission and
// its lease generation. The same person reaches a read-only view only through a
// FRESH read admission; nothing of the old write authority carries over, and a
// later re-grant does not revive the old generation. Removing read refuses all
// content and a fresh admission.
func TestCraftNativeWriteRevokeKeepsOnlyAFreshReadAdmission(t *testing.T) {
	for _, test := range []struct {
		name     string
		revoke   string
		readKept bool
	}{
		{name: "edit to read", revoke: `UPDATE page_grants SET level='read' WHERE page_id='native-page' AND subject_id='native-scope-writer'`, readKept: true},
		{name: "edit to none", revoke: `DELETE FROM page_grants WHERE page_id='native-page' AND subject_id='native-scope-writer'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, m, _ := craftNativeHTTPFixture(t)
			ctx := craftNativeGrantedWriter(t, f)
			clock := m.clock.(*collaborationAdmissionTestClock)
			cfg := mooring.NativeStoreConfig{Application: "wordcraft", EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: func(context.Context, []byte) error { return nil }}
			d := newCraftNativeDocumentsHTTP(m, map[string]craftNativeApplication{"wordcraft": {Store: cfg, EditorURL: "/workbook-editor/b/" + cfg.EditorBundle + "/index.html"}})
			if _, err := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "wordcraft"}).Apply(t.Context(), artifacts.VersionKey{WorkspaceID: f.wsID, PageID: "native-page"}, artifacts.VersionRequest{BaseVersion: 0, EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: []byte(`{"v":1}`), MediaType: "application/json"}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.userID, nil }); err != nil {
				t.Fatal(err)
			}
			body := func(session, generation, base, revision string) map[string]any {
				raw := []byte(`{"v":"` + revision + `"}`)
				return map[string]any{"workspaceId": f.wsID, "pageId": "native-page", "applicationId": "wordcraft", "enginePin": mooring.NewNativeStore(nil, cfg).EnginePin(), "sessionId": session, "generation": generation, "baseVersion": base, "editorRevision": revision, "contentHash": fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), "bytes": raw}
			}
			versions := func() int {
				var n int
				if err := f.db.QueryRow(`SELECT count(*) FROM artifact_versions`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			oldToken, oldID := craftNativeIssued(t, m, ctx)
			acquired := craftNativeLeaseCall(m, ctx, oldToken, "native-page", "acquire", `{}`)
			if acquired.Code != 200 {
				t.Fatalf("calibration acquire: %d %s", acquired.Code, acquired.Body)
			}
			var lease struct {
				Generation string `json:"generation"`
			}
			if err := json.Unmarshal(acquired.Body.Bytes(), &lease); err != nil {
				t.Fatal(err)
			}
			if w := craftNativeDocumentCall(t, d, ctx, oldToken, "save", body(oldID, lease.Generation, "1", "10:1:1")); w.Code != 200 {
				t.Fatalf("calibration save: %d %s", w.Code, w.Body)
			}
			published := versions()
			next := craftNativeOpenStream(t, f, m, ctx, oldToken)
			if event, first := next(); event != "lease" || first["can_write"] != true {
				t.Fatalf("calibration stream: %s %v", event, first)
			}

			mustExec(t, f.db, test.revoke)

			// The stream retires the admission at its next check and says why,
			// and whether the local draft may stay.
			event, retired := next()
			wantReason, wantDraft := "authority_changed", "purge"
			if test.readKept {
				wantReason, wantDraft = "write_revoked", "retain"
			}
			if event != "session_retired" || retired["delivery"] != "stopped" || retired["reason"] != wantReason || retired["draft"] != wantDraft {
				t.Fatalf("retirement %s %v, want reason %s draft %s", event, retired, wantReason, wantDraft)
			}

			// The old writer admission is retired for every write path.
			for _, operation := range []string{"save", "import", "restore", "reconcile"} {
				candidate := body(oldID, lease.Generation, "2", "10:2:1")
				if operation == "restore" {
					candidate["targetVersion"] = "1"
				}
				if w := craftNativeDocumentCall(t, d, ctx, oldToken, operation, candidate); w.Code != 403 && w.Code != 409 {
					t.Fatalf("revoked writer %s: %d %s", operation, w.Code, w.Body)
				}
			}
			if w := craftNativeLeaseCall(m, ctx, oldToken, "native-page", "renew", `{"generation":"`+lease.Generation+`"}`); w.Code != 403 {
				t.Fatalf("revoked writer renewed: %d %s", w.Code, w.Body)
			}
			if w := craftNativeDocumentCall(t, d, ctx, oldToken, "read", nil); w.Code == 200 {
				t.Fatalf("old admission still delivers content: %s", w.Body)
			}

			fresh := craftNativeSessionCall(m, ctx)
			if !test.readKept {
				if fresh.Code != 403 {
					t.Fatalf("read-revoked person admitted again: %d %s", fresh.Code, fresh.Body)
				}
				if versions() != published {
					t.Fatal("a revoked write published")
				}
				return
			}
			if fresh.Code != 201 {
				t.Fatalf("fresh read admission: %d %s", fresh.Code, fresh.Body)
			}
			var admitted struct {
				Token string `json:"session_token"`
				ID    string `json:"session_id"`
			}
			if err := json.Unmarshal(fresh.Body.Bytes(), &admitted); err != nil {
				t.Fatal(err)
			}
			if read := craftNativeDocumentCall(t, d, ctx, admitted.Token, "read", nil); read.Code != 200 {
				t.Fatalf("fresh reader read: %d %s", read.Code, read.Body)
			}
			view := craftNativeLeaseCall(m, ctx, admitted.Token, "native-page", "read", "")
			var reader struct {
				CanEdit  *bool `json:"can_edit"`
				CanWrite bool  `json:"can_write"`
				Mine     bool  `json:"mine"`
			}
			if err := json.Unmarshal(view.Body.Bytes(), &reader); view.Code != 200 || err != nil || reader.CanEdit == nil || *reader.CanEdit || reader.CanWrite || reader.Mine {
				t.Fatalf("fresh reader lease view: %d %s", view.Code, view.Body)
			}
			if w := craftNativeLeaseCall(m, ctx, admitted.Token, "native-page", "acquire", `{}`); w.Code != 403 {
				t.Fatalf("reader acquired write: %d %s", w.Code, w.Body)
			}
			if w := craftNativeDocumentCall(t, d, ctx, admitted.Token, "save", body(admitted.ID, lease.Generation, "2", "10:3:1")); w.Code != 403 && w.Code != 409 {
				t.Fatalf("reader reused the old generation: %d %s", w.Code, w.Body)
			}
			if versions() != published {
				t.Fatal("a revoked write published")
			}

			// Re-grant write. The old admission stays dead, and the old
			// generation is stale even for the fresh admission.
			mustExec(t, f.db, `UPDATE page_grants SET level='write' WHERE page_id='native-page' AND subject_id='native-scope-writer'`)
			clock.now += time.Minute // the retired lease can no longer be renewed and expires
			if w := craftNativeDocumentCall(t, d, ctx, oldToken, "save", body(oldID, lease.Generation, "2", "10:4:1")); w.Code != 403 && w.Code != 409 {
				t.Fatalf("re-grant revived the old writer: %d %s", w.Code, w.Body)
			}
			// The reader admission captured the read-only epoch; the re-grant moves
			// it, so writing needs yet another fresh admission.
			if w := craftNativeLeaseCall(m, ctx, admitted.Token, "native-page", "acquire", `{}`); w.Code != 403 {
				t.Fatalf("read admission became a writer after re-grant: %d %s", w.Code, w.Body)
			}
			writerToken, writerID := craftNativeIssued(t, m, ctx)
			regained := craftNativeLeaseCall(m, ctx, writerToken, "native-page", "acquire", `{}`)
			var regrant struct {
				Generation string `json:"generation"`
			}
			if err := json.Unmarshal(regained.Body.Bytes(), &regrant); regained.Code != 200 || err != nil || regrant.Generation == lease.Generation {
				t.Fatalf("re-granted acquire: %d %s", regained.Code, regained.Body)
			}
			if w := craftNativeDocumentCall(t, d, ctx, writerToken, "save", body(writerID, lease.Generation, "2", "10:5:1")); w.Code != 409 && w.Code != 403 {
				t.Fatalf("stale generation accepted after re-grant: %d %s", w.Code, w.Body)
			}
			if versions() != published {
				t.Fatal("a stale generation published")
			}
			if w := craftNativeDocumentCall(t, d, ctx, writerToken, "save", body(writerID, regrant.Generation, "2", "10:6:1")); w.Code != 200 {
				t.Fatalf("current generation save after re-grant: %d %s", w.Code, w.Body)
			}
		})
	}
}
