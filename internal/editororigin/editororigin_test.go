package editororigin

import (
	"crypto/sha256"
	"encoding/base64"
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
// operator's install step does and returns the pin: the sha256 of the exact
// manifest.json bytes, which in turn pins every file.
func writeBundle(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	manifest := map[string]any{"source_commit": "0123456789abcdef0123456789abcdef01234567", "files": map[string]string{}}
	for name, body := range files {
		s := sha256.Sum256([]byte(body))
		manifest["files"].(map[string]string)[name] = hex.EncodeToString(s[:])
	}
	raw, _ := json.Marshal(manifest)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	root := filepath.Join(dir, digest)
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return digest
}

const bootstrap = "import init from './app.js';init()"

func scriptHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

func fixture(t *testing.T) (*Handler, string) {
	t.Helper()
	dir := t.TempDir()
	digest := writeBundle(t, dir, map[string]string{
		"index.html":        `<!doctype html><script type="module">` + bootstrap + `</script><script src="app.js"></script>`,
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

// The bundle runs in an opaque origin (CSP sandbox without allow-same-origin),
// so every source names the editor origin explicitly instead of 'self'.
func policy(extraScript string) string {
	return "default-src 'none'; script-src " + editor + " 'wasm-unsafe-eval'" + extraScript + "; style-src " + editor + " 'unsafe-inline'; img-src " + editor + " data: blob:; font-src " + editor + "; connect-src " + editor + "; worker-src 'none'; child-src 'none'; frame-src 'none'; object-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors " + studio + "; sandbox allow-scripts"
}

var wantCSP = policy("")

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
		// Odd variants of the editor host fail closed into the editor handler,
		// never into the application.
		{"other port", "GET", "editor.example.test:8443", "/api/v1/auth/session", 404, false},
		{"trailing dot", "GET", "editor.example.test.", "/api/v1/auth/session", 404, false},
		{"trailing dot and port", "GET", "Editor.Example.Test.:80", "/", 404, false},
		{"wrong host never serves bundle", "GET", "studio.example.test", "/b/" + digest + "/index.html", 299, true},
		{"lookalike host", "GET", "editor.example.test.evil.test", "/b/" + digest + "/index.html", 299, true},
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
			if !tc.main && !strings.HasSuffix(w.Header().Get("Content-Security-Policy"), "; sandbox allow-scripts") {
				t.Fatalf("editor response without sandboxed editor CSP: %q", w.Header().Get("Content-Security-Policy"))
			}
		})
	}
}

func TestEditorOriginHeadersAreExact(t *testing.T) {
	h, digest := fixture(t)
	common := map[string]string{
		"Cross-Origin-Resource-Policy": "same-origin",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Origin-Agent-Cluster":         "?1",
		"Referrer-Policy":              "no-referrer",
		"X-Content-Type-Options":       "nosniff",
		"Strict-Transport-Security":    "max-age=31536000",
		"Permissions-Policy":           "camera=(), microphone=(), geolocation=(), clipboard-read=(), usb=(), serial=(), hid=()",
	}
	for _, tc := range []struct {
		file, contentType, cache, csp, acao string
	}{
		// The document is sandboxed and never readable cross-origin.
		{"index.html", "text/html; charset=utf-8", "no-store", policy(" " + scriptHash(bootstrap)), ""},
		// The opaque-origin document fetches assets in CORS mode; they are
		// public constant code, so any origin may read them without credentials.
		{"app.js", "text/javascript; charset=utf-8", "public, max-age=31536000, immutable", wantCSP, "*"},
		{"app_bg.wasm", "application/wasm", "public, max-age=31536000, immutable", wantCSP, "*"},
		{"assets/font.woff2", "font/woff2", "public, max-age=31536000, immutable", wantCSP, "*"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			// A browser on a sibling subdomain may attach same-site cookies;
			// they must not change anything and nothing may be set back.
			w := serve(h, "GET", "editor.example.test", "/b/"+digest+"/"+tc.file+"?host=authority&parent_origin=x", "Cookie", "authjs.session-token=secret", "Origin", "null")
			if w.Code != 200 {
				t.Fatal(w.Code)
			}
			want := map[string]string{"Content-Type": tc.contentType, "Cache-Control": tc.cache, "Content-Security-Policy": tc.csp}
			for k, v := range common {
				want[k] = v
			}
			if tc.acao != "" {
				want["Access-Control-Allow-Origin"] = tc.acao
			}
			for k, v := range want {
				if got := w.Header().Values(k); len(got) != 1 || got[0] != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
			forbidden := []string{"Set-Cookie", "X-Frame-Options", "Access-Control-Allow-Credentials"}
			if tc.acao == "" {
				forbidden = append(forbidden, "Access-Control-Allow-Origin")
			}
			for _, f := range forbidden {
				if w.Header().Get(f) != "" {
					t.Errorf("unexpected %s", f)
				}
			}
			for _, unsafe := range []string{"'unsafe-eval'", "allow-same-origin", "allow-top-navigation", "allow-popups", "allow-forms", "'self'"} {
				if strings.Contains(w.Header().Get("Content-Security-Policy"), unsafe) {
					t.Errorf("CSP contains %s", unsafe)
				}
			}
		})
	}
	w := serve(h, "GET", "editor.example.test", "/b/"+strings.Repeat("0", 64)+"/index.html")
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Content-Security-Policy") != wantCSP {
		t.Fatalf("404 headers: %v", w.Header())
	}
	if strings.Contains(w.Body.String(), "example.test") {
		t.Fatal("404 echoes configuration")
	}
}

