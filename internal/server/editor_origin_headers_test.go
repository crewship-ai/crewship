package server

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/editororigin"
)

func frameSrcOf(policy string) string {
	for _, d := range strings.Split(policy, ";") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, "frame-src") {
			return d
		}
	}
	return ""
}

func TestEditorOriginWidensFrameSrcOfEveryUIDocumentWhenEnabled(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	const editor = "https://editor.example.test"
	for _, tc := range []struct {
		name, editor, path string
		want               bool
	}{
		{"disabled", "", "/pages/budget", false},
		{"workbook page", editor, "/pages/budget", true},
		// The UI routes on the client, so a document loaded on /chat may
		// later show a workbook Page; the policy belongs to the document.
		{"other ui route", editor, "/chat", true},
		{"root", editor, "/", true},
		{"api", editor, "/api/v1/pages/budget/workbook", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			securityHeadersMiddlewareWith(inner, securityHeaderOptions{editorFrameSrc: tc.editor}).ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
			policy := w.Header().Get("Content-Security-Policy")
			if got := strings.Contains(policy, "editor.example.test"); got != tc.want {
				t.Fatalf("editor in policy = %v: %s", got, policy)
			}
			if tc.want && frameSrcOf(policy) != "frame-src "+editor {
				t.Fatalf("frame-src must name exactly the editor origin: %q", frameSrcOf(policy))
			}
			if !strings.Contains(policy, "frame-ancestors 'none'") || w.Header().Get("X-Frame-Options") != "DENY" {
				t.Fatal("Studio embedding protection removed")
			}
			if strings.Contains(policy, "wasm-unsafe-eval") {
				t.Fatal("editor wasm exception leaked into Studio policy")
			}
		})
	}
	w := httptest.NewRecorder()
	securityHeadersMiddlewareWith(inner, securityHeaderOptions{pageRuntimeOrigin: "https://apps.example.net", editorFrameSrc: editor}).ServeHTTP(w, httptest.NewRequest("GET", "/pages/budget", nil))
	if got := frameSrcOf(w.Header().Get("Content-Security-Policy")); got != "frame-src https://apps.example.net "+editor {
		t.Fatalf("runtime and editor frame-src: %q", got)
	}
}

func TestEditorOriginServesNothingWhileDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.NextjsURL = "https://studio.example.test"
	if newEditorOrigin(cfg, slog.Default()) != nil {
		t.Fatal("editor origin without configuration")
	}
	dir := t.TempDir()
	index := []byte("<!doctype html>")
	sum := sha256.Sum256(index)
	manifest := []byte(`{"files":{"index.html":"` + hex.EncodeToString(sum[:]) + `"}}`)
	pin := sha256.Sum256(manifest)
	digest := hex.EncodeToString(pin[:])
	os.MkdirAll(filepath.Join(dir, digest), 0o755)
	os.WriteFile(filepath.Join(dir, digest, "index.html"), index, 0o644)
	os.WriteFile(filepath.Join(dir, digest, "manifest.json"), manifest, 0o644)
	cfg.GridCraft = config.GridCraftConfig{EditorOrigin: "https://editor.example.test", EditorBundleDir: dir, EditorBundles: []string{digest}}
	main := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
	for _, enabled := range []bool{false, true} {
		cfg.GridCraft.Enabled, cfg.GridCraft.EditorEnabled = enabled, enabled
		gate := editororigin.Gate(newEditorOrigin(cfg, slog.Default()), main)
		r := httptest.NewRequest("GET", "/b/"+digest+"/index.html", nil)
		r.Host = "editor.example.test"
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, r)
		if want := map[bool]int{false: 404, true: 200}[enabled]; w.Code != want {
			t.Fatalf("enabled=%v status %d want %d", enabled, w.Code, want)
		}
	}
}

func TestUnparsableEditorOriginFailsClosed(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.NextjsURL = "https://studio.example.test"
	cfg.GridCraft = config.GridCraftConfig{EditorOrigin: "https://editor.example.test/path?x"}
	main := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
	w := httptest.NewRecorder()
	editororigin.Gate(newEditorOrigin(cfg, slog.Default()), main).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 404 {
		t.Fatalf("misconfigured editor origin let the application through: %d", w.Code)
	}
}

