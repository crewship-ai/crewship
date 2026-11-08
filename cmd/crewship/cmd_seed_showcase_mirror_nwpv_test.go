package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
)

// sha256 of each project file as the claude.ai artifact service returned it
// (NwpV8rqSjHWDtpZPYUb2ko, version 1794089214-dd97). The copies under
// _original/ must stay these bytes; the manifest is derived from them.
var mirrorNwpvOriginalSHA = map[string]string{
	"canvas.json":        "12abc2d806611ca3875016c0a22dd3743bbc6466b92e0e5c71506add95866bfd",
	"Main.dc.html":       "e8d07e185fed8b014e78c24a78611c5c3825b287ab98e8a6d3d73f764b99e6a0",
	"Detail.dc.html":     "0baca2dfa4f234389c5d8bfe57b25c1c2ca911b72cec7334379ba0dd41e3c9f1",
	"Onboarding.dc.html": "f64c3a102aeac82573ecae0abc0ed814a13b25b32ff338d5ba15d41d20bbf2e1",
	"Tablet.dc.html":     "c3d2e92f6ec3abef9e848892b8b326c0e33bef0e90449381fa789d76a728ea78",
	"Desktop.dc.html":    "7e5c334484219edbf49563fcd54949819203be37a910b63fe04bb103c85136cd",
	"Web.dc.html":        "6a4b1602deaf1067de827ac8e0103997420b023bb04f6da79387c2bf15ad3341",
	"Wireframe.dc.html":  "d32c188fbab92156f33fc561cdcdf28e8f5e8c99cff3d50cd11517a54091496d",
	"Flow.dc.html":       "a0eb1ceb7da1afb3a9389a9305c82903a7a8fe535307ed58a0d4dd16524ec3a0",
	"States.dc.html":     "c8f3f41dd14574c8fb578ed352ca00d57b6fc3a29be652a137dd1ac1b0539aab",
	"Form.dc.html":       "90a270634660daff404d9f66ddcb3cfbe32107fce7f0a6c40f4b651799024c66",
	"Social.dc.html":     "005de43ce56c838b355013b47868afeef1ec465563a31005e1865ff66fca7720",
	"Email.dc.html":      "2d508d4a55589251e2a6b5674f396d9a7226a8fec4707985581be3f22440bd3b",
	"Palette.dc.html":    "8c38a161b4e641874477305444b485de3b933de5a885e5955bc6d737c6a2c906",
}

const mirrorNwpvDir = "seeddata/showcase/mirror/nwpv"

type mirrorNwpvFile struct {
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
	Content   string `json:"content"`
}

type mirrorNwpvManifest struct {
	EntryPoint string           `json:"entrypoint"`
	Files      []mirrorNwpvFile `json:"files"`
	Message    string           `json:"message"`
}

type mirrorNwpvCanvas struct {
	Title string `json:"title"`
	Pages []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"pages"`
	Boards map[string]struct {
		Title string `json:"title"`
		Page  string `json:"page"`
	} `json:"boards"`
	Order []string `json:"order"`
	Notes map[string]struct {
		Text string `json:"text"`
	} `json:"notes"`
}

func mirrorNwpvDef(t *testing.T) seeddata.ShowcaseArtifactDef {
	t.Helper()
	var found []seeddata.ShowcaseArtifactDef
	for _, a := range seeddata.ShowcaseArtifacts {
		if a.Slug == seeddata.MirrorNwpvSlug {
			found = append(found, a)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s registered %d times, want once", seeddata.MirrorNwpvSlug, len(found))
	}
	return found[0]
}

func mirrorNwpvLoad(t *testing.T) (mirrorNwpvManifest, map[string]mirrorNwpvFile) {
	t.Helper()
	var m mirrorNwpvManifest
	dec := json.NewDecoder(bytes.NewReader(seeddata.ShowcaseFile(mirrorNwpvDef(t).File)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	byPath := map[string]mirrorNwpvFile{}
	for _, f := range m.Files {
		byPath[f.Path] = f
	}
	return m, byPath
}

func mirrorNwpvOriginal(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(mirrorNwpvDir, "_original", "project", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mirrorNwpvReadCanvas(t *testing.T) mirrorNwpvCanvas {
	t.Helper()
	var c mirrorNwpvCanvas
	if err := json.Unmarshal(mirrorNwpvOriginal(t, "canvas.json"), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// mirrorNwpvPublished and mirrorNwpvConvert are the transformation SOURCE.md
// documents and _build.py applies.
func mirrorNwpvPublished(board string) string { return strings.TrimSuffix(board, ".dc.html") + ".html" }

func mirrorNwpvConvert(src string, boards []string) string {
	out := strings.Replace(src, `<script src="./support.js"></script>`, "<link rel=\"stylesheet\" href=\"fonts.css\">\n<script src=\"dc.js\"></script>", 1)
	var kept []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, `<link rel="stylesheet" href="https://fonts.googleapis.com/`) {
			kept = append(kept, line)
		}
	}
	out = strings.Join(kept, "\n")
	for _, b := range boards {
		out = strings.ReplaceAll(out, `href="`+b+`"`, `href="`+mirrorNwpvPublished(b)+`"`)
	}
	return out
}

