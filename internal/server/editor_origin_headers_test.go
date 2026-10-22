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

func TestEditorOriginWidensFrameSrcOnlyOnPagesWhenEnabled(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	const editor = "https://editor.example.test"
	for _, tc := range []struct {
		name, editor, path string
		want               bool
	}{
		{"disabled", "", "/pages/budget", false},
		{"workbook page", editor, "/pages/budget", true},
		{"other ui route", editor, "/chat", false},
		{"pages index", editor, "/pages", false},
		{"api", editor, "/api/v1/pages/budget/workbook", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			securityHeadersMiddlewareWith(inner, securityHeaderOptions{editorOrigin: tc.editor}).ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
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
	securityHeadersMiddlewareWith(inner, securityHeaderOptions{pageRuntimeOrigin: "https://apps.example.net", editorOrigin: editor}).ServeHTTP(w, httptest.NewRequest("GET", "/pages/budget", nil))
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
	digest := hex.EncodeToString(sum[:])
	os.MkdirAll(filepath.Join(dir, digest), 0o755)
	os.WriteFile(filepath.Join(dir, digest, "index.html"), index, 0o644)
	os.WriteFile(filepath.Join(dir, digest, "manifest.json"), []byte(`{"files":{"index.html":"`+digest+`"}}`), 0o644)
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