// writeEditorBundle installs a one-file bundle and returns its pin.
func writeEditorBundle(t *testing.T, dir string) string {
	t.Helper()
	index := []byte("<!doctype html>")
	sum := sha256.Sum256(index)
	manifest := []byte(`{"files":{"index.html":"` + hex.EncodeToString(sum[:]) + `"}}`)
	pin := sha256.Sum256(manifest)
	digest := hex.EncodeToString(pin[:])
	if err := os.MkdirAll(filepath.Join(dir, digest), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, digest, "index.html"), index, 0o644)
	os.WriteFile(filepath.Join(dir, digest, "manifest.json"), manifest, 0o644)
	return digest
}

// Zero-config default: with the editor enabled and no dedicated origin, the
// Studio origin serves the editor under its prefix and Studio frames only that
// bundle path, never its own whole origin.
func TestZeroConfigEditorServesUnderStudioPrefix(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.NextjsURL = "https://studio.example.test"
	dir := t.TempDir()
	digest := writeEditorBundle(t, dir)
	cfg.GridCraft = config.GridCraftConfig{EditorBundleDir: dir, EditorBundles: []string{digest}}
	if newEditorOrigin(cfg, slog.Default()) != nil {
		t.Fatal("disabled editor without an origin must change nothing")
	}
	cfg.GridCraft.Enabled, cfg.GridCraft.EditorEnabled = true, true
	editor := newEditorOrigin(cfg, slog.Default())
	if editor == nil {
		t.Fatal("enabled editor without an origin is not served")
	}
	if got := editor.IndexURL(digest); got != "https://studio.example.test/_editor/b/"+digest+"/index.html" {
		t.Fatalf("index URL %q", got)
	}
	opts := editorHeaderOptions(cfg, editor)
	if opts.editorFrameSrc != "https://studio.example.test/_editor/b/" {
		t.Fatalf("frame-src source %q", opts.editorFrameSrc)
	}
	main := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
	handler := editororigin.Gate(editor, securityHeadersMiddlewareWith(main, opts))

	r := httptest.NewRequest("GET", "/_editor/b/"+digest+"/index.html", nil)
	r.Header.Set("Sec-Fetch-Dest", "iframe")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 || !strings.HasSuffix(w.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") || w.Header().Get("X-Frame-Options") != "" {
		t.Fatalf("editor document: %d %v", w.Code, w.Header())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/chat", nil))
	policy := w.Header().Get("Content-Security-Policy")
	if w.Code != 299 || frameSrcOf(policy) != "frame-src https://studio.example.test/_editor/b/" {
		t.Fatalf("Studio document: %d frame-src %q", w.Code, frameSrcOf(policy))
	}
	if !strings.Contains(policy, "frame-ancestors 'none'") || strings.Contains(policy, "wasm-unsafe-eval") {
		t.Fatalf("Studio policy weakened: %s", policy)
	}

	// The editor stays off while either switch is off, even with bundles.
	for _, off := range []func(g *config.GridCraftConfig){
		func(g *config.GridCraftConfig) { g.EditorEnabled = false },
		func(g *config.GridCraftConfig) { g.Enabled = false },
	} {
		c := *cfg
		off(&c.GridCraft)
		if e := newEditorOrigin(&c, slog.Default()); e != nil || editorHeaderOptions(&c, e).editorFrameSrc != "" {
			t.Fatal("same-host editor served while disabled")
		}
	}
}

// A dedicated origin still wins over the zero-config default.
func TestDedicatedEditorOriginOverridesSameHost(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.NextjsURL = "https://studio.example.test"
	cfg.GridCraft = config.GridCraftConfig{Enabled: true, EditorEnabled: true, EditorOrigin: "https://editor.example.org"}
	editor := newEditorOrigin(cfg, slog.Default())
	if got := editorHeaderOptions(cfg, editor).editorFrameSrc; got != "https://editor.example.org" {
		t.Fatalf("frame-src source %q", got)
	}
	main := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
	w := httptest.NewRecorder()
	editororigin.Gate(editor, main).ServeHTTP(w, httptest.NewRequest("GET", "/_editor/b/x/index.html", nil))
	if w.Code != 299 {
		t.Fatalf("same-host prefix served alongside a dedicated origin: %d", w.Code)
	}
}
