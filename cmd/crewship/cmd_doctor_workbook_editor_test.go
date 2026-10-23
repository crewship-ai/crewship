//go:build !clionly

package main

// `crewship doctor` checks the zero-config same-host workbook editor from the
// operator's side: it reads the editor status from /healthz and requests
// /_editor/ variants through the public Studio URL, where a reverse proxy,
// CDN or SSO gate would redirect or normalise them.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/editororigin"
)

// editorStack is a Studio origin (Gate + same-host editor) behind an optional
// front layer, plus a /healthz that reports it like crewshipd does.
func editorStack(t *testing.T, front func(http.Handler) http.Handler, serverCheck string) *httptest.Server {
	t.Helper()
	var studio *httptest.Server
	mux := http.NewServeMux()
	var gated http.Handler
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "workbook_editor": editororigin.Status{Mode: "same-host", URL: studio.URL + "/_editor/b/", ProxyCheck: serverCheck, Disabled: serverCheck == editororigin.ProxyCheckFailed}})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { gated.ServeHTTP(w, r) })
	studio = httptest.NewServer(mux)
	t.Cleanup(studio.Close)
	h, err := editororigin.New(editororigin.Options{SameHost: true, StudioOrigin: studio.URL})
	if err != nil {
		t.Fatal(err)
	}
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("studio")) })
	gated = front(editororigin.Gate(h, app))
	return studio
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
	for _, tc := range []struct {
		name, serverCheck, want, detail string
		front                           func(http.Handler) http.Handler
	}{
		{"passes through the proxy", editororigin.ProxyCheckPassed, "PASS", "", direct},
		{"server self-check still pending", editororigin.ProxyCheckPending, "PASS", "", direct},
		{"redirect in front", editororigin.ProxyCheckPassed, "FAIL", "redirect", sso},
		{"server disabled the editor", editororigin.ProxyCheckFailed, "FAIL", "disabled", direct},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := editorStack(t, tc.front, tc.serverCheck)
			got := checkWorkbookEditorProxy(context.Background(), srv.URL)
			if got.status != tc.want || !strings.Contains(strings.ToLower(got.detail), tc.detail) {
				t.Fatalf("%s: %+v", tc.name, got)
			}
			if got.status == "FAIL" && !strings.Contains(got.hint, "/_editor/") {
				t.Fatalf("FAIL without the proxy rule in the hint: %+v", got)
			}
		})
	}
	// Editor off, dedicated origin, older server or no server: informational.
	for name, body := range map[string]string{
		"editor off": `{"status":"ok"}`,
		"dedicated":  `{"status":"ok","workbook_editor":{"mode":"dedicated","url":"https://editor.example.org"}}`,
		"not json":   `<html>`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		if got := checkWorkbookEditorProxy(context.Background(), srv.URL); got.status != "INFO" {
			t.Errorf("%s: %+v", name, got)
		}
		srv.Close()
	}
	if got := checkWorkbookEditorProxy(context.Background(), "http://127.0.0.1:1"); got.status != "INFO" {
		t.Errorf("unreachable server: %+v", got)
	}
}
