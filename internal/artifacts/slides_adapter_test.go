package artifacts

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/slides"
)

const slideTextStyle = "left:80px;top:80px;width:1600px;height:180px;font-size:48px;color:#112233;text-align:left"

func slideText(id, text string) string {
	return `<p id="` + id + `" style="` + slideTextStyle + `">` + text + `</p>`
}
func slideSource(id, children string) string {
	return `<section id="` + id + `">` + children + `</section>`
}
func slideFiles(order []string, bodies ...string) map[string]VersionFile {
	quoted := make([]string, len(order))
	for i, id := range order {
		quoted[i] = fmt.Sprintf("%q", id)
	}
	files := map[string]VersionFile{"deck.json": {Content: []byte(`{"schema_version":1,"order":[` + strings.Join(quoted, ",") + `]}`), MediaType: "application/json"}}
	for i, id := range order {
		if i < len(bodies) {
			files["slides/"+id+".html"] = VersionFile{Content: []byte(slideSource(id, bodies[i])), MediaType: "text/html"}
		}
	}
	return files
}
func TestSlidesArtifactMixedDeckAndAuthoredAnchors(t *testing.T) {
	shape := `<div id="shape" data-shape="ellipse" style="left:10px;top:300px;width:400px;height:200px;background-color:#DDEEFF;border-color:#112233;border-width:2px"></div>`
	table := `<table id="sales" style="left:500px;top:300px;width:800px;height:300px;font-size:24px;color:#112233;background-color:#FFFFFF;border-color:#112233"><tbody><tr><td>North</td> <td>12</td></tr><tr><td>South</td><td>25</td></tr></tbody></table>`
	files := slideFiles([]string{"second", "first"}, slideText("heading", " Žluťoučký&#160;  kůň &amp; řeka\n")+shape+table+`<aside id="notes">Příští krok</aside>`, slideText("end", "Done"))
	original := string(files["slides/second.html"].Content)
	got, err := ParseSlidesArtifact("page-id", files)
	if err != nil {
		t.Fatal(err)
	}
	if err := slides.Validate(got.Deck); err != nil {
		t.Fatal(err)
	}
	if got.Deck.Width != 1920 || got.Deck.Height != 1080 || got.Deck.SchemaVersion != 1 || got.Deck.Theme.FontFamily != "Go Regular" || !reflect.DeepEqual(got.Deck.SlideOrder, []string{"second", "first"}) {
		t.Fatalf("wrong projection: %+v", got.Deck)
	}
	first := got.Deck.Slides[0]
	if first.ID != "second" || first.Notes != "Příští krok" || len(first.Elements) != 3 || first.Elements[1].Shape.Kind != "ellipse" || !reflect.DeepEqual(first.Elements[2].Table.Cells, [][]string{{"North", "12"}, {"South", "25"}}) {
		t.Fatalf("lost content: %+v", first)
	}
	anchor := got.Anchors["heading"]
	if anchor.Path != "slides/second.html" || anchor.Selector != "#heading" || anchor.Quote != "Žluťoučký kůň & řeka" || anchor.SlideID != "second" {
		t.Fatalf("wrong authored anchor: %+v", anchor)
	}
	if got.Anchors["sales"].Quote != "North 12South25" {
		t.Fatalf("table quote must preserve original DOM text order: %+v", got.Anchors["sales"])
	}
	if _, ok := got.Anchors["shape"]; ok {
		t.Fatal("invented a quote for empty shape")
	}
	if _, ok := got.Anchors["second"]; ok {
		t.Fatal("invented a whole-section quote")
	}
	if string(files["slides/second.html"].Content) != original {
		t.Fatal("modified source authority")
	}
}
func TestSlidesArtifactRefusesUnsupportedWithoutPartialProjection(t *testing.T) {
	valid := slideText("title", "Hello")
	cases := map[string]func(map[string]VersionFile){
		"missing slide": func(f map[string]VersionFile) { delete(f, "slides/intro.html") },
		"missing deck":  func(f map[string]VersionFile) { delete(f, "deck.json") },
		"traversal": func(f map[string]VersionFile) {
			f["deck.json"] = VersionFile{Content: []byte(`{"schema_version":1,"order":["../intro"]}`), MediaType: "application/json"}
		},
		"duplicate order": func(f map[string]VersionFile) {
			f["deck.json"] = VersionFile{Content: []byte(`{"schema_version":1,"order":["intro","intro"]}`), MediaType: "application/json"}
		},
		"duplicate JSON key": func(f map[string]VersionFile) {
			f["deck.json"] = VersionFile{Content: []byte(`{"schema_version":1,"order":["intro"],"order":["intro"]}`), MediaType: "application/json"}
		},
		"unknown JSON key": func(f map[string]VersionFile) {
			f["deck.json"] = VersionFile{Content: []byte(`{"schema_version":1,"order":["intro"],"url":"https://evil.test"}`), MediaType: "application/json"}
		},
		"trailing JSON": func(f map[string]VersionFile) {
			v := f["deck.json"]
			v.Content = append(v.Content, []byte(`{}`)...)
			f["deck.json"] = v
		},
		"wrong casing": func(f map[string]VersionFile) {
			f["deck.json"] = VersionFile{Content: []byte(`{"Schema_Version":1,"order":["intro"]}`), MediaType: "application/json"}
		},
		"extra file": func(f map[string]VersionFile) { f["slides/extra.html"] = VersionFile{Content: []byte("unused")} },
	}
	invalidHTML := map[string]string{
		"script": valid + `<script>alert(1)</script>`, "network": valid + `<img src="https://evil.test">`, "handler": strings.Replace(valid, `id="title"`, `id="title" onclick="alert(1)"`, 1),
		"CSS URL": strings.Replace(valid, "#112233", "url(https://evil.test)", 1), "unknown style": strings.Replace(valid, "text-align:left", "text-align:left;filter:blur(1px)", 1),
		"duplicate style": strings.Replace(valid, "text-align:left", "text-align:left;color:#000000", 1), "duplicate attr": strings.Replace(valid, `id="title"`, `id="title" id="other"`, 1),
		"duplicate ID": valid + valid, "missing ID": strings.Replace(valid, ` id="title"`, "", 1), "section ID collision": strings.Replace(valid, `id="title"`, `id="intro"`, 1),
		"notes ID collision": valid + `<aside id="title">Note</aside>`, "unknown font": strings.Replace(valid, "text-align:left", "text-align:left;font-family:RemoteFont", 1),
		"geometry overflow": strings.Replace(valid, "width:1600px", "width:1920px", 1), "fractional geometry": strings.Replace(valid, "left:80px", "left:0.5px", 1),
		"negative geometry": strings.Replace(valid, "left:80px", "left:-1px", 1), "unknown tag": `<h1 id="title">Hello</h1>`, "nested markup": slideText("title", "<b>Hello</b>"),
		"malformed": strings.TrimSuffix(valid, "</p>"), "comment": valid + `<!-- hidden -->`, "text overflow": slideText("title", strings.Repeat("x", 32769)),
		"notes overflow": valid + `<aside id="notes">` + strings.Repeat("ž", 4001) + `</aside>`, "duplicate notes": valid + `<aside id="n1">One</aside><aside id="n2">Two</aside>`,
	}
	for name, body := range invalidHTML {
		body := body
		cases[name] = func(f map[string]VersionFile) {
			f["slides/intro.html"] = VersionFile{Content: []byte(slideSource("intro", body)), MediaType: "text/html"}
		}
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			files := slideFiles([]string{"intro"}, valid)
			mutate(files)
			got, err := ParseSlidesArtifact("page-id", files)
			if !errors.Is(err, ErrSlidesArtifactUnsupported) {
				t.Fatalf("expected explicit unsupported: %v", err)
			}
			if got.Deck.DeckID != "" || len(got.Anchors) != 0 {
				t.Fatal("returned partial projection")
			}
		})
	}
}
func TestSlidesArtifactBoundariesAndGlobalIDs(t *testing.T) {
	build := func(counts ...int) map[string]VersionFile {
		order := make([]string, len(counts))
		bodies := make([]string, len(counts))
		for i, count := range counts {
			order[i] = fmt.Sprintf("s%d", i)
			for n := 0; n < count; n++ {
				bodies[i] += slideText(fmt.Sprintf("e%d_%d", i, n), "Title")
			}
		}
		return slideFiles(order, bodies...)
	}
	for _, counts := range [][]int{{200}, {200, 200, 200, 200, 200}} {
		if _, err := ParseSlidesArtifact("page-id", build(counts...)); err != nil {
			t.Fatalf("valid boundary %v: %v", counts, err)
		}
	}
	for _, counts := range [][]int{{201}, {200, 200, 200, 200, 200, 1}} {
		if _, err := ParseSlidesArtifact("page-id", build(counts...)); err == nil {
			t.Fatalf("overflow admitted %v", counts)
		}
	}
	counts := make([]int, 100)
	for i := range counts {
		counts[i] = 1
	}
	if _, err := ParseSlidesArtifact("page-id", build(counts...)); err != nil {
		t.Fatal(err)
	}
	counts = append(counts, 1)
	if _, err := ParseSlidesArtifact("page-id", build(counts...)); err == nil {
		t.Fatal("101 slides admitted")
	}
	files := slideFiles([]string{"one", "two"}, slideText("same", "First"), slideText("same", "Second"))
	if _, err := ParseSlidesArtifact("page-id", files); err == nil {
		t.Fatal("cross-slide duplicate ID admitted")
	}
	files = slideFiles([]string{"intro"}, slideText("title", strings.Repeat("ž", 5000))+`<aside id="note">`+strings.Repeat("ž", 4000)+`</aside>`)
	got, err := ParseSlidesArtifact("page-id", files)
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(got.Anchors["title"].Quote) != 500 {
		t.Fatal("quote must be the existing 500-codepoint canonical prefix")
	}
}

