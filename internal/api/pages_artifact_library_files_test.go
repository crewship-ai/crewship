package api

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// A pin carries the library's licensed fonts; every reader of the consumer may
// read them to render, nobody outside the consumer may, and losing access to
// the library changes nothing (#5248).
func TestArtifactLibraryPinFilesFollowTheConsumer(t *testing.T) {
	f := newLibraryFixture(t)
	font := "data:font/woff2;base64," + base64.StdEncoding.EncodeToString(append([]byte("wOF2"), make([]byte, 64)...))
	body := fmt.Sprintf(`{"base_version":1,"entrypoint":"index.html","files":[{"path":"index.html","media_type":"text/html","content":"<main>Library</main>"},{"path":"design-system.json","media_type":"application/json","content":%q},{"path":"fonts.json","media_type":"application/json","content":%q},{"path":"fonts/Inter.woff2","media_type":"font/woff2","content":%q},{"path":"fonts/OFL.txt","media_type":"text/plain","content":"SIL Open Font License, Version 1.1"}]}`,
		`{"colors":{"primary":"#111"}}`, `{"schema_version":1,"fonts":[{"file":"fonts/Inter.woff2","family":"Inter"}]}`, font)
	f.must(200, f.owner, "POST", "lib/artifact/versions", body)
	f.must(200, f.owner, "POST", "lib/artifact/share", `{"base_version":2,"version":2}`)
	f.must(200, f.owner, "POST", "lib/artifact/library/publish", `{"version":2}`)
	pin := f.must(200, "lib-editor", "PUT", "deck/artifact/library-pin", `{"library":"lib","version":2,"if_pinned_version":0}`)
	if !strings.Contains(fmt.Sprint(pin["files"]), "fonts/Inter.woff2") || strings.Contains(fmt.Sprint(pin["files"]), "wOF2") {
		t.Fatalf("pin lists files without bytes: %v", pin["files"])
	}

	// The library goes private: the reader of the consumer still renders.
	mustExec(t, f.h.db, `DELETE FROM page_grants WHERE page_id=?`, f.libID)
	got := f.must(200, "lib-reader", "GET", "deck/artifact/library-pin/files", "")
	files := got["files"].([]any)
	if len(files) != 2 {
		t.Fatalf("files %v", files)
	}
	inter := files[0].(map[string]any)
	if inter["path"] != "fonts/Inter.woff2" || inter["status"] != "copied" || inter["family"] != "Inter" || "data:font/woff2;base64,"+inter["content"].(string) != font {
		t.Fatalf("font %v", inter)
	}
	// Another workspace's owner and an unpinned project see nothing.
	if w := f.call("outsider", "GET", "deck/artifact/library-pin/files", ""); w.Code != 404 {
		t.Fatalf("outsider %d", w.Code)
	}
	if w := f.call(f.owner, "GET", "lib/artifact/library-pin/files", ""); w.Code != 404 {
		t.Fatalf("unpinned project %d", w.Code)
	}
}
