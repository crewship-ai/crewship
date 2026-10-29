package editororigin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// sessionReq is an HTTPS API request reaching Crewship on host.
func sessionReq(host string) *http.Request {
	r := httptest.NewRequest("POST", "https://"+host+"/api/v1/pages/p/workbook/sessions", nil)
	r.Host = host
	return r
}

// SessionBundle is what a new editor session reports to the host page
// (bundle_digest, editor_url). In same-host mode the URL is the bundle path on
// the host's own origin, exactly the form the host's editorFrameSrc accepts
// (lib/workbooks/editor-client.ts); with a dedicated origin it is absolute.
func TestSessionBundleNamesTheFramedBundle(t *testing.T) {
	h, digest := sameHostFixture(t)
	if d, u := h.SessionBundle(sessionReq("studio.example.test")); d != digest || u != "/_editor/b/"+digest+"/index.html" || u != h.IndexURL(digest) {
		t.Fatalf("same-host bundle %q %q", d, u)
	}
	// The configured Studio origin is the only one the editor answers on.
	if d, u := h.SessionBundle(sessionReq("other.example.test")); d != "" || u != "" {
		t.Fatalf("misdirected host got %q %q", d, u)
	}
	// An origin the proxy self-check disabled frames nothing.
	h.RecordCheck(studio, ProxyCheck{Status: ProxyCheckFailed, Isolation: true, Cause: CauseRedirect, Problems: []string{"redirected"}})
	if d, u := h.SessionBundle(sessionReq("studio.example.test")); d != "" || u != "" {
		t.Fatalf("disabled origin got %q %q", d, u)
	}

	z, zd := zeroConfigFixture(t)
	if d, u := z.SessionBundle(sessionReq("anything.example.test")); d != zd || u != "/_editor/b/"+zd+"/index.html" {
		t.Fatalf("zero-config bundle %q %q", d, u)
	}

	dedicated, dd := fixture(t)
	if d, u := dedicated.SessionBundle(sessionReq("studio.example.test")); d != dd || u != editor+"/b/"+dd+"/index.html" {
		t.Fatalf("dedicated bundle %q %q", d, u)
	}

	var off *Handler
	if d, u := off.SessionBundle(sessionReq("studio.example.test")); d != "" || u != "" {
		t.Fatalf("nil handler got %q %q", d, u)
	}
	if d, u := Deny().SessionBundle(sessionReq("studio.example.test")); d != "" || u != "" {
		t.Fatalf("deny handler got %q %q", d, u)
	}
}

// With several pinned bundles the first one of the allowlist that loaded is
// framed; one that failed verification is skipped.
func TestSessionBundleFollowsAllowlistOrder(t *testing.T) {
	dir := t.TempDir()
	index := `<!doctype html><script type="module">` + bootstrap + `</script>`
	first := writeBundle(t, dir, map[string]string{"index.html": index, "app.js": "console.log(1)"})
	second := writeBundle(t, dir, map[string]string{"index.html": index, "app.js": "console.log(2)"})
	missing := strings.Repeat("0", 64)
	h, err := New(Options{SameHost: true, StudioOrigin: studio, BundleDir: dir, Bundles: []string{second, first}})
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := h.SessionBundle(sessionReq("studio.example.test")); d != second {
		t.Fatalf("framed %q, want the first allowlisted %q", d, second)
	}
	h, _ = New(Options{SameHost: true, StudioOrigin: studio, BundleDir: dir, Bundles: []string{missing, first}})
	if d, _ := h.SessionBundle(sessionReq("studio.example.test")); d != first {
		t.Fatalf("framed %q, want the first bundle that loaded %q", d, first)
	}
	h, _ = New(Options{SameHost: true, StudioOrigin: studio, BundleDir: dir})
	if d, u := h.SessionBundle(sessionReq("studio.example.test")); d != "" || u != "" {
		t.Fatalf("no bundles got %q %q", d, u)
	}
}