func TestSlidesArtifactQuoteUsesOriginalTextAndBoundedCanonicalPrefix(t *testing.T) {
	for _, text := range []string{strings.Repeat("😀", 5001), strings.Repeat("a", 499) + " rest", "\ufeff" + strings.Repeat("ž", 510)} {
		files := slideFiles([]string{"intro"}, slideText("title", text))
		got, err := ParseSlidesArtifact("page-id", files)
		if err != nil {
			t.Fatal(err)
		}
		want := strings.Join(strings.Fields(text), " ")
		runes := []rune(want)
		if len(runes) > 500 {
			want = strings.Join(strings.Fields(string(runes[:500])), " ")
		}
		quote := got.Anchors["title"].Quote
		if quote != want || len(quote) > 2000 || !utf8.ValidString(quote) {
			t.Fatalf("unsafe prefix (%d bytes): %q", len(quote), quote)
		}
		if got.Deck.Slides[0].Elements[0].Text.Value != text {
			t.Fatal("anchor truncation changed business text")
		}
	}
}

func TestSlidesArtifactStrictBytesTypesAndTableBudgets(t *testing.T) {
	table := func(id string, rows, columns int, value string) string {
		body := `<table id="` + id + `" style="left:0px;top:0px;width:1800px;height:900px;font-size:24px;color:#111111;background-color:#FFFFFF;border-color:#111111"><tbody>`
		for row := 0; row < rows; row++ {
			body += "<tr>"
			for column := 0; column < columns; column++ {
				body += "<td>" + value + "</td>"
			}
			body += "</tr>"
		}
		return body + "</tbody></table>"
	}
	for name, body := range map[string]string{
		"rows": table("t", 31, 1, "x"), "columns": table("t", 1, 21, "x"), "cell bytes": table("t", 1, 1, strings.Repeat("x", 4097)),
		"slide cells":       table("t1", 30, 20, "x") + table("t2", 30, 20, "x") + table("t3", 30, 20, "x") + table("t4", 30, 20, "x"),
		"boolean duplicate": strings.Replace(slideText("title", "Hello"), `id="title"`, `id="title" id`, 1),
		"case duplicate":    strings.Replace(slideText("title", "Hello"), `id="title"`, `id="title" ID="other"`, 1),
		"source bytes":      strings.Repeat(" ", artifactSlideFileBytes) + slideText("title", "Hello"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSlidesArtifact("page", slideFiles([]string{"intro"}, body)); !errors.Is(err, ErrSlidesArtifactUnsupported) {
				t.Fatalf("unsafe fixture accepted: %v", err)
			}
		})
	}
	for _, index := range []string{`{"schema_version":"1","order":["intro"]}`, `{"schema_version":1.0,"order":["intro"]}`, `{"schema_version":1,"order":null}`, `{"schema_version":1,"order":["intro"],"\u006frder":["intro"]}`} {
		files := slideFiles([]string{"intro"}, slideText("title", "Hello"))
		files["deck.json"] = VersionFile{Content: []byte(index), MediaType: "application/json"}
		if _, err := ParseSlidesArtifact("page", files); err == nil {
			t.Fatalf("index admitted: %s", index)
		}
	}
	for _, family := range []string{"Go Regular", "'Go Regular'", "sans-serif"} {
		body := strings.Replace(slideText("title", "Hello"), "text-align:left", "text-align:left;font-family:"+family, 1)
		got, err := ParseSlidesArtifact("page", slideFiles([]string{"intro"}, body))
		if err != nil {
			t.Fatal(err)
		}
		if got.Deck.Slides[0].Elements[0].Text.FontFamily != "Go Regular" {
			t.Fatal("noncanonical font")
		}
	}
	files := slideFiles([]string{"intro"}, slideText("title", "Hello"))
	file := files["slides/intro.html"]
	file.Content = append(file.Content, 0xff)
	files["slides/intro.html"] = file
	if _, err := ParseSlidesArtifact("page", files); err == nil {
		t.Fatal("invalid UTF-8 admitted")
	}
	if _, err := ParseSlidesArtifact("", slideFiles([]string{"intro"}, slideText("title", "Hello"))); err == nil {
		t.Fatal("missing existing Page identity admitted")
	}
}

