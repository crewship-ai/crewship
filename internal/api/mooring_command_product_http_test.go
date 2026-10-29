package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
)

// Real Page/SQL policy and ingress; a native-port fixture and controlled
// response I/O. This is handler qualification, not browser/native evidence.
func TestCommandProductHTTPViewBoundaries(t *testing.T) {
	for _, scenario := range []string{"read", "revoked", "foreign-view", "wrong-page", "cross-origin", "unknown-field", "casefold", "duplicate", "reader-write", "jwt-refresh", "different-session", "revoked-restored", "role-restored", "role-upgrade", "stalled-flush"} {
		t.Run(scenario, func(t *testing.T) {
			f, manager, request := commandProductTestManager(t)
			manager.config.PublicOrigin = "https://studio.example"
			mustExec(t, f.r.db, `DELETE FROM mooring_collab_documents WHERE page_id='admission-page'`)
			port := &commandProjectionFixture{commandProductFixture: commandProductFixture{commandSQLFixturePort: commandSQLFixturePort{done: make(chan struct{})}}, projection: commandProductProjection{Generation: "generation", IDs: []string{"block"}, NativeJSON: `{"body":[]}`}}
			factory, err := newCommandProductFactory(true, manager, func(_ context.Context, runtime string) (commandProductPort, error) {
				port.ref = runtime
				return port, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(factory.Close)
			refresh := func(session string, horizon time.Duration) {
				u := *UserFromContext(request.Context())
				u.SessionID, u.CredentialExpiresAt = session, time.Now().Add(horizon)
				source, err := captureOriginalHumanCredential(request.Context(), f.r.db, &u, AuthKindSession, "")
				if err != nil {
					t.Fatal(err)
				}
				u.OriginalCredential = source
				request = request.WithContext(auth.WithOriginalCredential(context.WithValue(request.Context(), ctxUser, &u), *source))
			}
			if scenario == "jwt-refresh" {
				refresh("admission-source", 2*time.Minute)
			}
			stall := false
			call := func(raw, origin, slug string) *collaborationSessionIOWriter {
				r := httptest.NewRequest("POST", "https://studio.example/command-test", strings.NewReader(raw)).WithContext(request.Context())
				r.SetPathValue("slug", slug)
				r.Header.Set("Origin", origin)
				w := &collaborationSessionIOWriter{ResponseRecorder: httptest.NewRecorder(), fence: manager.fence}
				w.stalledFlush = stall
				manager.fence.Wrap(http.HandlerFunc(factory.serve)).ServeHTTP(w, r)
				return w
			}
			opened := call(`{"action":"open","create":true,"write":false}`, "https://studio.example", "@admission-page")
			if opened.Code != 200 {
				t.Fatalf("open=%d %s", opened.Code, opened.Body.String())
			}
			var response struct {
				View string `json:"view"`
			}
			if json.Unmarshal(opened.Body.Bytes(), &response) != nil || response.View == "" {
				t.Fatal("missing bound view")
			}
			body, _ := json.Marshal(map[string]any{"action": "read", "view": response.View})
			origin, slug, want := "https://studio.example", "@admission-page", 403
			switch scenario {
			case "jwt-refresh":
				refresh("admission-source", 10*time.Minute)
				want = 200
			case "different-session":
				mustExec(t, f.r.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('other-command-session',?,?)`, f.r.userID, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
				refresh("other-command-session", 10*time.Minute)
			case "revoked-restored":
				mustExec(t, f.r.db, `UPDATE user_sessions SET revoked_at='2026-10-26T00:00:00Z' WHERE id='admission-source'`)
				mustExec(t, f.r.db, `UPDATE user_sessions SET revoked_at=NULL WHERE id='admission-source'`)
				refresh("admission-source", 10*time.Minute)
			case "role-restored", "role-upgrade":
				var role string
				if err := f.r.db.QueryRow(`SELECT role FROM workspace_members WHERE workspace_id=? AND user_id=?`, f.r.wsID, f.r.userID).Scan(&role); err != nil {
					t.Fatal(err)
				}
				mustExec(t, f.r.db, `UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, f.r.wsID, f.r.userID)
				if scenario == "role-upgrade" {
					role = "ADMIN"
				}
				mustExec(t, f.r.db, `UPDATE workspace_members SET role=? WHERE workspace_id=? AND user_id=?`, role, f.r.wsID, f.r.userID)
				refresh("admission-source", 10*time.Minute)
			case "stalled-flush":
				stall, want = true, 200
			case "read":
				want = 200
			case "revoked":
				mustExec(t, f.r.db, `UPDATE users SET authority_revision=authority_revision+1 WHERE id=?`, f.r.userID)
			case "foreign-view":
				body = []byte(`{"action":"read","view":"foreign"}`)
			case "wrong-page":
				slug = "@missing-page"
			case "cross-origin":
				origin = "https://other.example"
			case "unknown-field":
				body = []byte(`{"action":"read","view":"` + response.View + `","role":"owner"}`)
				want = 400
			case "casefold":
				body = []byte(`{"ACTION":"read","view":"` + response.View + `"}`)
				want = 400
			case "duplicate":
				body = []byte(`{"action":"read","action":"submit","view":"` + response.View + `"}`)
				want = 400
			case "reader-write":
				body = []byte(`{"action":"submit","view":"` + response.View + `","raw":"{}"}`)
			}
			result := call(string(body), origin, slug)
			if result.Code != want {
				t.Fatalf("got %d want %d: %s", result.Code, want, result.Body.String())
			}
			if want != 200 && strings.Contains(result.Body.String(), "native_json") {
				t.Fatal("denied response released document")
			}
			if want == 200 && (result.observedWrite.IsZero() || time.Until(result.observedWrite) > collaborationHTTPSendDeadline) {
				t.Fatal("physical send exceeded checked horizon")
			}
			if stall && (result.writes != 1 || result.flushes != 1 || result.Body.Len() != 0) {
				t.Fatal("failed flush repeated response or released buffered bytes", result.writes, result.flushes)
			}
			document := factory.views[response.View].document
			document.store.humans.mu.Lock()
			active := len(document.store.humans.admissions)
			document.store.humans.mu.Unlock()
			if active != 0 {
				t.Fatal("completed HTTP requests retained admission slots", active)
			}
		})
	}
}
