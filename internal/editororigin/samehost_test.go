package editororigin

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// Zero-config default: without a dedicated origin the editor is served under
// SameHostPrefix on the Studio origin. Isolation then rests on the opaque
// origin (CSP sandbox), on CSP sources scoped to the bundle path and on
// refusing any request that a credentialless, sandboxed frame would not make.

const sameHostSelf = studio + "/_editor/b/"

func sameHostFixture(t *testing.T) (*Handler, string) {
	t.Helper()
	dir := t.TempDir()
	digest := writeBundle(t, dir, map[string]string{
		"index.html":  `<!doctype html><script type="module">` + bootstrap + `</script><script src="app.js"></script>`,
		"app.js":      "console.log(1)",
		"app_bg.wasm": "\x00asm",
	})
	h, err := New(Options{SameHost: true, StudioOrigin: studio, BundleDir: dir, Bundles: []string{digest}})
	if err != nil {
		t.Fatal(err)
	}
	return h, digest
}

// A credentialless sandboxed iframe framed by Studio sends no cookies; its
// navigation is same-origin (Studio frames it) or cross-site (it navigates
// itself from the opaque origin); its subresource requests are cross-site.
var (
	frameNav     = []string{"Sec-Fetch-Site", "same-origin", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "iframe"}
	frameSelfNav = []string{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "iframe"}
	frameScript  = []string{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "cors", "Sec-Fetch-Dest", "script"}
	frameFetch   = []string{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "cors", "Sec-Fetch-Dest", "empty"}
)

func with(base []string, extra ...string) []string {
	return append(append([]string{}, base...), extra...)
}

func TestSameHostServesBundleOnlyUnderPrefix(t *testing.T) {
	h, digest := sameHostFixture(t)
	main := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("X-Main", "1"); w.WriteHeader(299) })
	gate := Gate(h, main)
	index := "/_editor/b/" + digest + "/index.html"
	for _, tc := range []struct {
		name, method, path string
		header             []string
		want               int
		main               bool
	}{
		{"index framed by studio", "GET", index, frameNav, 200, false},
		{"index after self-navigation", "GET", index, frameSelfNav, 200, false},
		{"module script", "GET", "/_editor/b/" + digest + "/app.js", frameScript, 200, false},
		{"wasm fetch", "GET", "/_editor/b/" + digest + "/app_bg.wasm", frameFetch, 200, false},
		{"head", "HEAD", "/_editor/b/" + digest + "/app.js", frameScript, 200, false},
		// Every other path stays with the application; the prefix never does.
		{"studio root", "GET", "/", nil, 299, true},
		{"studio api", "GET", "/api/v1/auth/session", nil, 299, true},
		{"dedicated bundle path is not served on studio", "GET", "/b/" + digest + "/index.html", frameNav, 299, true},
		{"lookalike prefix", "GET", "/_editorx/b/" + digest + "/index.html", frameNav, 299, true},
		// Path confusion inside the prefix fails closed in the editor handler
		// (404 under the sandboxed policy), never in the application.
		{"bare prefix", "GET", "/_editor", nil, 404, false},
		{"prefix root", "GET", "/_editor/", nil, 404, false},
		{"uppercase prefix", "GET", "/_EDITOR/b/" + digest + "/index.html", frameNav, 404, false},
		{"encoded prefix", "GET", "/%5Feditor/b/" + digest + "/index.html", frameNav, 404, false},
		{"encoded slash after prefix", "GET", "/_editor%2Fb/" + digest + "/index.html", frameNav, 404, false},
		{"encoded traversal to api", "GET", "/_editor/b/..%2F..%2Fapi/v1/auth/session", frameSelfNav, 404, false},
		{"encoded dot segments", "GET", "/_editor/b/%2e%2e/%2e%2e/api/v1/auth/session", frameSelfNav, 404, false},
		{"backslash", "GET", "/_editor/b/..%5C..%5Capi", frameSelfNav, 404, false},
		{"double slash", "GET", "/_editor//b/" + digest + "/index.html", frameNav, 404, false},
		{"traversal inside bundle", "GET", "/_editor/b/" + digest + "/../" + digest + "/index.html", frameNav, 404, false},
		{"manifest", "GET", "/_editor/b/" + digest + "/manifest.json", frameFetch, 404, false},
		{"unknown digest", "GET", "/_editor/b/" + strings.Repeat("ab", 32) + "/index.html", frameNav, 404, false},
		{"post", "POST", index, frameNav, 405, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := serve(gate, tc.method, "studio.example.test", tc.path, tc.header...)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d", w.Code, tc.want)
			}
			if (w.Header().Get("X-Main") == "1") != tc.main {
				t.Fatalf("reached main app = %v", !tc.main)
			}
			if !tc.main {
				if !strings.HasSuffix(w.Header().Get("Content-Security-Policy"), "; sandbox allow-scripts") {
					t.Fatalf("editor response without sandboxed policy: %q", w.Header().Get("Content-Security-Policy"))
				}
				if w.Header().Get("Location") != "" {
					t.Fatal("editor prefix must never redirect: CSP drops path scoping after a redirect")
				}
			}
		})
	}
	// The prefix is served on whatever host reaches the server: the gate does
	// not depend on proxies preserving Host.
	if w := serve(gate, "GET", "internal-upstream:8080", index, frameNav...); w.Code != 200 {
		t.Fatalf("prefix depends on Host: %d", w.Code)
	}
}