func TestMirrorNwpv_Catalogue(t *testing.T) {
	t.Parallel()
	def := mirrorNwpvDef(t)
	for _, tc := range []struct{ field, got, want string }{
		{"slug prefix", def.Slug[:4], "ref-"},
		{"name is the original title", def.Name, mirrorNwpvReadCanvas(t).Title},
		{"file", def.File, "mirror/nwpv/m-nwpv.json"},
		{"kind", def.Kind, "project"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
	if len(def.Data) != 0 {
		t.Errorf("a static App declares no data collections, got %v", def.Data)
	}
	for _, a := range seeddata.ShowcaseArtifacts {
		if a.Slug != def.Slug && a.File == def.File {
			t.Errorf("%s shares the manifest file with %s", a.Slug, def.Slug)
		}
	}
}

// The originals kept beside the manifest are the bytes claude.ai returned.
func TestMirrorNwpv_OriginalsArePinned(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(filepath.Join(mirrorNwpvDir, "_original", "project"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(mirrorNwpvOriginalSHA) {
		t.Errorf("%d original files, want %d", len(entries), len(mirrorNwpvOriginalSHA))
	}
	for name, want := range mirrorNwpvOriginalSHA {
		sum := sha256.Sum256(mirrorNwpvOriginal(t, name))
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s: sha256 %s, want %s", name, got, want)
		}
	}
}

// What the server accepts (POST /api/v1/pages/artifacts): only the three
// manifest fields, unique clean relative paths, UTF-8 text with a media type,
// an entrypoint that exists, within the per-item budget.
func TestMirrorNwpv_ManifestShape(t *testing.T) {
	t.Parallel()
	raw := seeddata.ShowcaseFile(mirrorNwpvDef(t).File)
	if len(raw) > 2<<20 {
		t.Errorf("manifest is %d bytes, over the 2 MB mirror budget", len(raw))
	}
	m, byPath := mirrorNwpvLoad(t)
	if len(byPath) != len(m.Files) {
		t.Error("duplicate file paths")
	}
	if _, ok := byPath[m.EntryPoint]; !ok || m.EntryPoint != "index.html" {
		t.Errorf("entrypoint %q, want index.html present", m.EntryPoint)
	}
	if m.Message == "" || len(m.Message) > 4096 {
		t.Errorf("message %q", m.Message)
	}
	wantMedia := map[string]string{".html": "text/html", ".js": "text/javascript", ".css": "text/css"}
	for _, f := range m.Files {
		if f.Path == "" || path.IsAbs(f.Path) || path.Clean(f.Path) != f.Path || strings.HasPrefix(f.Path, "../") || strings.ContainsAny(f.Path, "\\\x00?#") {
			t.Errorf("path %q is refused by the version store", f.Path)
		}
		if want := wantMedia[path.Ext(f.Path)]; f.MediaType != want {
			t.Errorf("%s: media type %q, want %q", f.Path, f.MediaType, want)
		}
		if !utf8.ValidString(f.Content) || f.Content == "" {
			t.Errorf("%s: empty or not UTF-8", f.Path)
		}
		if path.Ext(f.Path) == ".html" && (strings.Contains(f.Content, "fonts.googleapis.com") || strings.Contains(f.Content, "support.js")) {
			t.Errorf("%s still references the claude.ai runtime or a network font", f.Path)
		}
	}
	for _, name := range []string{"dc.js", "fonts.css"} {
		if _, ok := byPath[name]; !ok {
			t.Errorf("%s missing", name)
		}
	}
	if !strings.Contains(byPath["fonts.css"].Content, "font-family:'IBM Plex Sans'") || !strings.Contains(byPath["fonts.css"].Content, "font-family:'JetBrains Mono'") {
		t.Error("fonts.css does not carry both families the artboards name")
	}
	if strings.Contains(byPath["fonts.css"].Content, "url(http") {
		t.Error("fonts.css must embed the fonts; the frame loads fonts from data: only")
	}
}

// Every artboard is its original, changed only by the documented head lines
// and screen-link renames.
func TestMirrorNwpv_ArtboardsAreTheOriginals(t *testing.T) {
	t.Parallel()
	canvas := mirrorNwpvReadCanvas(t)
	_, byPath := mirrorNwpvLoad(t)
	if len(canvas.Order) != 13 || len(canvas.Boards) != 13 {
		t.Fatalf("canvas has %d/%d boards, want 13", len(canvas.Order), len(canvas.Boards))
	}
	if len(byPath) != len(canvas.Order)+3 {
		t.Errorf("manifest has %d files, want the %d artboards plus index.html, dc.js and fonts.css", len(byPath), len(canvas.Order))
	}
	for _, board := range canvas.Order {
		t.Run(board, func(t *testing.T) {
			got, ok := byPath[mirrorNwpvPublished(board)]
			if !ok {
				t.Fatalf("%s not published", mirrorNwpvPublished(board))
			}
			want := mirrorNwpvConvert(string(mirrorNwpvOriginal(t, board)), canvas.Order)
			if got.Content != want {
				t.Errorf("%s differs from its converted original; rerun _build.py", board)
			}
			if !strings.Contains(got.Content, `<script src="dc.js"></script>`) || !strings.Contains(got.Content, `<link rel="stylesheet" href="fonts.css">`) {
				t.Error("runtime or fonts not linked")
			}
		})
	}
}

// The overview carries the canvas itself: its title, every page, every note
// and a link to every artboard by its title.
func TestMirrorNwpv_OverviewCarriesTheCanvas(t *testing.T) {
	t.Parallel()
	canvas := mirrorNwpvReadCanvas(t)
	_, byPath := mirrorNwpvLoad(t)
	index := byPath["index.html"].Content
	var want []string
	want = append(want, "<title>"+html.EscapeString(canvas.Title)+"</title>")
	for _, p := range canvas.Pages {
		want = append(want, `href="#page-`+p.ID+`"`, `id="page-`+p.ID+`"`, html.EscapeString(p.Name))
	}
	for id, n := range canvas.Notes {
		if !strings.Contains(index, html.EscapeString(n.Text)) {
			t.Errorf("note %s missing: %q", id, n.Text)
		}
	}
	for _, b := range canvas.Order {
		want = append(want, `href="`+mirrorNwpvPublished(b)+`"`, html.EscapeString(canvas.Boards[b].Title))
	}
	for _, w := range want {
		if !strings.Contains(index, w) {
			t.Errorf("overview lacks %q", w)
		}
	}
	if strings.Contains(index, "<script") {
		t.Error("the overview is static")
	}
}

// The seed posts the manifest through the artifact route once, and a re-seed
// leaves the existing App alone (idempotent by slug).
func TestMirrorNwpv_SeedIsIdempotent(t *testing.T) {
	t.Parallel()
	def := mirrorNwpvDef(t)
	var mu sync.Mutex
	exists := false
	var posts []map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/pages/"+def.Slug:
			if !exists {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":"not found"}`))
				return
			}
			_, _ = w.Write([]byte(`{"has_artifact_project":true}`))
		case r.Method == "POST" && r.URL.Path == "/api/v1/pages/artifacts":
			var body map[string]json.RawMessage
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("create body: %v", err)
			}
			posts = append(posts, body)
			if exists {
				w.WriteHeader(409)
				return
			}
			exists = true
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"version":1}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	client := cli.NewClient(srv.URL, "tok", showcaseTestWS)
	for i, want := range []showcaseOutcome{showcaseCreated, showcaseExists} {
		var out bytes.Buffer
		if got := seedShowcaseArtifact(client, &out, def); got != want {
			t.Fatalf("run %d: outcome %v, want %v\n%s", i+1, got, want, out.String())
		}
	}
	if len(posts) != 1 {
		t.Fatalf("%d creates, want 1", len(posts))
	}
	var slug, name string
	_ = json.Unmarshal(posts[0]["slug"], &slug)
	_ = json.Unmarshal(posts[0]["name"], &name)
	var files []mirrorNwpvFile
	_ = json.Unmarshal(posts[0]["files"], &files)
	_, byPath := mirrorNwpvLoad(t)
	if slug != def.Slug || name != def.Name || len(files) != len(byPath) {
		t.Errorf("posted slug %q name %q with %d files", slug, name, len(files))
	}
	for _, f := range files {
		if byPath[f.Path].Content != f.Content {
			t.Errorf("%s posted with different bytes", f.Path)
		}
	}
}
