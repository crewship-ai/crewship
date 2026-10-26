//go:build !clionly

package main

// `crewship doctor` checks the zero-config same-host workbook editor from the
// operator's side: it reads the coarse state from /healthz, the per-origin
// detail from the instance-admin endpoint, and requests /_editor/ variants
// through each public Studio origin, where a reverse proxy, CDN or SSO gate
// would redirect or rewrite them.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/editororigin"
)

// editorStack is a zero-config Studio origin (Gate + same-host editor)
// behind a front layer, with a /healthz that reports it like crewshipd does.
// The returned editor has learned the stack's origin from a signed-in
// browser request.
func editorStack(t *testing.T, front func(http.Handler) http.Handler) (*httptest.Server, *editororigin.Handler) {
	t.Helper()
	// The stack serves on loopback, as a development server bound to it.
	h, err := editororigin.New(editororigin.Options{SameHost: true, LoopbackServer: true})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	var gated http.Handler
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "workbook_editor": h.Health()})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { gated.ServeHTTP(w, r) })
	studio := httptest.NewServer(mux)
	t.Cleanup(studio.Close)
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("studio")) })
	gated = front(editororigin.Gate(h, app))
	r := httptest.NewRequest("GET", "/api/v1/me/preferences", nil)
	r.Host = strings.TrimPrefix(studio.URL, "http://")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Referer", studio.URL+"/chat")
	h.LearnOrigin(r, "admin", func() bool { return true })
	return studio, h
}

func stripRouteMarker(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		for k, v := range rec.Header() {
			if !strings.EqualFold(k, editororigin.RouteHeader) {
				w.Header()[k] = v
			}
		}
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes())
	})
}

func adminDetail(h *editororigin.Handler) func(context.Context) (*editororigin.Detail, error) {
	return func(context.Context) (*editororigin.Detail, error) {
		d := h.Detail()
		return &d, nil
	}
}

func TestCheckWorkbookEditorProxy(t *testing.T) {
	direct := func(h http.Handler) http.Handler { return h }
	sso := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/_editor/") {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			h.ServeHTTP(w, r)
		})
	}
	passed := editororigin.ProxyCheck{Status: editororigin.ProxyCheckPassed}
	for _, tc := range []struct {
		name   string
		front  func(http.Handler) http.Handler
		server *editororigin.ProxyCheck // the server's own check of the origin
		want   string
		detail string
	}{
		{"passes from here and on the server", direct, &passed, "PASS", "server self-check passed"},
		// N5: passing from here is not enough while the server's own check
		// has not passed: it is what keeps the editor enabled.
		{"server self-check still pending", direct, nil, "WARN", "pending"},
		{"server self-check could not reach the public URL", direct, &editororigin.ProxyCheck{Status: editororigin.ProxyCheckUnreachable}, "WARN", "unreachable"},
		{"server self-check failed", direct, &editororigin.ProxyCheck{Status: editororigin.ProxyCheckFailed, Cause: editororigin.CauseNotRouted, Problems: []string{"502 at startup"}}, "WARN", "502 at startup"},
		{"redirect in front", sso, &passed, "FAIL", "redirect"},
		// The route marker removed in front: the check cannot tell where
		// a traversal ends up, so it fails closed.
		{"route marker stripped", stripRouteMarker, &passed, "FAIL", "removes the " + editororigin.RouteHeader},
		{"server disabled the editor", direct, &editororigin.ProxyCheck{Status: editororigin.ProxyCheckFailed, Isolation: true, Cause: editororigin.CauseRedirect, Problems: []string{"redirected"}}, "FAIL", "disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, h := editorStack(t, tc.front)
			if tc.server != nil {
				h.RecordCheck(srv.URL, *tc.server)
			}
			got := checkWorkbookEditorProxy(context.Background(), srv.URL, adminDetail(h))
			if got.status != tc.want || !strings.Contains(strings.ToLower(got.detail), strings.ToLower(tc.detail)) {
				t.Fatalf("%s: %+v", tc.name, got)
			}
			if got.status == "FAIL" && !strings.Contains(got.hint, "/_editor/") {
				t.Fatalf("FAIL without the proxy rule in the hint: %+v", got)
			}
		})
	}

	// No browser has used Studio yet: nothing is known, nothing passed.
	h, _ := editororigin.New(editororigin.Options{SameHost: true, LoopbackServer: true})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"workbook_editor": h.Health()})
	}))
	defer srv.Close()
	if got := checkWorkbookEditorProxy(context.Background(), srv.URL, adminDetail(h)); got.status != "WARN" || !strings.Contains(got.detail, "no browser") {
		t.Fatalf("nothing learned: %+v", got)
	}
	// The learned public origin is not reachable from here (https on the
	// --server host, which answers only plain http).
	host := strings.TrimPrefix(srv.URL, "http://")
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = host
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Referer", "https://"+host+"/chat")
	h.LearnOrigin(r, "admin", func() bool { return true })
	if got := checkWorkbookEditorProxy(context.Background(), srv.URL, adminDetail(h)); got.status != "WARN" || !strings.Contains(got.detail, "not reachable from here") {
		t.Fatalf("public URL unreachable from here: %+v", got)
	}

	// Without an instance administrator's login only the coarse state is
	// known: PASS is never claimed.
	for health, want := range map[string]string{
		editororigin.HealthOK:       "INFO",
		editororigin.HealthPending:  "WARN",
		editororigin.HealthFailed:   "WARN",
		editororigin.HealthDisabled: "FAIL",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"workbook_editor": health})
		}))
		for _, detail := range []func(context.Context) (*editororigin.Detail, error){nil, func(context.Context) (*editororigin.Detail, error) { return nil, errEditorDetailForbidden }} {
			if got := checkWorkbookEditorProxy(context.Background(), srv.URL, detail); got.status != want {
				t.Errorf("coarse %s: %+v", health, got)
			}
		}
		srv.Close()
	}

	// Editor off, dedicated origin, older server or no server: informational.
	for name, tc := range map[string]struct {
		body   string
		detail *editororigin.Detail
	}{
		"editor off":    {`{"status":"ok"}`, nil},
		"dedicated":     {`{"status":"ok","workbook_editor":"ok"}`, &editororigin.Detail{Mode: "dedicated", Origin: "https://editor.example.org"}},
		"older server":  {`{"status":"ok","workbook_editor":{"mode":"same-host"}}`, nil},
		"not json":      {`<html>`, nil},
		"off on detail": {`{"status":"ok","workbook_editor":"ok"}`, &editororigin.Detail{Mode: "off"}},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(tc.body)) }))
		detail := func(context.Context) (*editororigin.Detail, error) {
			if tc.detail == nil {
				return nil, errors.New("unexpected")
			}
			return tc.detail, nil
		}
		if got := checkWorkbookEditorProxy(context.Background(), srv.URL, detail); got.status != "INFO" {
			t.Errorf("%s: %+v", name, got)
		}
		srv.Close()
	}
	if got := checkWorkbookEditorProxy(context.Background(), "http://127.0.0.1:1", nil); got.status != "INFO" {
		t.Errorf("unreachable server: %+v", got)
	}
}

