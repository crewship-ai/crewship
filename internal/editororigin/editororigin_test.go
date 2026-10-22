package editororigin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	studio = "https://studio.example.test"
	editor = "https://editor.example.test"
)

// writeBundle lays out <dir>/<digest>/{files,manifest.json} the way the
// operator's install step does and returns the digest of index.html.
func writeBundle(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(files["index.html"]))
	digest := hex.EncodeToString(sum[:])
	root := filepath.Join(dir, digest)
	manifest := map[string]any{"source_commit": "0123456789abcdef0123456789abcdef01234567", "files": map[string]string{}}
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		s := sha256.Sum256([]byte(body))
		manifest["files"].(map[string]string)[name] = hex.EncodeToString(s[:])
	}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return digest
}

func fixture(t *testing.T) (*Handler, string) {
	t.Helper()
	dir := t.TempDir()
	digest := writeBundle(t, dir, map[string]string{
		"index.html":        "<!doctype html><script src=app.js></script>",
		"app.js":            "console.log(1)",
		"app_bg.wasm":       "\x00asm",
		"assets/font.woff2": "font",
	})
	h, err := New(Options{Origin: editor, StudioOrigin: studio, BundleDir: dir, Bundles: []string{digest}})
	if err != nil {
		t.Fatal(err)
	}
	return h, digest
}

func serve(h http.Handler, method, host, path string, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+host+path, nil)
	r.Host = host
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const wantCSP = "default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; worker-src 'none'; child-src 'none'; frame-src 'none'; object-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors " + studio

func TestEditorOriginServesAllowlistedBundleOnlyOnEditorHost(t *testing.T) {
	h, digest := fixture(t)
	main := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("X-Main", "1"); w.WriteHeader(299) })
	gate := Gate(h, main)
	unknown := strings.Repeat("ab", 32)
	for _, tc := range []struct {
		name, method, host, path string
		want                     int
		main                     bool
	}{
		{"index", "GET", "editor.example.test", "/b/" + digest + "/index.html", 200, false},
		{"wasm", "GET", "editor.example.test", "/b/" + digest + "/app_bg.wasm", 200, false},
		{"nested asset", "GET", "editor.example.test", "/b/" + digest + "/assets/font.woff2", 200, false},
		{"head", "HEAD", "editor.example.test", "/b/" + digest + "/app.js", 200, false},
		{"host is case insensitive", "GET", "EDITOR.example.test", "/b/" + digest + "/index.html", 200, false},
		{"explicit default port", "GET", "editor.example.test:443", "/b/" + digest + "/index.html", 200, false},
		{"wrong host never serves bundle", "GET", "studio.example.test", "/b/" + digest + "/index.html", 299, true},
		{"lookalike host", "GET", "editor.example.test.evil.test", "/b/" + digest + "/index.html", 299, true},
		{"other port is another origin", "GET", "editor.example.test:8443", "/b/" + digest + "/index.html", 299, true},
		{"unknown digest", "GET", "editor.example.test", "/b/" + unknown + "/index.html", 404, false},
		{"uppercase digest is not the pin", "GET", "editor.example.test", "/b/" + strings.ToUpper(digest) + "/index.html", 404, false},
		{"manifest is not served", "GET", "editor.example.test", "/b/" + digest + "/manifest.json", 404, false},
		{"unlisted file", "GET", "editor.example.test", "/b/" + digest + "/secret.txt", 404, false},
		{"traversal", "GET", "editor.example.test", "/b/" + digest + "/../" + digest + "/index.html", 404, false},
		{"directory index", "GET", "editor.example.test", "/b/" + digest + "/", 404, false},
		{"api on editor host", "GET", "editor.example.test", "/api/v1/auth/session", 404, false},
		{"spa on editor host", "GET", "editor.example.test", "/", 404, false},
		{"post", "POST", "editor.example.test", "/b/" + digest + "/index.html", 405, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := serve(gate, tc.method, tc.host, tc.path)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d", w.Code, tc.want)
			}
			if (w.Header().Get("X-Main") == "1") != tc.main {
				t.Fatalf("reached main app = %v", !tc.main)
			}
			if !tc.main && w.Header().Get("Content-Security-Policy") != wantCSP {
				t.Fatalf("editor response without editor CSP: %q", w.Header().Get("Content-Security-Policy"))
			}
		})
	}
}

func TestEditorOriginHeadersAreExact(t *testing.T) {
	h, digest := fixture(t)
	common := map[string]string{
		"Content-Security-Policy":      wantCSP,
		"Cross-Origin-Resource-Policy": "same-origin",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Origin-Agent-Cluster":         "?1",
		"Referrer-Policy":              "no-referrer",
		"X-Content-Type-Options":       "nosniff",
		"Permissions-Policy":           "camera=(), microphone=(), geolocation=(), clipboard-read=(), usb=(), serial=(), hid=()",
	}
	for _, tc := range []struct {
		file, contentType, cache string
	}{
		{"index.html", "text/html; charset=utf-8", "no-store"},
		{"app.js", "text/javascript; charset=utf-8", "public, max-age=31536000, immutable"},
		{"app_bg.wasm", "application/wasm", "public, max-age=31536000, immutable"},
		{"assets/font.woff2", "font/woff2", "public, max-age=31536000, immutable"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			// A browser on a sibling subdomain may attach same-site cookies;
			// they must not change anything and nothing may be set back.
			w := serve(h, "GET", "editor.example.test", "/b/"+digest+"/"+tc.file+"?host=authority&parent_origin=x", "Cookie", "authjs.session-token=secret")
			if w.Code != 200 {
				t.Fatal(w.Code)
			}
			want := map[string]string{"Content-Type": tc.contentType, "Cache-Control": tc.cache}
			for k, v := range common {
				want[k] = v
			}
			for k, v := range want {
				if got := w.Header().Values(k); len(got) != 1 || got[0] != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
			for _, forbidden := range []string{"Set-Cookie", "X-Frame-Options", "Access-Control-Allow-Origin", "Access-Control-Allow-Credentials"} {
				if w.Header().Get(forbidden) != "" {
					t.Errorf("unexpected %s", forbidden)
				}
			}
		})
	}
	w := serve(h, "GET", "editor.example.test", "/b/"+strings.Repeat("0", 64)+"/index.html")
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("404 headers: %v", w.Header())
	}
	if strings.Contains(w.Body.String(), "example.test") {
		t.Fatal("404 echoes configuration")
	}
}