func TestInlineScriptsAreAllowedOnlyByHash(t *testing.T) {
	for _, tc := range []struct {
		name, html string
		want       []string
	}{
		{"trunk module bootstrap", `<html><head><script type="module">` + bootstrap + `</script></head></html>`, []string{scriptHash(bootstrap)}},
		{"external only", `<script src="app.js"></script>`, nil},
		{"two inline, uppercase tag", "<SCRIPT>a()</SCRIPT><script>\nb()\n</script>", []string{scriptHash("a()"), scriptHash("\nb()\n")}},
		{"duplicate once", "<script>a()</script><script>a()</script>", []string{scriptHash("a()")}},
		{"json data is not a script source", `<script type="application/json">{"x":1}</script>`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := inlineScriptHashes([]byte(tc.html))
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("hashes %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEditorCSPGrantsWasmOnlyOnEditorOrigin(t *testing.T) {
	if !strings.Contains(wantCSP, "'wasm-unsafe-eval'") || strings.Contains(wantCSP, "'unsafe-eval'") || strings.Contains(wantCSP, "'unsafe-inline';") && strings.Contains(wantCSP, "script-src 'unsafe-inline'") {
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
		// Rewriting a file together with its manifest line changes the
		// manifest digest, which is the pin.
		{"asset and manifest rewritten consistently", func(t *testing.T, root, digest string) []string {
			body := "evil()"
			os.WriteFile(filepath.Join(root, digest, "app.js"), []byte(body), 0o644)
			raw, _ := os.ReadFile(filepath.Join(root, digest, "manifest.json"))
			var m map[string]any
			json.Unmarshal(raw, &m)
			s := sha256.Sum256([]byte(body))
			m["files"].(map[string]any)["app.js"] = hex.EncodeToString(s[:])
			raw, _ = json.Marshal(m)
			os.WriteFile(filepath.Join(root, digest, "manifest.json"), raw, 0o644)
			return []string{digest}
		}},
		{"manifest reformatted", func(t *testing.T, root, digest string) []string {
			raw, _ := os.ReadFile(filepath.Join(root, digest, "manifest.json"))
			os.WriteFile(filepath.Join(root, digest, "manifest.json"), append(raw, '\n'), 0o644)
			return []string{digest}
		}},
		{"missing manifest", func(t *testing.T, root, digest string) []string {
			os.Remove(filepath.Join(root, digest, "manifest.json"))
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
		{"too many pins", func(t *testing.T, root, digest string) []string {
			return []string{digest, strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64)}
		}},
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
			if w := serve(h, "GET", "editor.example.test", "/b/"+digest+"/index.html"); w.Code != 404 && tc.name != "too many pins" {
				t.Fatalf("unverified bundle served: %d", w.Code)
			}
		})
	}
	root := t.TempDir()
	digest := writeBundle(t, root, map[string]string{"app.js": "1"})
	if _, err := New(Options{Origin: editor, StudioOrigin: studio, BundleDir: root, Bundles: []string{digest}}); err == nil {
		t.Fatal("bundle without index.html accepted")
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
		{"studio host trailing dot", "https://studio.example.test.", runtime, true, false},
		{"trailing dot", "https://editor.example.test.", runtime, true, false},
		{"apex parent of studio", "https://example.test", runtime, true, false},
		{"apex of another site", "https://example.org", runtime, false, false},
		{"parent of page runtime", "https://editor.apps.example.net", "https://x.editor.apps.example.net", false, false},
		{"single label", "https://localhost", runtime, true, false},
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
	// Without a usable Studio origin nothing may frame the editor.
	h, err = New(Options{Origin: editor, StudioOrigin: "not an origin"})
	if h == nil || err == nil {
		t.Fatal("invalid Studio origin must still isolate the host and report")
	}
	if w := serve(Gate(h, main), "GET", "editor.example.test", "/"); w.Code != 404 || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("status %d csp %q", w.Code, w.Header().Get("Content-Security-Policy"))
	}
	// An unparsable editor origin cannot be matched by host; Deny closes everything.
	if w := serve(Gate(Deny(), main), "GET", "studio.example.test", "/"); w.Code != 404 {
		t.Fatalf("deny gate reached main app: %d", w.Code)
	}
}