func TestSameHostRefusesRequestsASandboxedCredentiallessFrameWouldNotMake(t *testing.T) {
	h, digest := sameHostFixture(t)
	index := "/_editor/b/" + digest + "/index.html"
	asset := "/_editor/b/" + digest + "/app.js"
	for _, tc := range []struct {
		name, path string
		header     []string
	}{
		// A frame without the credentialless attribute carries Studio's
		// cookies; refusing it keeps the attribute mandatory even where
		// Studio's COEP is missing (a proxy strips it, another browser).
		{"index with studio session cookie", index, with(frameNav, "Cookie", "__Secure-authjs.session-token=s")},
		{"index with studio session cookie over http", index, with(frameNav, "Cookie", "authjs.session-token=s")},
		{"index with studio csrf cookie", index, with(frameNav, "Cookie", "__Host-authjs.csrf-token=c")},
		{"index with studio csrf cookie over http", index, with(frameNav, "Cookie", "authjs.csrf-token=c")},
		{"index with studio refresh cookie", index, with(frameNav, "Cookie", "__Secure-authjs.refresh-token=r")},
		{"index with legacy session cookie", index, with(frameNav, "Cookie", "next-auth.session-token=s")},
		{"index with studio ui cookie", index, with(frameNav, "Cookie", "sidebar_state=true")},
		{"index with studio cookie among proxy cookies", index, with(frameNav, "Cookie", "AWSALBCORS=a; __cf_bm=b;authjs.session-token=s")},
		{"index with studio cookie in a second header", index, with(frameNav, "Cookie", "AWSALBCORS=a", "Cookie", " __Secure-authjs.session-token=s")},
		{"index opened top-level", index, []string{"Sec-Fetch-Site", "none", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"}},
		{"index without fetch metadata", index, nil},
		{"index as script", index, frameScript},
		// Studio itself must never load the foreign code into its origin.
		{"asset from studio document", asset, []string{"Sec-Fetch-Site", "same-origin", "Sec-Fetch-Mode", "no-cors", "Sec-Fetch-Dest", "script"}},
		{"asset same-site", asset, []string{"Sec-Fetch-Site", "same-site", "Sec-Fetch-Mode", "no-cors", "Sec-Fetch-Dest", "script"}},
		{"asset typed url", asset, []string{"Sec-Fetch-Site", "none", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"}},
		{"asset without fetch metadata", asset, nil},
		{"asset as frame document", asset, frameSelfNav},
		{"asset with studio cookie", asset, with(frameScript, "Cookie", "__Secure-authjs.session-token=s")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := serve(h, "GET", "studio.example.test", tc.path, tc.header...)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403", w.Code)
			}
			for k, v := range map[string]string{
				"Cache-Control":                "no-store",
				"Cross-Origin-Resource-Policy": "same-origin",
				"Vary":                         "Cookie, Sec-Fetch-Site, Sec-Fetch-Dest",
			} {
				if got := w.Header().Get(k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
			if w.Header().Get("Access-Control-Allow-Origin") != "" || strings.Contains(w.Body.String(), "console.log") || strings.Contains(w.Body.String(), "studio.example.test") {
				t.Fatal("refusal leaked bundle content or configuration")
			}
		})
	}
}

// A proxy, load balancer or CDN in front of Crewship may set its own
// SameSite=None cookies on editor responses (ALB stickiness, bot management).
// The credentialless frame's ephemeral jar is shared by the whole Studio
// document, so a reopened editor sends them back. Only Crewship's own cookies
// prove the frame lost the credentialless attribute; other cookies must not
// break reopening the editor in the same tab.
func TestSameHostIgnoresThirdPartyCookies(t *testing.T) {
	h, digest := sameHostFixture(t)
	for _, cookie := range []string{
		"AWSALBCORS=abc",
		"AWSALB=abc; AWSALBCORS=abc",
		"__cf_bm=abc; __cflb=def",
		"LBSTICKY=abc",
		"INGRESSCOOKIE=1; route=sticky",
		// Names that merely contain Crewship's are not Crewship's.
		"xauthjs.session-token=1; my_sidebar_state=1",
		"malformed; =x; ;",
	} {
		for _, tc := range []struct {
			file   string
			header []string
		}{{"index.html", frameNav}, {"app.js", frameScript}} {
			w := serve(h, "GET", "studio.example.test", "/_editor/b/"+digest+"/"+tc.file, with(tc.header, "Cookie", cookie)...)
			if w.Code != http.StatusOK {
				t.Errorf("%s with cookie %q: status %d, want 200", tc.file, cookie, w.Code)
			}
			if w.Header().Get("Set-Cookie") != "" {
				t.Errorf("%s: editor set a cookie", tc.file)
			}
		}
	}
}

func TestIsCrewshipCookie(t *testing.T) {
	for name, want := range map[string]bool{
		"authjs.session-token":          true,
		"__Secure-authjs.session-token": true,
		"authjs.refresh-token":          true,
		"__Secure-authjs.refresh-token": true,
		"authjs.csrf-token":             true,
		"__Host-authjs.csrf-token":      true,
		"next-auth.session-token":       true,
		"__Secure-next-auth.csrf-token": true,
		"sidebar_state":                 true,
		" AUTHJS.SESSION-TOKEN ":        true,
		"AWSALBCORS":                    false,
		"__cf_bm":                       false,
		"xauthjs.session-token":         false,
		"__Secure-other":                false,
		"":                              false,
	} {
		if got := IsCrewshipCookie(name); got != want {
			t.Errorf("IsCrewshipCookie(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSameHostHeadersAreExact(t *testing.T) {
	h, digest := sameHostFixture(t)
	policy := func(extra string) string {
		return "default-src 'none'; script-src " + sameHostSelf + " 'wasm-unsafe-eval'" + extra + "; style-src " + sameHostSelf + " 'unsafe-inline'; img-src " + sameHostSelf + " data: blob:; font-src " + sameHostSelf + "; connect-src " + sameHostSelf + "; worker-src 'none'; child-src 'none'; frame-src 'none'; object-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors " + studio + "; webrtc 'block'; sandbox allow-scripts"
	}
	for _, tc := range []struct {
		file, csp, corp, cache, acao string
		header                       []string
	}{
		{"index.html", policy(" " + scriptHash(bootstrap)), "same-origin", "no-store", "", frameNav},
		// Shared caches must not store assets: one that ignores Vary would
		// replay a frame's 200 to a Studio-context request the editor
		// refuses. Browsers still cache them privately.
		{"app.js", policy(""), "cross-origin", "private, max-age=31536000, immutable", "*", frameScript},
	} {
		t.Run(tc.file, func(t *testing.T) {
			w := serve(h, "GET", "studio.example.test", "/_editor/b/"+digest+"/"+tc.file, tc.header...)
			if w.Code != 200 {
				t.Fatal(w.Code)
			}
			want := map[string]string{
				"Content-Security-Policy":      tc.csp,
				"Cross-Origin-Resource-Policy": tc.corp,
				"Cache-Control":                tc.cache,
				"Access-Control-Allow-Origin":  tc.acao,
				"Vary":                         "Cookie, Sec-Fetch-Site, Sec-Fetch-Dest",
				"Referrer-Policy":              "no-referrer",
				"X-Content-Type-Options":       "nosniff",
				"Set-Cookie":                   "",
				"X-Frame-Options":              "",
				// The editor shares the Studio host: its HSTS must be
				// Studio's, or each editor load would drop includeSubDomains
				// from the host's stored policy (RFC 6797 section 8.1).
				"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
			}
			for k, v := range want {
				if got := w.Header().Get(k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
			// No fetch, script, style, image or font source may name the
			// Studio origin without the bundle path: that would let the
			// editor reach the API or load Studio scripts.
			csp := w.Header().Get("Content-Security-Policy")
			if bare := regexp.MustCompile(regexp.QuoteMeta(studio)+`(?:[ ;]|$)`).FindAllStringIndex(csp, -1); len(bare) != 1 || !strings.Contains(csp, "frame-ancestors "+studio+";") {
				t.Fatalf("Studio origin appears unscoped outside frame-ancestors: %s", csp)
			}
		})
	}
}

func TestSameHostFrameSourceAndIndexURL(t *testing.T) {
	h, digest := sameHostFixture(t)
	if got := h.FrameSource(); got != sameHostSelf {
		t.Fatalf("same-host frame-src source %q", got)
	}
	if got := h.IndexURL(digest); got != studio+"/_editor/b/"+digest+"/index.html" {
		t.Fatalf("same-host index URL %q", got)
	}
	d, dDigest := fixture(t)
	if d.FrameSource() != editor || d.IndexURL(dDigest) != editor+"/b/"+dDigest+"/index.html" {
		t.Fatalf("dedicated frame-src %q index %q", d.FrameSource(), d.IndexURL(dDigest))
	}
	var none *Handler
	if none.FrameSource() != "" || none.IndexURL(digest) != "" || Deny().FrameSource() != "" {
		t.Fatal("disabled or denying handler named a frame source")
	}
}

func TestSameHostWithoutUsableStudioOriginOwnsOnlyThePrefix(t *testing.T) {
	main := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
	h, err := New(Options{SameHost: true, StudioOrigin: "not an origin"})
	if h == nil || err == nil {
		t.Fatal("unusable Studio origin must be reported and still own the prefix")
	}
	if w := serve(Gate(h, main), "GET", "studio.example.test", "/_editor/b/x/index.html", frameNav...); w.Code != 404 || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("status %d csp %q", w.Code, w.Header().Get("Content-Security-Policy"))
	}
	// Unlike a dedicated host, the Studio origin itself must keep working.
	if w := serve(Gate(h, main), "GET", "studio.example.test", "/", nil...); w.Code != 299 {
		t.Fatalf("same-host failure took the application down: %d", w.Code)
	}
	if h.FrameSource() != "" {
		t.Fatal("unusable same-host editor widened frame-src")
	}
	if _, err := New(Options{SameHost: true, Origin: editor, StudioOrigin: studio}); err == nil {
		t.Fatal("same-host mode accepted a dedicated origin as well")
	}
}

func TestValidateSameHost(t *testing.T) {
	for _, tc := range []struct {
		name, studio, runtime string
		allowed               []string
		ok                    bool
	}{
		{"https studio", studio, "", nil, true},
		{"separate page runtime", studio, "https://apps.example.net", nil, true},
		{"loopback http", "http://localhost:3000", "", nil, true},
		{"loopback ip http", "http://127.0.0.1:8080", "", nil, true},
		{"cleartext studio", "http://studio.example.test", "", nil, false},
		{"unparsable studio", "studio", "", nil, false},
		{"same-origin page runtime", studio, studio, nil, false},
		{"same-origin page runtime spelled differently", studio, "https://STUDIO.example.test:443/", nil, false},
		{"opaque origin trusted", studio, "", []string{"null"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateSameHost(tc.studio, tc.runtime, tc.allowed); (err == nil) != tc.ok {
				t.Fatalf("err=%v want ok=%v", err, tc.ok)
			}
		})
	}
	if err := ValidateOrigin("https://editor.example.org", studio, "", false, []string{" null "}); err == nil {
		t.Fatal("dedicated editor accepted with the opaque origin trusted")
	}
}