// The application release pin must never fall back to another loaded editor.
func TestPinnedBundleRequiresExactVerifiedBundleAndOrigin(t *testing.T) {
	dir := t.TempDir()
	index := `<!doctype html><script type="module">` + bootstrap + `</script>`
	first := writeBundle(t, dir, map[string]string{"index.html": index, "app.js": "console.log(1)"})
	second := writeBundle(t, dir, map[string]string{"index.html": index, "app.js": "console.log(2)"})
	h, err := New(Options{SameHost: true, StudioOrigin: studio, BundleDir: dir, Bundles: []string{first, second}})
	if err != nil {
		t.Fatal(err)
	}
	if d, u := h.PinnedBundle(sessionReq("studio.example.test"), second); d != second || u != "/_editor/b/"+second+"/index.html" {
		t.Fatalf("exact pin got %q %q", d, u)
	}
	for _, missing := range []string{"", "../index.html", strings.Repeat("0", 64)} {
		if h.HasBundle(missing) {
			t.Fatalf("unverified pin loaded: %q", missing)
		}
		if d, u := h.PinnedBundle(sessionReq("studio.example.test"), missing); d != "" || u != "" {
			t.Fatalf("unverified pin framed: %q %q", d, u)
		}
	}
	if d, u := h.PinnedBundle(sessionReq("other.example.test"), second); d != "" || u != "" {
		t.Fatalf("wrong origin framed: %q %q", d, u)
	}
	h.RecordCheck(studio, ProxyCheck{Status: ProxyCheckFailed, Isolation: true, Cause: CauseRedirect, Problems: []string{"redirected"}})
	if !h.HasBundle(second) {
		t.Fatal("proxy failure must not change immutable verified inventory")
	}
	if d, u := h.PinnedBundle(sessionReq("studio.example.test"), second); d != "" || u != "" {
		t.Fatalf("failed isolation framed: %q %q", d, u)
	}
	for _, off := range []*Handler{nil, Deny()} {
		if off.HasBundle(second) {
			t.Fatal("disabled handler reports loaded pin")
		}
		if d, u := off.PinnedBundle(sessionReq("studio.example.test"), second); d != "" || u != "" {
			t.Fatalf("disabled handler framed: %q %q", d, u)
		}
	}
}

func TestApplicationPackagesVerifyAndSnapshotExactReleaseBytes(t *testing.T) {
	dir := t.TempDir()
	index := `<!doctype html><script type="module">` + bootstrap + `</script>`
	pin := writeBundle(t, dir, map[string]string{"index.html": index, "app.js": "console.log(42)"})
	source := fstest.MapFS{}
	for _, name := range []string{"manifest.json", "index.html", "app.js"} {
		body, err := os.ReadFile(filepath.Join(dir, pin, name))
		if err != nil {
			t.Fatal(err)
		}
		source[pin+"/"+name] = &fstest.MapFile{Data: body}
	}
	h, err := New(Options{SameHost: true, StudioOrigin: studio, ApplicationPackages: source})
	if err != nil {
		t.Fatal(err)
	}
	if d, u := h.PinnedBundle(sessionReq("studio.example.test"), pin); d != pin || u == "" {
		t.Fatalf("release pin unavailable: %q %q", d, u)
	}
	// Application packages never become the workbook's first allowlisted app.
	if d, u := h.SessionBundle(sessionReq("studio.example.test")); d != "" || u != "" {
		t.Fatalf("release package changed legacy application selection: %q %q", d, u)
	}
	source[pin+"/app.js"].Data[0] = 'X'
	r := httptest.NewRequest("GET", "https://studio.example.test/_editor/b/"+pin+"/app.js", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Sec-Fetch-Dest", "script")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "console.log(42)" {
		t.Fatalf("handler retained mutable package source: %d %q", w.Code, w.Body.String())
	}
	bad, err := New(Options{SameHost: true, StudioOrigin: studio, ApplicationPackages: source})
	if err == nil || bad.HasBundle(pin) {
		t.Fatal("corrupt release package accepted")
	}
	delete(source, pin+"/manifest.json")
	bad, err = New(Options{SameHost: true, StudioOrigin: studio, ApplicationPackages: source})
	if err == nil || bad.HasBundle(pin) {
		t.Fatal("unmanifested release package accepted")
	}
}
