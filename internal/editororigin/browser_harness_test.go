package editororigin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// harnessApp is the stand-in editor: an inline module bootstrap (as Trunk
// emits), an external module and a wasm module. It reports what the sandbox
// allowed and then speaks only over the MessagePort it is handed.
const harnessBootstrap = `import init from './app.js';init()`

const harnessApp = `export default async function init() {
  const params = new URLSearchParams(location.search)
  const parentOrigin = params.get('parent_origin')
  const bundle = location.pathname.split('/')[2]
  const r = { origin: self.origin }
  try { r.cookie = document.cookie; r.cookieRead = 'allowed' } catch (e) { r.cookieRead = 'blocked:' + e.name }
  try { document.cookie = 'tossed=1; Path=/'; r.cookieWrite = 'allowed' } catch (e) { r.cookieWrite = 'blocked:' + e.name }
  try { localStorage.setItem('x', '1'); r.storage = 'allowed' } catch (e) { r.storage = 'blocked:' + e.name }
  try { top.location.href = parentOrigin + '/navigated'; r.topNav = 'not thrown' } catch (e) { r.topNav = 'blocked:' + e.name }
  try { await WebAssembly.instantiateStreaming(fetch(new URL('./app_bg.wasm', import.meta.url))); r.wasm = 'ok' } catch (e) { r.wasm = 'failed:' + e }
  try { await fetch(parentOrigin + '/probe'); r.fetchParent = 'allowed' } catch (e) { r.fetchParent = 'blocked' }
  // no-cors subresources from the opaque origin (Trunk CSS, images, classic scripts)
  r.cssApplied = getComputedStyle(document.body).color === 'rgb(1, 2, 3)'
  r.classicRan = window.classicRan === true
  const img = document.getElementById('logo')
  r.imgLoaded = await new Promise(resolve => {
    if (img.complete) return resolve(img.naturalWidth > 0)
    img.onload = () => resolve(img.naturalWidth > 0)
    img.onerror = () => resolve(false)
  })
  addEventListener('message', event => {
    if (event.source !== parent || event.origin !== parentOrigin || event.data?.type !== 'gridcraft:connect' || r.connected) return
    r.connected = true
    const port = event.ports[0]
    port.onmessage = message => {
      if (message.data?.navigate) { location.href = parentOrigin + message.data.navigate; return }
      port.postMessage({ type: 'gridcraft:echo', got: message.data, results: r })
    }
  })
  parent.postMessage({ type: 'gridcraft:hello', protocol: 1, bundle }, parentOrigin)
}
`

// A 1x1 transparent PNG.
var harnessPNG = string([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00, 0x0b, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x60, 0x00, 0x02, 0x00, 0x00, 0x05, 0x00, 0x01, 0x7a, 0x5e, 0xab, 0x3f, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82})

// The smallest valid wasm module: magic and version.
var harnessWasm = string([]byte{0, 'a', 's', 'm', 1, 0, 0, 0})

// TestEditorOriginBrowserHarness serves a sandboxed bundle for
// e2e/editor-origin-sandbox.mjs; the Playwright script stops it.
func TestEditorOriginBrowserHarness(t *testing.T) {
	out := os.Getenv("CREWSHIP_TEST_EDITOR_FILE")
	if out == "" {
		// SKIP-WAIVER(#2472): blocking server started only by the Chromium harness script.
		t.Skip("browser harness only")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + listener.Addr().String()
	dir := t.TempDir()
	files := map[string]string{
		"index.html":  `<!doctype html><html><head><meta charset="utf-8"><link rel="stylesheet" href="app.css"><script src="classic.js"></script><script type="module">` + harnessBootstrap + `</script></head><body><img id="logo" alt="" src="logo.png"></body></html>`,
		"app.js":      harnessApp,
		"app_bg.wasm": harnessWasm,
		"app.css":     "body { color: rgb(1, 2, 3) }",
		"classic.js":  "window.classicRan = true",
		"logo.png":    harnessPNG,
	}
	sums := map[string]string{}
	for name, body := range files {
		s := sha256.Sum256([]byte(body))
		sums[name] = hex.EncodeToString(s[:])
	}
	manifest, _ := json.Marshal(map[string]any{"files": sums})
	pin := sha256.Sum256(manifest)
	digest := hex.EncodeToString(pin[:])
	os.MkdirAll(filepath.Join(dir, digest), 0o755)
	for name, body := range files {
		os.WriteFile(filepath.Join(dir, digest, name), []byte(body), 0o644)
	}
	os.WriteFile(filepath.Join(dir, digest, "manifest.json"), manifest, 0o644)
	h, err := New(Options{Origin: origin, StudioOrigin: os.Getenv("CREWSHIP_TEST_STUDIO_ORIGIN"), BundleDir: dir, Bundles: []string{digest}})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{}, 1)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__stop" {
			select {
			case stop <- struct{}{}:
			default:
			}
			w.WriteHeader(204)
			return
		}
		h.ServeHTTP(w, r)
	})}
	defer server.Close()
	go server.Serve(listener)
	if err := os.WriteFile(out, []byte(fmt.Sprintf(`{"origin":%q,"digest":%q}`, origin, digest)), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stop:
	case <-time.After(60 * time.Second):
		t.Fatal("browser harness timeout")
	}
}
