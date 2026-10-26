//go:build linux

package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Positive controller construction requires the real suspend-aware Linux clock;
// no production or test fallback substitutes wall time on unsupported platforms.
// Actual JWT/session middleware and canonical SQL. The deliberately non-runtime
// fixture executable must never run: these are pre-body admission negatives.
func TestMooringHTTPAuthenticatesAndChecksPageBeforeBody(t *testing.T) {
	rig := pagesTransferSetup(t)
	rig.seedPage(t, "http-private", "http-private", "Private", rig.adminID, "")
	v, e := auth.NewJWTValidator("http-mooring-fixture-signing-32characters")
	if e != nil {
		t.Fatal(e)
	}
	store := sessions.NewDBStore(rig.db)
	tokens := map[string]string{}
	for _, user := range []string{rig.adminID, rig.userID} {
		s, e := store.Create(t.Context(), user, "", "", time.Hour)
		if e != nil {
			t.Fatal(e)
		}
		tokens[user], e = v.IssueAccessToken(user, s.ID, "", "")
		if e != nil {
			t.Fatal(e)
		}
	}
	path := filepath.Join(t.TempDir(), "not-a-runtime")
	fixture := []byte("not executable and must not be launched")
	if e = os.WriteFile(path, fixture, 0600); e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(fixture)
	r := &Router{db: rig.db, pages: NewPageHandler(rig.db, nil, newTestLogger()), logger: newTestLogger(), mux: newRouteMux(), authMw: NewAuthMiddleware(v, store, rig.db, newTestLogger()), mooringHTTPConfig: MooringHTTPConfig{Enabled: true, Binary: path, BinarySHA256: hex.EncodeToString(sum[:])}}
	r.registerMooringRoutes()
	if r.mooringHTTP == nil {
		t.Fatal("controller not registered")
	}
	defer r.mooringHTTP.Close()
	registered := registeredPublicRoutes(r)
	spec := loadGeneratedSpec(t)
	if len(registered) != 5 {
		t.Fatalf("enabled transport registered %d paths, want exactly five", len(registered))
	}
	for path, method := range map[string]string{
		"/api/v1/pages/{slug}/mooring/session":             "POST",
		"/api/v1/pages/{slug}/mooring/session/reauthorize": "POST",
		"/api/v1/pages/{slug}/mooring/ws":                  "GET",
		"/api/v1/pages/{slug}/mooring/provenance":          "GET",
		"/api/v1/pages/{slug}/mooring/tasks":               "POST",
	} {
		if len(registered[path]) != 1 || !registered[path][method] || !spec[path][method] {
			t.Fatalf("enabled transport %s: registered=%v spec=%v", path, registered[path], spec[path])
		}
	}
	// Keep these authorization/body negatives on a deadline-capable transport;
	// unsupported response writers are covered by the dedicated I/O regressions.
	for _, tc := range []struct {
		name, token string
		want        int
	}{{"missing-auth", "", 401}, {"foreign-private-page", tokens[rig.userID], 403}} {
		for _, endpoint := range []string{"session", "session/reauthorize"} {
			t.Run(tc.name+"/"+endpoint, func(t *testing.T) {
				body := &collaborationUnreadBody{}
				req := httptest.NewRequest("POST", "/api/v1/pages/http-private/mooring/"+endpoint+"?workspace_id="+rig.wsID, nil)
				req.Body = body
				if tc.token != "" {
					req.Header.Set("Authorization", "Bearer "+tc.token)
				}
				rr := &collaborationSessionIOWriter{ResponseRecorder: httptest.NewRecorder(), fence: r.mooringHTTP.fence}
				r.mux.ServeHTTP(rr, req)
				if rr.Code != tc.want || body.reads != 0 {
					t.Fatalf("status=%d reads=%d", rr.Code, body.reads)
				}
			})
		}
	}
	for _, payload := range []string{`{"write":true,"write":false}`, `{"Write":true}`, `{"write":false,"Write":true}`, `{"role":"OWNER"}`, `{"checkpoint_base64":"e31="}`, `null`, `[]`} {
		req := httptest.NewRequest("POST", "/api/v1/pages/http-private/mooring/session?workspace_id="+rig.wsID, bytes.NewBufferString(payload))
		req.Header.Set("Authorization", "Bearer "+tokens[rig.adminID])
		rr := &collaborationSessionIOWriter{ResponseRecorder: httptest.NewRecorder(), fence: r.mooringHTTP.fence}
		r.mux.ServeHTTP(rr, req)
		if rr.Code != 400 {
			t.Fatalf("untrusted body %q got %d", payload, rr.Code)
		}
	}
	if len(r.mooringHTTP.docs) != 0 {
		t.Fatal("negative request launched runtime")
	}
	if len(r.mutationRoutes) != 3 {
		t.Fatal("missing explicit session/reauthorize/task mutation policies")
	}
	policies := map[string]bool{
		"/api/v1/pages/{slug}/mooring/session":             false,
		"/api/v1/pages/{slug}/mooring/session/reauthorize": false,
		"/api/v1/pages/{slug}/mooring/tasks":               false,
	}
	for _, route := range r.mutationRoutes {
		seen, expected := policies[route.Pattern]
		if !expected || seen || route.Method != "POST" || route.Role != roleSelf || route.Scope != scopeSelf {
			t.Fatal("incorrect or duplicate session/reauthorize/task mutation policy", route)
		}
		policies[route.Pattern] = true
	}
	// The route wrapper stamps before authentication; suspending the very same
	// fence must reject even a still-valid signed human credential before decode.
	r.mooringHTTP.fence.SuspendBeforeRestore()
	for _, endpoint := range []string{"session", "session/reauthorize"} {
		body := &collaborationUnreadBody{}
		req := httptest.NewRequest("POST", "/api/v1/pages/http-private/mooring/"+endpoint+"?workspace_id="+rig.wsID, nil)
		req.Body = body
		req.Header.Set("Authorization", "Bearer "+tokens[rig.adminID])
		rr := &collaborationSessionIOWriter{ResponseRecorder: httptest.NewRecorder(), fence: r.mooringHTTP.fence}
		r.mux.ServeHTTP(rr, req)
		if rr.Code != 503 || body.reads != 0 {
			t.Fatalf("%s suspended HTTP%d reads%d", endpoint, rr.Code, body.reads)
		}
	}
}