func TestEditorCSPGrantsWasmOnlyOnEditorOrigin(t *testing.T) {
	if !strings.Contains(wantCSP, "'wasm-unsafe-eval'") || strings.Contains(wantCSP, "'unsafe-eval'") {
		t.Fatal("fixture policy drifted")
	}
	if got := FrameSrc(""); got != "" {
		t.Fatalf("disabled editor widened frame-src: %q", got)
	}
	if got := FrameSrc("https://Editor.example.test:443/"); got != "https://editor.example.test" {
		t.Fatalf("frame-src source %q", got)
	}
}

func TestBundleIntegrityIsVerifiedBeforeServing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, root, digest string) []string
	}{
		{"index changed after pin", func(t *testing.T, root, digest string) []string {
			os.WriteFile(filepath.Join(root, digest, "index.html"), []byte("tampered"), 0o644)
			return []string{digest}
		}},
		{"asset changed after manifest", func(t *testing.T, root, digest string) []string {
			os.WriteFile(filepath.Join(root, digest, "app.js"), []byte("tampered"), 0o644)
			return []string{digest}
		}},
		{"missing manifest", func(t *testing.T, root, digest string) []string {
			os.Remove(filepath.Join(root, digest, "manifest.json"))
			return []string{digest}
		}},
		{"manifest escapes bundle", func(t *testing.T, root, digest string) []string {
			raw, _ := json.Marshal(map[string]any{"files": map[string]string{"index.html": digest, "../x": digest}})
			os.WriteFile(filepath.Join(root, digest, "manifest.json"), raw, 0o644)
			return []string{digest}
		}},
		{"symlink out of bundle", func(t *testing.T, root, digest string) []string {
			outside := filepath.Join(t.TempDir(), "x")
			os.WriteFile(outside, []byte("console.log(1)"), 0o644)
			os.Remove(filepath.Join(root, digest, "app.js"))
			if err := os.Symlink(outside, filepath.Join(root, digest, "app.js")); err != nil {
				t.Skip(err)
			}
			return []string{digest}
		}},
		{"malformed digest", func(t *testing.T, root, digest string) []string { return []string{"../" + digest} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			digest := writeBundle(t, root, map[string]string{"index.html": "<!doctype html>", "app.js": "console.log(1)"})
			bundles := tc.mutate(t, root, digest)
			h, err := New(Options{Origin: editor, StudioOrigin: studio, BundleDir: root, Bundles: bundles})
			if err == nil {
				t.Fatal("unverified bundle accepted")
			}
			if h == nil {
				t.Fatal("rejected bundle must still leave a host-isolating handler")
			}
			if w := serve(h, "GET", "editor.example.test", "/b/"+digest+"/index.html"); w.Code != 404 {
				t.Fatalf("unverified bundle served: %d", w.Code)
			}
		})
	}
}

func TestValidateOriginRefusesSharedOrigins(t *testing.T) {
	const runtime = "https://apps.example.net"
	for _, tc := range []struct {
		name, editor, runtime string
		devSameSite, ok       bool
	}{
		{"separate site", "https://editor.example.org", runtime, false, true},
		{"sibling needs explicit dev exception", "https://editor.example.test", runtime, false, false},
		{"sibling with dev exception", "https://editor.example.test", runtime, true, true},
		{"equals studio", studio, runtime, true, false},
		{"studio host other port", "https://studio.example.test:8443", runtime, true, false},
		{"studio host case", "https://STUDIO.example.test", runtime, true, false},
		{"equals page runtime", runtime, runtime, false, false},
		{"page runtime host other port", "https://apps.example.net:9443", runtime, false, false},
		{"equals same-origin dev page runtime", studio, studio, true, false},
		{"cleartext", "http://editor.example.org", runtime, false, false},
		{"path", "https://editor.example.org/b", runtime, false, false},
		{"query", "https://editor.example.org?x=1", runtime, false, false},
		{"credentials", "https://u:p@editor.example.org", runtime, false, false},
		{"ip literal", "https://192.0.2.1", runtime, false, false},
		{"empty", "", runtime, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateOrigin(tc.editor, studio, tc.runtime, tc.devSameSite, nil)
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v want ok=%v", err, tc.ok)
			}
		})
	}
	if err := ValidateOrigin("https://editor.example.org", studio, "", false, []string{"https://editor.example.org/"}); err == nil {
		t.Fatal("editor origin accepted as a trusted API caller")
	}
}

func TestDisabledGateStillIsolatesEditorHost(t *testing.T) {
	main := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
	if w := serve(Gate(nil, main), "GET", "studio.example.test", "/"); w.Code != 299 {
		t.Fatal("nil gate changed main app")
	}
	h, err := New(Options{Origin: editor, StudioOrigin: studio})
	if err != nil {
		t.Fatal(err)
	}
	if w := serve(Gate(h, main), "GET", "editor.example.test", "/api/v1/auth/session"); w.Code != 404 {
		t.Fatalf("editor host reached main app: %d", w.Code)
	}
}
