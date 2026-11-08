package main

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/cli"
)

// The original deck (claude.ai/artifact/CBBoNZhbaHvUUUz2aQ2Ccv), slide by
// slide: its speaker notes and every visible text, verbatim. Two eyebrows are
// upper-cased by CSS in the original; the mirror holds the text as shown.
var mirrorCbboOriginal = []struct {
	id    string
	notes string
	texts []string
}{
	{"cover", "Titulní slide. Ukazuje vlastní typografii (IBM Plex Sans + JetBrains Mono), gradient na pozadí, ikonu s animací pop a přechod fade.",
		[]string{"TEMPLATE · SLIDES", "Crewship", "Posádka AI agentů, která převezme úkol a doručí výsledek.", "Testovací deck · listopad 2026"}},
	{"features", "Karty s ikonami. Každý prvek jde v editoru přesunout, přepsat a okomentovat.",
		[]string{"CO UMÍ SLIDES", "Tři věci, které deck zvládne sám",
			"Komentáře", "Kolega napíše poznámku přímo k prvku slidu.",
			"Prezentace", "Režim Present s přechody a animacemi prvků.",
			"Export", "Stažení jako .pptx nebo PDF jedním klikem.",
			"Template Crewship Slides · 2"}},
	{"chart", "Graf jako SVG přímo ve slidu, velké číslo jako hlavní sdělení. Data jsou testovací.",
		[]string{"Dokončené úkoly rostou každý týden", "38", "úkolů v týdnu T6, trojnásobek oproti T1 (12)",
			"T1 · 12", "T2 · 18", "T3 · 15", "T4 · 24", "T5 · 31", "T6 · 38",
			"Testovací data · Template Crewship Slides · 3"}},
	{"flow", "Diagram z nativních prvků: boxy, x-connector šipky. Export do PPTX zachová editovatelné tvary.",
		[]string{"Jak crew pracuje", "Zadání", "CLI nebo web", "Kapitán", "rozdělí práci", "Agenti", "každý svou roli",
			"Výsledek", "k review", "Diagram ze tvarů a spojnic – v editoru jde každý box posunout.", "Template Crewship Slides · 4"}},
}

