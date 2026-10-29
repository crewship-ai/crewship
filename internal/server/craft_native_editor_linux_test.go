package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
)

func TestNativeOnlyEditorServesQualifiedBundleWithoutEnablingC1(t *testing.T) {
	cfg, bundle := nativeEditorConfigFixture(t)
	cfg.CraftNative.Enabled = false
	if newEditorOrigin(cfg, slog.Default()) != nil {
		t.Fatal("native off enabled assets")
	}
	cfg.CraftNative.Enabled = true
	server := New(cfg, slog.Default(), &Deps{DB: openTestDB(t)})
	t.Cleanup(server.StopBackground)
	request := func(path string, header map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "https://studio.example.test"+path, nil)
		req.Header.Set("Sec-Fetch-Dest", "iframe")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		for k, v := range header {
			req.Header.Set(k, v)
		}
		out := httptest.NewRecorder()
		server.httpServer.Handler.ServeHTTP(out, req)
		return out
	}
	path := "/_editor/b/" + bundle + "/index.html"
	out := request(path, nil)
	if out.Code != 200 {
		t.Fatalf("native-only qualified bundle unavailable: %d %s", out.Code, out.Body)
	}
	if cfg.GridCraft.Enabled || cfg.GridCraft.EditorEnabled || cfg.Mooring.Enabled {
		t.Fatal("native flag enabled C1 or old engine")
	}
	csp := out.Header().Get("Content-Security-Policy")
	if !strings.HasSuffix(csp, "sandbox allow-scripts") || strings.Contains(csp, "allow-same-origin") {
		t.Fatalf("native frame lost opaque sandbox: %s", csp)
	}
	if out = request("/_editor/b/"+strings.Repeat("f", 64)+"/index.html", nil); out.Code != 404 {
		t.Fatalf("unknown native bundle %d", out.Code)
	}
	for _, header := range []map[string]string{{"Authorization": "Bearer private-native-fixture"}, {"X-Craft-Session": "private-native-fixture"}, {"Cookie": "crewship_session=private-native-fixture"}} {
		out = request(path, header)
		if out.Code != 403 {
			t.Errorf("credential-bearing native frame must be refused: %d", out.Code)
		}
		if strings.Contains(out.Body.String()+fmt.Sprint(out.Header()), "private-native-fixture") {
			t.Error("credential entered frame response")
		}
	}
	for _, name := range []string{"Authorization", "X-Craft-Session", "Cookie"} {
		req := httptest.NewRequest("GET", "https://studio.example.test"+path, nil)
		req.Header.Set("Sec-Fetch-Dest", "iframe")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header[name] = []string{"", "private-native-fixture"}
		out := httptest.NewRecorder()
		server.httpServer.Handler.ServeHTTP(out, req)
		if out.Code != 403 {
			t.Errorf("empty first credential header hid a later %s: %d", name, out.Code)
		}
	}

}
func nativeEditorModuleBundle(t *testing.T, dir string) string {
	t.Helper()
	files := map[string][]byte{"index.html": []byte(`<!doctype html><script type="module" src="native.js"></script>`), "native.js": []byte(`export const native = true;`), "native.wasm": {0, 97, 115, 109, 1, 0, 0, 0}}
	hashes := map[string]string{}
	for name, body := range files {
		hashes[name] = fmt.Sprintf("%x", sha256.Sum256(body))
	}
	manifest, err := json.Marshal(map[string]any{"files": hashes})
	if err != nil {
		t.Fatal(err)
	}
	pin := fmt.Sprintf("%x", sha256.Sum256(manifest))
	path := filepath.Join(dir, pin)
	if err = os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err = os.WriteFile(filepath.Join(path, name), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(path, "manifest.json"), manifest, 0644); err != nil {
		t.Fatal(err)
	}
	return pin
}

func TestNativeDedicatedEditorModuleAndWASMShareLegacyMountAndRejectCredentials(t *testing.T) {
	cfg, legacyPin := nativeEditorConfigFixture(t)
	nativePin := nativeEditorModuleBundle(t, cfg.CraftNative.EditorBundleDir)
	cfg.Auth.NextjsURL = "https://studio.example.test"
	cfg.GridCraft = config.GridCraftConfig{Enabled: true, EditorEnabled: true, EditorOrigin: "https://editor.example.org", EditorBundleDir: cfg.CraftNative.EditorBundleDir, EditorBundles: []string{legacyPin}}
	profile := cfg.CraftNative.Profiles["wordcraft"]
	profile.EditorBundle = nativePin
	profile.EditorURL = cfg.GridCraft.EditorOrigin + "/b/" + nativePin + "/index.html"
	cfg.CraftNative.Profiles["wordcraft"] = profile
	editor := newEditorOrigin(cfg, slog.Default())
	if editor == nil || !editor.HasBundle(nativePin) || !editor.HasBundle(legacyPin) {
		t.Fatal("native dedicated union dropped a qualified native or existing legacy bundle")
	}
	if _, err := nativeCraftHTTPConfig(cfg, editor); err != nil {
		t.Fatal(err)
	}
	server := New(cfg, slog.Default(), &Deps{DB: openTestDB(t)})
	t.Cleanup(server.StopBackground)
	request := func(host, path, dest, secret string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "https://"+host+path, nil)
		req.Header.Set("Origin", "null")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Sec-Fetch-Dest", dest)
		if secret != "" {
			req.Header.Set("Authorization", secret)
		}
		out := httptest.NewRecorder()
		server.httpServer.Handler.ServeHTTP(out, req)
		return out
	}
	for _, tc := range []struct{ name, dest, contentType string }{{"native.js", "script", "javascript"}, {"native.wasm", "empty", "application/wasm"}} {
		path := "/b/" + nativePin + "/" + tc.name
		out := request("editor.example.org", path, tc.dest, "")
		if out.Code != 200 || out.Header().Get("Access-Control-Allow-Origin") != "*" || out.Header().Get("Cross-Origin-Resource-Policy") != "cross-origin" || !strings.Contains(out.Header().Get("Content-Type"), tc.contentType) || out.Header().Get("Access-Control-Allow-Credentials") != "" || out.Header().Get("Set-Cookie") != "" {
			t.Errorf("native public asset CORS/type invalid: %d %v", out.Code, out.Header())
		}
		out = request("editor.example.org", path, tc.dest, "Bearer private-native-asset")
		if out.Code != 403 || strings.Contains(out.Body.String(), "private-native-asset") || out.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Error("credential-bearing native dedicated asset passed through")
		}
		if out = request("studio.example.test", path, tc.dest, ""); out.Code == 200 {
			t.Error("native dedicated asset escaped onto Studio host")
		}
	}
	for _, pin := range []string{nativePin, legacyPin} {
		out := request("editor.example.org", "/b/"+pin+"/index.html", "iframe", "")
		if out.Code != 200 || out.Header().Get("Access-Control-Allow-Origin") != "" || out.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" || !strings.Contains(out.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") || strings.Contains(out.Header().Get("Content-Security-Policy"), "allow-same-origin") {
			t.Errorf("dedicated frame policy changed: %d %v", out.Code, out.Header())
		}
	}
	// The native additional credential gate must not consume ordinary Studio paths
	// that happen to have the dedicated bundle spelling.
	pass := nativeCraftCredentialGate(cfg, editor, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) }))
	req := httptest.NewRequest("GET", "https://studio.example.test/b/"+nativePin+"/native.js", nil)
	req.Header.Set("Authorization", "ordinary-studio-auth")
	out := httptest.NewRecorder()
	pass.ServeHTTP(out, req)
	if out.Code != 299 {
		t.Error("native gate captured another host's route")
	}
}