func TestSlidesArtifactAggregateBudgets(t *testing.T) {
	order := []string{}
	bodies := []string{}
	for slide := 0; slide < 10; slide++ {
		order = append(order, fmt.Sprintf("s%d", slide))
		body := ""
		for element := 0; element < 10; element++ {
			body += slideText(fmt.Sprintf("e%d_%d", slide, element), strings.Repeat("x", 20830))
		}
		bodies = append(bodies, body)
	}
	files := slideFiles(order, bodies...)
	sourceBytes := 0
	for _, file := range files {
		sourceBytes += len(file.Content)
	}
	if sourceBytes > artifactSlideSourceBytes {
		t.Fatal("fixture must reach canonical admission, not source cap")
	}
	if _, err := ParseSlidesArtifact("page", files); err == nil || !strings.Contains(err.Error(), "typed deck validation") {
		t.Fatalf("canonical deck overflow not refused: %v", err)
	}
	// Also refuse an aggregate source overflow even when every individual file is bounded.
	for index := range bodies {
		bodies[index] += strings.Repeat(" ", 10000)
	}
	if _, err := ParseSlidesArtifact("page", slideFiles(order, bodies...)); err == nil || !strings.Contains(err.Error(), "source byte budget") {
		t.Fatalf("aggregate source overflow not refused: %v", err)
	}
}
