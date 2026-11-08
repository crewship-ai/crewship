package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/cli"
)

// sha256 of index.html as the claude.ai artifact service returned it
// (E8HYX1pSVKSAbLqgvTCZ8Z, version 1794069218-3a10).
const mirrorE8hyOriginalSHA = "e7ebdcd29ad05a150f02cc75c6a8d58d540ff6d942b540460caa208cad0d37d5"

func mirrorE8hyDef(t *testing.T) seeddata.ShowcaseArtifactDef {
	t.Helper()
	var found []seeddata.ShowcaseArtifactDef
	for _, a := range seeddata.ShowcaseArtifacts {
		if a.Slug == seeddata.MirrorE8hySlug {
			found = append(found, a)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s registered %d times, want once", seeddata.MirrorE8hySlug, len(found))
	}
	return found[0]
}

type mirrorE8hyManifest struct {
	EntryPoint string `json:"entrypoint"`
	Files      []struct {
		Path      string `json:"path"`
		MediaType string `json:"media_type"`
		Content   string `json:"content"`
	} `json:"files"`
	Message string `json:"message"`
}

func TestMirrorE8hy_Catalogue(t *testing.T) {
	t.Parallel()
	def := mirrorE8hyDef(t)
	for _, tc := range []struct{ field, got, want string }{
		{"slug prefix", def.Slug[:4], "ref-"},
		{"name", def.Name, "Template Crewship Showcase"},
		{"file", def.File, "mirror/e8hy/m-e8hy.json"},
		{"kind", def.Kind, "interactive"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
	if len(def.Data) != 0 {
		t.Errorf("the original declares no data collections, got %v", def.Data)
	}
	for _, p := range seeddata.Pages {
		if p.Slug == def.Slug {
			t.Errorf("slug %s collides with a demo Page", def.Slug)
		}
	}
}

// The manifest is shaped exactly as POST /api/v1/pages/artifacts takes it and
// carries the original page byte for byte.
func TestMirrorE8hy_ManifestIsWhatTheRouteAccepts(t *testing.T) {
	t.Parallel()
	raw := seeddata.ShowcaseFile(mirrorE8hyDef(t).File)
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	for k := range top {
		if k != "entrypoint" && k != "files" && k != "message" {
			t.Errorf("manifest field %q is refused by the server", k)
		}
	}
	var fileFields []map[string]json.RawMessage
	if err := json.Unmarshal(top["files"], &fileFields); err != nil {
		t.Fatal(err)
	}
	for i, f := range fileFields {
		if len(f) != 3 || f["path"] == nil || f["content"] == nil || f["media_type"] == nil {
			t.Errorf("file %d: the route requires exactly path, content and media_type", i)
		}
	}
	var m mirrorE8hyManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Message) > 4096 {
		t.Error("message over 4096 bytes")
	}
	files := map[string]artifacts.VersionFile{}
	total := 0
	for _, f := range m.Files {
		if _, dup := files[f.Path]; dup {
			t.Errorf("duplicate path %s", f.Path)
		}
		if f.Path == "" || path.IsAbs(f.Path) || path.Clean(f.Path) != f.Path || strings.ContainsAny(f.Path, "\\\x00?#") || !utf8.ValidString(f.Content) {
			t.Errorf("%s: unsafe path or invalid UTF-8", f.Path)
		}
		files[f.Path] = artifacts.VersionFile{Content: []byte(f.Content), MediaType: f.MediaType}
		total += len(f.Content)
	}
	if total > 2<<20 {
		t.Errorf("project is %d bytes, over the 2 MiB interactive source limit", total)
	}
	entry, ok := files[m.EntryPoint]
	if m.EntryPoint != "index.html" || !ok || entry.MediaType != "text/html" {
		t.Fatalf("entrypoint %q is not an HTML file of the project", m.EntryPoint)
	}
	// Crewship runs author scripts only for a project that declares a data
	// collection; the original keeps no data, so one unused one is declared.
	if cols, err := artifacts.ParseInteractiveArtifact(files); err != nil || len(cols) != 1 || cols[0] != "showcase" {
		t.Errorf("interactive marker: collections %v, err %v; the page's scripts would not run", cols, err)
	}
	if len(files) != 3 {
		t.Errorf("project has %d files, want index.html and the two markers", len(files))
	}

	for _, tc := range []struct {
		name string
		got  []byte
	}{
		{"manifest index.html", entry.Content},
		{"embedded index.html", seeddata.ShowcaseFile("mirror/e8hy/index.html")},
	} {
		sum := sha256.Sum256(tc.got)
		if hex.EncodeToString(sum[:]) != mirrorE8hyOriginalSHA {
			t.Errorf("%s differs from the original page", tc.name)
		}
	}
	// The original pins its CDN script; the copy must not float it.
	for _, want := range []string{
		`<script src="https://cdnjs.cloudflare.com/ajax/libs/three.js/r128/three.min.js"></script>`,
		`<title>Template Crewship Showcase</title>`,
		`id="model-3d"`,
	} {
		if !strings.Contains(string(entry.Content), want) {
			t.Errorf("index.html lost %s", want)
		}
	}
}

// The generic artifact step posts the manifest with slug and name, and a
// second run leaves the existing copy alone.
func TestMirrorE8hy_SeedPostsOnceAndIsIdempotent(t *testing.T) {
	t.Parallel()
	def := mirrorE8hyDef(t)
	s := newShowcaseServer(showcaseKnobs{})
	var mu sync.Mutex
	var posted [][]byte
	inner := s.handler(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/pages/artifacts" {
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				Slug string `json:"slug"`
			}
			if json.Unmarshal(raw, &body) == nil && body.Slug == def.Slug {
				mu.Lock()
				posted = append(posted, raw)
				mu.Unlock()
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		inner.ServeHTTP(w, r)
	}))
	defer srv.Close()
	client := cli.NewClient(srv.URL, "tok", showcaseTestWS)

	for run := 1; run <= 2; run++ {
		if _, err := seedShowcase(context.Background(), client, io.Discard); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		mu.Lock()
		n := len(posted)
		mu.Unlock()
		if n != 1 {
			t.Fatalf("after run %d the copy was posted %d times, want once", run, n)
		}
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(posted[0], &body); err != nil {
		t.Fatal(err)
	}
	var name string
	_ = json.Unmarshal(body["name"], &name)
	if name != def.Name {
		t.Errorf("posted name %q, want %q", name, def.Name)
	}
	var got, want mirrorE8hyManifest
	_ = json.Unmarshal(posted[0], &got)
	_ = json.Unmarshal(seeddata.ShowcaseFile(def.File), &want)
	if !reflect.DeepEqual(got, want) {
		t.Error("posted manifest differs from the embedded one")
	}
}
