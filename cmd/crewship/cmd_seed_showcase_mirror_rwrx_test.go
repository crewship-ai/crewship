package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/cli"
)

// The mirrored reference dashboard (seeddata/showcase/mirror/rwrx): an
// interactive dashboard project whose page is the original's bytes and whose
// rows are the original dataset's, in order.

func mirrorRwrxItem(t *testing.T) seeddata.ShowcaseArtifactDef {
	t.Helper()
	var found []seeddata.ShowcaseArtifactDef
	for _, a := range seeddata.ShowcaseArtifacts {
		if a.Slug == seeddata.MirrorRwrxSlug {
			found = append(found, a)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s registered %d times, want once", seeddata.MirrorRwrxSlug, len(found))
	}
	return found[0]
}

type mirrorRwrxManifest struct {
	EntryPoint string `json:"entrypoint"`
	Files      []struct {
		Path      string `json:"path"`
		MediaType string `json:"media_type"`
		Content   string `json:"content"`
	} `json:"files"`
	Message string `json:"message"`
}

func mirrorRwrxFiles(t *testing.T, item seeddata.ShowcaseArtifactDef) (mirrorRwrxManifest, map[string]artifacts.VersionFile) {
	t.Helper()
	raw := seeddata.ShowcaseFile(item.File)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	for k := range fields {
		if k != "entrypoint" && k != "files" && k != "message" {
			t.Errorf("manifest field %q is refused by POST /api/v1/pages/artifacts", k)
		}
	}
	var m mirrorRwrxManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	files := map[string]artifacts.VersionFile{}
	for _, f := range m.Files {
		if _, dup := files[f.Path]; dup {
			t.Errorf("duplicate file %s", f.Path)
		}
		files[f.Path] = artifacts.VersionFile{Content: []byte(f.Content), MediaType: f.MediaType}
	}
	return m, files
}

func TestShowcaseMirrorRwrx_Registered(t *testing.T) {
	t.Parallel()
	item := mirrorRwrxItem(t)
	cases := []struct{ name, got, want string }{
		{"slug prefix", item.Slug[:4], "ref-"},
		{"name is the original title", item.Name, "Template Crewship Dashboard"},
		{"kind", item.Kind, "dashboard"},
		{"manifest", item.File, "mirror/rwrx/manifest.json"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	if len(item.Data) != 1 || item.Data[0].Collection != "tasks" || item.Data[0].File != "mirror/rwrx/data-tasks.json" {
		t.Errorf("data = %+v, want the tasks collection", item.Data)
	}
	for _, p := range seeddata.Pages {
		if p.Slug == item.Slug {
			t.Errorf("slug %s collides with a demo Page", item.Slug)
		}
	}
}

func TestShowcaseMirrorRwrx_Manifest(t *testing.T) {
	t.Parallel()
	item := mirrorRwrxItem(t)
	m, files := mirrorRwrxFiles(t, item)
	if m.EntryPoint != "index.html" || m.Message == "" {
		t.Errorf("entrypoint %q, message %q", m.EntryPoint, m.Message)
	}
	wantTypes := map[string]string{"index.html": "text/html", "theme.css": "text/css", "interactive.json": "application/json", "dashboard.json": "application/json", "datasets.json": "application/json", "viewer-filters.json": "application/json"}
	if len(files) != len(wantTypes) {
		t.Errorf("files = %d, want %d", len(files), len(wantTypes))
	}
	for path, mt := range wantTypes {
		if files[path].MediaType != mt {
			t.Errorf("%s media type %q, want %q", path, files[path].MediaType, mt)
		}
	}
	collections, err := artifacts.ParseInteractiveArtifact(files)
	if err != nil || !reflect.DeepEqual(collections, []string{"tasks"}) {
		t.Fatalf("interactive collections = %v, %v; want [tasks]", collections, err)
	}

	if !artifacts.InteractiveViewerFilters(files) {
		t.Fatal("seed Dashboard must opt into browser-local filters")
	}

	html := string(files["index.html"].Content)
	original := string(seeddata.ShowcaseFile("mirror/rwrx/original/index.html"))
	pageAt := strings.Index(html, original)
	ordered := []struct{ name, needle string }{
		{"language", `<html lang="cs">`},
		{"title", "<title>Template Crewship Dashboard</title>"},
		{"d3 v7 inline", "// https://d3js.org v7.9.0 "},
		{"dash runtime", "window.dash = Object.freeze("},
		{"dash root", `<div id="dash-root" data-dash-one-writer>`},
		{"original page", original},
		{"start after the page's scripts", "<script>window.__dashStart()</script>"},
	}
	last := -1
	for _, o := range ordered {
		at := strings.Index(html, o.needle)
		if at < 0 {
			t.Errorf("index.html lacks %s", o.name)
			continue
		}
		if at <= last {
			t.Errorf("index.html: %s out of order", o.name)
		}
		last = at
	}
	if runtime := strings.TrimRight(string(seeddata.ShowcaseFile("mirror/rwrx/host/runtime.js")), "\n"); !strings.Contains(html, runtime) {
		t.Error("index.html must inline host/runtime.js as it is; rerun build.py")
	}
	var datasets struct {
		SchemaVersion int `json:"schema_version"`
		Datasets      map[string]struct {
			Title, Description, Mode string
			Sample                   bool
		} `json:"datasets"`
	}
	if err := json.Unmarshal(files["datasets.json"].Content, &datasets); err != nil || datasets.SchemaVersion != 1 || len(datasets.Datasets) != 1 {
		t.Fatalf("datasets.json = %s, %v; want schema 1 with the tasks dataset", files["datasets.json"].Content, err)
	}
	if task := datasets.Datasets["tasks"]; task.Mode != "fixed" || !task.Sample || !strings.HasPrefix(task.Title, "Sample") || task.Description == "" {
		t.Errorf("datasets.json tasks = %+v; want the original's title and description, fixed, sample", task)
	}
	if pageAt < 0 || len(original) < 10000 {
		t.Errorf("index.html must hold the original page verbatim (%d bytes)", len(original))
	}
	if strings.Contains(html, "<script src") || strings.Contains(html, "<link") {
		t.Error("the artifact runtime drops external scripts and links; everything must be inline")
	}
	if strings.Count(html, "</script>") != 4 {
		t.Errorf("index.html has %d scripts, want d3, runtime, the page's own and the start", strings.Count(html, "</script>"))
	}
	if total := len(seeddata.ShowcaseFile(item.File)); total > 2<<20 {
		t.Errorf("manifest %d bytes exceeds the 2 MiB interactive limit", total)
	}
	if css := string(files["theme.css"].Content); !strings.Contains(css, "--cds-chart-categorical-1") || !strings.Contains(css, "prefers-color-scheme: dark") {
		t.Error("theme.css must carry the host tokens in light and dark")
	}
}

func TestShowcaseMirrorRwrx_DataIsTheOriginalRows(t *testing.T) {
	t.Parallel()
	item := mirrorRwrxItem(t)
	batch, err := artifacts.DecodeDataBatch(seeddata.ShowcaseFile(item.Data[0].File))
	if err != nil {
		t.Fatalf("data batch refused: %v", err)
	}
	var original []map[string]any
	if err := json.Unmarshal(seeddata.ShowcaseFile("mirror/rwrx/original/sample-crew-tasks.json"), &original); err != nil {
		t.Fatal(err)
	}
	if batch.BaseVersion != 1 || len(batch.Documents) != len(original) || len(original) != 18 {
		t.Fatalf("batch: version %d, %d documents, original %d rows", batch.BaseVersion, len(batch.Documents), len(original))
	}
	prev := ""
	for i, doc := range batch.Documents {
		// The store returns documents in id order; ids keep the original row order.
		if doc.ID <= prev {
			t.Errorf("document %d id %q does not sort after %q", i, doc.ID, prev)
		}
		prev = doc.ID
		if doc.IfVersion != 0 {
			t.Errorf("document %s ifVersion %d, want 0 (create)", doc.ID, doc.IfVersion)
		}
		var value map[string]any
		if err := json.Unmarshal(doc.Value, &value); err != nil || !reflect.DeepEqual(value, original[i]) {
			t.Errorf("document %s = %v, want original row %v", doc.ID, value, original[i])
		}
	}
}

func TestShowcaseMirrorRwrx_SeedIsIdempotent(t *testing.T) {
	t.Parallel()
	slug := seeddata.MirrorRwrxSlug
	cases := []struct {
		name  string
		runs  []showcaseKnobs
		lines []string
		rows  int
	}{
		{"created then left alone", []showcaseKnobs{allOn, allOn}, []string{"  + " + slug + "\n", "  = " + slug + ": already exists, left unchanged"}, 18},
		{"refused data is written by the next run", []showcaseKnobs{{dataStatus: 503}, allOn, allOn},
			[]string{"  ! " + slug + ": incomplete (data tasks not written", "  + " + slug + ": completed", "  = " + slug + ": already exists, left unchanged"}, 18},
		{"artifacts unavailable creates nothing", []showcaseKnobs{{artifactStatus: map[string]int{slug: 404}}}, []string{"  = " + slug + ": skipped (dashboard artifacts unavailable"}, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newShowcaseServer(tc.runs[0])
			srv := httptest.NewServer(s.handler(t))
			defer srv.Close()
			for i, knobs := range tc.runs {
				s.setKnobs(knobs)
				var out bytes.Buffer
				if _, err := seedShowcase(context.Background(), cli.NewClient(srv.URL, "tok", showcaseTestWS), &out); err != nil {
					t.Fatalf("run %d: %v", i, err)
				}
				if !strings.Contains(out.String(), tc.lines[i]) {
					t.Errorf("run %d lacks %q\n%s", i, tc.lines[i], out.String())
				}
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			p := s.pages[slug]
			switch {
			case tc.rows < 0 && p != nil:
				t.Errorf("%s created although the server refused it", slug)
			case tc.rows >= 0 && (p == nil || !p.artifact || p.data["tasks"] != tc.rows):
				t.Errorf("%s = %+v, want an artifact with %d tasks", slug, p, tc.rows)
			}
		})
	}
}