// Learned origins come from headers any signed-in user sends. Doctor probes,
// from the operator's machine, only those on the host it was pointed at;
// the others are listed, not requested.
func TestDoctorSkipsLearnedOriginsOnOtherHosts(t *testing.T) {
	var hits atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer foreign.Close()
	srv, h := editorStack(t, func(h http.Handler) http.Handler { return h })
	h.RecordCheck(srv.URL, editororigin.ProxyCheck{Status: editororigin.ProxyCheckPassed})
	// Another host:port than --server, taught by a regular user.
	fhost := strings.Replace(strings.TrimPrefix(foreign.URL, "http://"), "127.0.0.1", "localhost", 1)
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = fhost
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Origin", "http://"+fhost)
	h.LearnOrigin(r, "mallory", func() bool { return false })
	h.RecordCheck("http://"+fhost, editororigin.ProxyCheck{Status: editororigin.ProxyCheckFailed, Isolation: true, Cause: editororigin.CauseRedirect})
	// And one the server has not judged yet, from another user.
	ihost := strings.TrimPrefix(foreign.URL, "http://")
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.Host = ihost
	r2.Header.Set("Sec-Fetch-Site", "same-origin")
	r2.Header.Set("Origin", "http://"+ihost)
	h.LearnOrigin(r2, "mallory2", func() bool { return false })
	got := checkWorkbookEditorProxy(context.Background(), srv.URL, adminDetail(h))
	if hits.Load() != 0 {
		t.Fatalf("doctor probed a learned origin on another host (%d requests)", hits.Load())
	}
	if got.status != "PASS" || !strings.Contains(got.detail, "not probed from here") || !strings.Contains(got.detail, "http://"+fhost+" (learned by user mallory") {
		t.Fatalf("foreign learned origin: %+v", got)
	}
	// Only foreign origins: nothing verified, never PASS.
	only, _ := editororigin.New(editororigin.Options{SameHost: true, LoopbackServer: true})
	only.LearnOrigin(r, "mallory", func() bool { return false })
	if got := checkWorkbookEditorProxy(context.Background(), srv.URL, func(context.Context) (*editororigin.Detail, error) { d := only.Detail(); return &d, nil }); got.status != "WARN" || hits.Load() != 0 {
		t.Fatalf("only foreign origins: %+v (%d requests)", got, hits.Load())
	}
}