func mirrorCbboItem(t *testing.T) seeddata.ShowcaseArtifactDef {
	t.Helper()
	var found []seeddata.ShowcaseArtifactDef
	for _, a := range seeddata.ShowcaseArtifacts {
		if a.Slug == seeddata.MirrorCbboSlug {
			found = append(found, a)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s registered %d times, want once", seeddata.MirrorCbboSlug, len(found))
	}
	return found[0]
}

type mirrorCbboManifest struct {
	Entrypoint string `json:"entrypoint"`
	Message    string `json:"message"`
	Files      []struct {
		Path      string `json:"path"`
		MediaType string `json:"media_type"`
		Content   string `json:"content"`
	} `json:"files"`
}

func TestMirrorCbbo_Registered(t *testing.T) {
	t.Parallel()
	item := mirrorCbboItem(t)
	if item.Name != "Template Crewship Slides" || item.Kind != "slides" || item.File != "mirror/cbbo/m-deck.json" || len(item.Data) != 0 {
		t.Errorf("catalogue entry = %+v", item)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(seeddata.ShowcaseFile(item.File), &keys); err != nil {
		t.Fatal(err)
	}
	for k := range keys {
		if k != "entrypoint" && k != "files" && k != "message" {
			t.Errorf("manifest field %q is refused by POST /api/v1/pages/artifacts", k)
		}
	}
	if !strings.HasPrefix(string(seeddata.ShowcaseFile("mirror/cbbo/SOURCE.md")), "# Mirror: Template Crewship Slides") {
		t.Error("SOURCE.md is missing or untitled")
	}
}

// The posted manifest is exactly the readable sources next to it, so editing a
// slide file and forgetting to regenerate the manifest fails here.
func TestMirrorCbbo_ManifestMatchesSources(t *testing.T) {
	t.Parallel()
	var m mirrorCbboManifest
	if err := json.Unmarshal(seeddata.ShowcaseFile(mirrorCbboItem(t).File), &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"deck.json": "application/json"}
	for _, s := range mirrorCbboOriginal {
		want["slides/"+s.id+".html"] = "text/html"
	}
	if m.Entrypoint != "slides/cover.html" || len(m.Files) != len(want) {
		t.Fatalf("entrypoint %q, %d files; want slides/cover.html and %d files", m.Entrypoint, len(m.Files), len(want))
	}
	for _, f := range m.Files {
		if want[f.Path] != f.MediaType {
			t.Errorf("%s: media type %q, want %q", f.Path, f.MediaType, want[f.Path])
		}
		if src := string(seeddata.ShowcaseFile("mirror/cbbo/" + f.Path)); src != f.Content {
			t.Errorf("%s: manifest content differs from mirror/cbbo/%s", f.Path, f.Path)
		}
	}
}

// The server's own Slides admission accepts the deck, and it carries every
// slide, note and visible text of the original in the original order.
func TestMirrorCbbo_SlidesAdmission(t *testing.T) {
	t.Parallel()
	var m mirrorCbboManifest
	if err := json.Unmarshal(seeddata.ShowcaseFile(mirrorCbboItem(t).File), &m); err != nil {
		t.Fatal(err)
	}
	files := map[string]artifacts.VersionFile{}
	for _, f := range m.Files {
		files[f.Path] = artifacts.VersionFile{Content: []byte(f.Content), MediaType: f.MediaType}
	}
	projection, err := artifacts.ParseSlidesArtifact("page-id", files)
	if err != nil {
		t.Fatalf("Slides admission refused the mirror: %v", err)
	}
	deck := projection.Deck
	if len(deck.Slides) != len(mirrorCbboOriginal) {
		t.Fatalf("%d slides, want %d", len(deck.Slides), len(mirrorCbboOriginal))
	}
	for i, want := range mirrorCbboOriginal {
		t.Run(want.id, func(t *testing.T) {
			slide := deck.Slides[i]
			if deck.SlideOrder[i] != want.id || slide.ID != want.id {
				t.Fatalf("slide %d is %q, want %q", i+1, slide.ID, want.id)
			}
			if slide.Notes != want.notes {
				t.Errorf("notes = %q, want %q", slide.Notes, want.notes)
			}
			var texts []string
			for _, e := range slide.Elements {
				if e.X < 0 || e.Y < 0 || e.X+e.Width > deck.Width || e.Y+e.Height > deck.Height {
					t.Errorf("%s leaves the 1920x1080 canvas", e.ID)
				}
				if e.Text != nil {
					texts = append(texts, e.Text.Value)
				}
			}
			if strings.Join(texts, "\n") != strings.Join(want.texts, "\n") {
				t.Errorf("texts =\n%q\nwant\n%q", texts, want.texts)
			}
			if bg := slide.Elements[0]; bg.Shape == nil || bg.X != 0 || bg.Y != 0 || bg.Width != 1920 || bg.Height != 1080 {
				t.Errorf("first element %s is not the full-bleed background", bg.ID)
			}
		})
	}
}

// Idempotent by slug: the first seed creates the deck, a second one leaves it
// alone, and a Page that already holds the slug is never overwritten.
func TestMirrorCbbo_Idempotent(t *testing.T) {
	t.Parallel()
	item := mirrorCbboItem(t)
	cases := []struct {
		name    string
		prefill bool
		want    []showcaseOutcome
		creates int
	}{
		{name: "fresh workspace", want: []showcaseOutcome{showcaseCreated, showcaseExists}, creates: 1},
		{name: "slug already taken", prefill: true, want: []showcaseOutcome{showcaseExists, showcaseExists}, creates: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newShowcaseServer(allOn)
			if tc.prefill {
				s.pages[item.Slug] = &fakeShowcasePage{artifact: true, data: map[string]int{}}
			}
			srv := httptest.NewServer(s.handler(t))
			defer srv.Close()
			client := cli.NewClient(srv.URL, "tok", showcaseTestWS)
			for i, want := range tc.want {
				if got := seedShowcaseArtifact(client, io.Discard, item); got != want {
					t.Errorf("run %d: outcome %d, want %d", i+1, got, want)
				}
			}
			if got := s.count(exactly("POST /api/v1/pages/artifacts")); got != tc.creates {
				t.Errorf("artifact creates = %d, want %d", got, tc.creates)
			}
		})
	}
}
