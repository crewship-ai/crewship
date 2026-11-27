package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// binaryReadFixture is a gridcraft page with two saved versions and an admitted owner session.
func binaryReadFixture(t *testing.T) (*craftNativeDocumentsHTTP, context.Context, string) {
	t.Helper()
	f, h, ctx := craftNativeHTTPFixture(t)
	mustExec(t, f.db, `DELETE FROM craft_native_documents WHERE page_id='native-page'`)
	mustExec(t, f.db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES('native-page',?,'gridcraft',?,?,?)`, f.wsID, strings.Repeat("a", 40), strings.Repeat("b", 64), strings.Repeat("c", 64))
	token, _ := craftNativeIssued(t, h, ctx)
	cfg := mooring.NativeStoreConfig{Application: "gridcraft", EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: func(context.Context, []byte) error { return nil }}
	d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{"gridcraft": {Store: cfg, EditorURL: "/_editor/b/" + cfg.EditorBundle + "/index.html"}})
	s := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	for i, doc := range []string{`{"v":1}`, `{"v":2,"cells":"` + strings.Repeat("x", 64<<10) + `"}`} {
		if _, err := s.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: f.wsID, PageID: "native-page"}, artifacts.VersionRequest{BaseVersion: int64(i), EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: []byte(doc), MediaType: "application/json"}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.userID, nil }); err != nil {
			t.Fatal(err)
		}
	}
	return d, ctx, token
}

func readDocument(d *craftNativeDocumentsHTTP, ctx context.Context, token string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/pages/native-page/craft/document", nil).WithContext(ctx)
	r.SetPathValue("slug", "native-page")
	if token != "" {
		r.Header.Set("X-Craft-Session", token)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	d.readSnapshot(w, r)
	return w
}

// A client that asks for the native media type gets the document as the raw body, gzip-compressed when
// it accepts gzip, with the same metadata as the JSON read in X-Craft-Document; nothing in base64.
func TestNativeDocumentReadsAsCompressedBinary(t *testing.T) {
	d, ctx, token := binaryReadFixture(t)
	jsonRead := readDocument(d, ctx, token, nil)
	if jsonRead.Code != 200 || !strings.HasPrefix(jsonRead.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("json read %d %q", jsonRead.Code, jsonRead.Header().Get("Content-Type"))
	}
	var want craftNativeDocumentResponse
	if err := json.Unmarshal(jsonRead.Body.Bytes(), &want); err != nil {
		t.Fatal(err)
	}

	w := readDocument(d, ctx, token, map[string]string{"Accept": craftNativeBinaryMediaType, "Accept-Encoding": "br, gzip;q=0.9"})
	if w.Code != 200 || w.Header().Get("Content-Type") != craftNativeBinaryMediaType || w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("binary read %d %v", w.Code, w.Header())
	}
	if vary := w.Header().Values("Vary"); !strings.Contains(strings.Join(vary, ","), "Accept-Encoding") || !strings.Contains(strings.Join(vary, ","), "Accept") {
		t.Fatalf("vary %v", vary)
	}
	if w.Body.Len() >= len(want.Bytes)/4 {
		t.Fatalf("compressed body %d of %d bytes", w.Body.Len(), len(want.Bytes))
	}
	zr, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(zr)
	if err != nil || !bytes.Equal(got, want.Bytes) {
		t.Fatalf("body differs from the JSON read: %v (%d vs %d bytes)", err, len(got), len(want.Bytes))
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(w.Header().Get("X-Craft-Document")), &meta); err != nil {
		t.Fatalf("metadata %q: %v", w.Header().Get("X-Craft-Document"), err)
	}
	if _, has := meta["bytes"]; has || meta["version"] != want.Version || meta["applicationId"] != "gridcraft" || meta["enginePin"] != want.EnginePin || meta["editorBundle"] != want.EditorBundle || meta["editorURL"] != want.EditorURL {
		t.Fatalf("metadata %v, want %+v", meta, want)
	}
	if q, _ := meta["quota"].(map[string]any); q == nil || q["usedBytes"] != want.Quota.UsedBytes || q["limitBytes"] != want.Quota.LimitBytes {
		t.Fatalf("quota %v", meta["quota"])
	}
	if l := w.Header().Get("Content-Length"); l != "" && l != strconv.Itoa(w.Body.Len()) {
		t.Fatalf("content-length %s for %d bytes", l, w.Body.Len())
	}

	plain := readDocument(d, ctx, token, map[string]string{"Accept": craftNativeBinaryMediaType})
	if plain.Code != 200 || plain.Header().Get("Content-Encoding") != "" || !bytes.Equal(plain.Body.Bytes(), want.Bytes) {
		t.Fatalf("uncompressed binary read %d %v (%d bytes)", plain.Code, plain.Header(), plain.Body.Len())
	}
	if refused := readDocument(d, ctx, token, map[string]string{"Accept": craftNativeBinaryMediaType, "Accept-Encoding": "gzip;q=0"}); refused.Header().Get("Content-Encoding") != "" {
		t.Fatal("gzip sent to a client that refused it")
	}
}

// The binary read is the same read: without the admission it fails exactly as the JSON read does.
func TestNativeDocumentBinaryReadKeepsTheReadAuthority(t *testing.T) {
	d, ctx, _ := binaryReadFixture(t)
	jsonRead := readDocument(d, ctx, "", nil)
	bin := readDocument(d, ctx, "", map[string]string{"Accept": craftNativeBinaryMediaType, "Accept-Encoding": "gzip"})
	if jsonRead.Code == 200 || bin.Code != jsonRead.Code || bin.Header().Get("X-Craft-Document") != "" || bytes.Contains(bin.Body.Bytes(), []byte(`"v":2`)) {
		t.Fatalf("json %d, binary %d %v", jsonRead.Code, bin.Code, bin.Header())
	}
}
