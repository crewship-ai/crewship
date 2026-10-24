package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/editororigin"
)

// writeEditorBundle lays out one pinned editor bundle (<dir>/<digest>/ with
// its manifest) and returns its pin.
func writeEditorBundle(t *testing.T, dir string) string {
	t.Helper()
	files := map[string]string{"index.html": "<!doctype html><title>editor</title>", "app.js": "console.log(1)"}
	manifest := map[string]any{"files": map[string]string{}}
	for name, body := range files {
		sum := sha256.Sum256([]byte(body))
		manifest["files"].(map[string]string)[name] = hex.EncodeToString(sum[:])
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	root := filepath.Join(dir, digest)
	if err = os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err = os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(root, "manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return digest
}

// Opening an editor session names the bundle the host page frames: with the
// zero-config same-host editor, bundle_digest is the pinned bundle and
// editor_url its document path on the host's own origin
// (/_editor/b/<digest>/index.html), the form the host's editorFrameSrc accepts.
// Heartbeats report the same pair. Without a bundle both stay empty.
func TestGridCraftEditorSessionNamesTheSameHostBundle(t *testing.T) {
	dir := t.TempDir()
	digest := writeEditorBundle(t, dir)
	editor, err := editororigin.New(editororigin.Options{SameHost: true, Scheme: RequestScheme, BundleDir: dir, Bundles: []string{digest}})
	if err != nil {
		t.Fatal(err)
	}
	f := gridcraftInstanceFixture(t)
	r, err := NewRouter(f.db, "this-is-a-32-char-test-secret-pad", newTestLogger(), WithWorkbookEditorBundle(editor.SessionBundle))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Shutdown)
	f.r = r
	f.r.RegisterGridCraft(appengine.Options{Enabled: true, Editor: true}, gridcraftCellsEngine{})
	setGridCraftEditorFlag(t, f, "ws-old", true)
	base := liveWorkbook(t, f, f.boss, "boss", "bundle-book")

	type opened struct {
		SessionID    string `json:"session_id"`
		BundleDigest string `json:"bundle_digest"`
		EditorURL    string `json:"editor_url"`
	}
	read := func(name string, body []byte) opened {
		t.Helper()
		var o opened
		if err := json.Unmarshal(body, &o); err != nil {
			t.Fatalf("%s: %s", name, body)
		}
		if o.BundleDigest != digest || o.EditorURL != "/_editor/b/"+digest+"/index.html" {
			t.Fatalf("%s: bundle_digest=%q editor_url=%q, want %q and its same-host index", name, o.BundleDigest, o.EditorURL, digest)
		}
		return o
	}
	w := f.do(f.boss, "POST", base+"/sessions?workspace_id=ws-old", "")
	wantCode(t, w, 201, "open")
	s := read("open", w.Body.Bytes())
	w = f.do(f.boss, "POST", base+"/sessions?workspace_id=ws-old", `{"session_id":"`+s.SessionID+`"}`)
	wantCode(t, w, 200, "heartbeat")
	read("heartbeat", w.Body.Bytes())

	// No loaded bundle: nothing to frame, so neither field is sent.
	empty, err := editororigin.New(editororigin.Options{SameHost: true, Scheme: RequestScheme})
	if err != nil {
		t.Fatal(err)
	}
	g := gridcraftInstanceFixture(t)
	r2, err := NewRouter(g.db, "this-is-a-32-char-test-secret-pad", newTestLogger(), WithWorkbookEditorBundle(empty.SessionBundle))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r2.Shutdown)
	g.r = r2
	g.r.RegisterGridCraft(appengine.Options{Enabled: true, Editor: true}, gridcraftCellsEngine{})
	setGridCraftEditorFlag(t, g, "ws-old", true)
	base = liveWorkbook(t, g, g.boss, "boss", "unbundled-book")
	w = g.do(g.boss, "POST", base+"/sessions?workspace_id=ws-old", "")
	wantCode(t, w, 201, "open without bundle")
	var raw map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &raw); err != nil || raw["bundle_digest"] != "" || raw["editor_url"] != nil {
		t.Fatalf("session without bundle %s", w.Body.String())
	}
}
