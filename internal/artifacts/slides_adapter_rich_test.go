package artifacts

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/slides"
)

const richText = `<p id="title" style="left:128px;top:128px;width:1100px;height:160px;font-size:144px;color:#F4F1EA;text-align:left;font-family:'IBM Plex Sans';font-weight:700;letter-spacing:4px;line-height:1.05">Crewship</p>`
const richPanel = `<div id="panel" data-shape="rectangle" style="left:1200px;top:0px;width:720px;height:1080px;background-color:#16304F;border-color:#16304F;border-width:0px;border-radius:24px;background-image:linear-gradient(160deg, #16304F 0%, #0F1F33 70%)"></div>`
const richLine = `<div id="link" data-shape="line" data-line="0 12 96 12" data-arrow-end="triangle" style="left:472px;top:532px;width:96px;height:24px;border-color:#E8833A;border-width:4px"></div>`
const richIcon = `<div id="icon" data-icon="users" style="left:1440px;top:400px;width:280px;height:280px;color:#E8833A"></div>`

func TestSlidesArtifactRichProfile(t *testing.T) {
	files := slideFiles([]string{"cover"}, richText+richPanel+richLine+richIcon)
	files["slides/cover.html"] = VersionFile{Content: []byte(`<section id="cover" data-transition="fade">` + richText + richPanel + richLine + richIcon + `</section>`), MediaType: "text/html"}
	got, err := ParseSlidesArtifact("page-id", files)
	if err != nil {
		t.Fatal(err)
	}
	if got.Deck.SchemaVersion != slides.RichSchemaVersion || got.Deck.Slides[0].Transition != "fade" {
		t.Fatalf("schema %d transition %q", got.Deck.SchemaVersion, got.Deck.Slides[0].Transition)
	}
	e := got.Deck.Slides[0].Elements
	wantText := &slides.Text{Value: "Crewship", FontFamily: "IBM Plex Sans", FontSize: 144, Color: "#F4F1EA", Align: "left", FontWeight: 700, LetterSpacing: 4, LineHeight: 105}
	if !reflect.DeepEqual(e[0].Text, wantText) {
		t.Errorf("text = %+v", e[0].Text)
	}
	wantPanel := &slides.Shape{Kind: "rectangle", Fill: "#16304F", Stroke: "#16304F", CornerRadius: 24, Gradient: &slides.Gradient{Angle: 160, Stops: []slides.GradientStop{{Color: "#16304F", Position: 0}, {Color: "#0F1F33", Position: 70}}}}
	if !reflect.DeepEqual(e[1].Shape, wantPanel) {
		t.Errorf("panel = %+v", e[1].Shape)
	}
	wantLine := &slides.Shape{Kind: "line", Fill: "#E8833A", Stroke: "#E8833A", StrokeWidth: 4, Line: &slides.Line{X1: 0, Y1: 12, X2: 96, Y2: 12, Start: "none", End: "triangle"}}
	if !reflect.DeepEqual(e[2].Shape, wantLine) {
		t.Errorf("line = %+v", e[2].Shape)
	}
	if e[3].Type != "icon" || !reflect.DeepEqual(e[3].Icon, &slides.Icon{Name: "users", Color: "#E8833A"}) {
		t.Errorf("icon = %+v", e[3])
	}
	if _, ok := got.Anchors["icon"]; ok {
		t.Error("invented a quote for an icon")
	}
}

// A deck without rich fields keeps exactly its schema-1 projection, including
// the legacy font spellings, and its JSON carries none of the new keys.
func TestSlidesArtifactPlainDeckStaysSchemaOne(t *testing.T) {
	legacy := strings.Replace(slideText("a", "x"), "text-align:left", "text-align:left;font-family:'Go Regular'", 1) +
		strings.Replace(slideText("b", "y"), "text-align:left", "text-align:left;font-family:sans-serif", 1) +
		strings.Replace(slideText("c", "z"), "text-align:left", "text-align:left;font-weight:400", 1) +
		strings.Replace(slideText("d", "w"), "text-align:left", "text-align:left;line-height:1.2", 1)
	got, err := ParseSlidesArtifact("page-id", slideFiles([]string{"intro"}, legacy))
	if err != nil {
		t.Fatal(err)
	}
	if got.Deck.SchemaVersion != 1 {
		t.Fatalf("schema %d", got.Deck.SchemaVersion)
	}
	if err := slides.Validate(got.Deck); err != nil {
		t.Fatalf("stored-schema Validate refused a plain projection: %v", err)
	}
	raw, _ := json.Marshal(got)
	for _, key := range []string{"font_weight", "letter_spacing", "line_height", "corner_radius", "gradient", "transition", `"line"`, `"icon"`} {
		if strings.Contains(string(raw), key) {
			t.Errorf("schema-1 projection JSON carries %s", key)
		}
	}
}

func TestSlidesArtifactRichProfileRefusals(t *testing.T) {
	cases := map[string]string{
		"font fallback list":       strings.Replace(richText, `'IBM Plex Sans'`, `'IBM Plex Sans', Arial, sans-serif`, 1),
		"remote font":              strings.Replace(richText, `'IBM Plex Sans'`, `url(https://evil.test/f.woff2)`, 1),
		"unknown family":           strings.Replace(richText, `'IBM Plex Sans'`, `Arial`, 1),
		"mismatched quotes":        strings.Replace(richText, `'IBM Plex Sans'`, `'IBM Plex Sans"`, 1),
		"keyword weight":           strings.Replace(richText, "font-weight:700", "font-weight:bold", 1),
		"unavailable weight":       strings.Replace(richText, "font-weight:700", "font-weight:300", 1),
		"mono bold":                strings.Replace(strings.Replace(richText, `'IBM Plex Sans'`, `'JetBrains Mono'`, 1), "font-weight:700", "font-weight:700", 1),
		"em letter spacing":        strings.Replace(richText, "letter-spacing:4px", "letter-spacing:0.1em", 1),
		"huge letter spacing":      strings.Replace(richText, "letter-spacing:4px", "letter-spacing:101px", 1),
		"line height with unit":    strings.Replace(richText, "line-height:1.05", "line-height:40px", 1),
		"line height too small":    strings.Replace(richText, "line-height:1.05", "line-height:0.5", 1),
		"line height three digits": strings.Replace(richText, "line-height:1.05", "line-height:1.055", 1),
		"font size above max":      strings.Replace(richText, "font-size:144px", "font-size:321px", 1),
		"weight on table":          `<table id="t" style="left:0px;top:0px;width:100px;height:100px;font-size:24px;color:#000000;background-color:#FFFFFF;border-color:#000000;font-weight:700"><tbody><tr><td>a</td></tr></tbody></table>`,
		"radius on ellipse":        strings.Replace(richPanel, `data-shape="rectangle"`, `data-shape="ellipse"`, 1),
		"radius too large":         strings.Replace(richPanel, "border-radius:24px", "border-radius:361px", 1),
		"radial gradient":          strings.Replace(richPanel, "linear-gradient(160deg", "radial-gradient(160deg", 1),
		"gradient url":             strings.Replace(richPanel, "linear-gradient(160deg, #16304F 0%, #0F1F33 70%)", "url(https://evil.test/x.png)", 1),
		"gradient named colour":    strings.Replace(richPanel, "#0F1F33 70%", "red 70%", 1),
		"gradient rgba":            strings.Replace(richPanel, "#0F1F33 70%", "rgba(0,0,0,1) 70%", 1),
		"gradient one stop":        strings.Replace(richPanel, ", #0F1F33 70%", "", 1),
		"gradient decreasing":      strings.Replace(richPanel, "#0F1F33 70%", "#0F1F33 0%", 1) + strings.Replace(strings.Replace(richPanel, `id="panel"`, `id="panel2"`, 1), "#16304F 0%", "#16304F 90%", 1),
		"gradient angle 360":       strings.Replace(richPanel, "160deg", "360deg", 1),
		"gradient turn unit":       strings.Replace(richPanel, "160deg", "0.5turn", 1),
		"gradient trailing":        strings.Replace(richPanel, "70%)", "70%), url(x)", 1),
		"line outside box":         strings.Replace(richLine, `data-line="0 12 96 12"`, `data-line="0 12 97 12"`, 1),
		"line negative":            strings.Replace(richLine, `data-line="0 12 96 12"`, `data-line="-1 12 96 12"`, 1),
		"line three numbers":       strings.Replace(richLine, `data-line="0 12 96 12"`, `data-line="0 12 96"`, 1),
		"line without points":      strings.Replace(richLine, ` data-line="0 12 96 12"`, "", 1),
		"line unknown arrow":       strings.Replace(richLine, `data-arrow-end="triangle"`, `data-arrow-end="circle"`, 1),
		"line with fill":           strings.Replace(richLine, "border-width:4px", "border-width:4px;background-color:#000000", 1),
		"arrow on rectangle":       strings.Replace(richPanel, `data-shape="rectangle"`, `data-shape="rectangle" data-arrow-end="triangle"`, 1),
		"unknown icon":             strings.Replace(richIcon, `data-icon="users"`, `data-icon="skull"`, 1),
		"icon svg payload":         strings.Replace(richIcon, `data-icon="users"`, `data-icon="&lt;svg onload=alert(1)&gt;"`, 1),
		"icon and shape":           strings.Replace(richIcon, `data-icon="users"`, `data-icon="users" data-shape="rectangle"`, 1),
		"icon with text":           strings.Replace(richIcon, `></div>`, `>Users</div>`, 1),
		"icon fill style":          strings.Replace(richIcon, "color:#E8833A", "color:#E8833A;background-color:#000000", 1),
		"icon currentColor":        strings.Replace(richIcon, "color:#E8833A", "color:currentColor", 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseSlidesArtifact("page-id", slideFiles([]string{"intro"}, body))
			if !errors.Is(err, ErrSlidesArtifactUnsupported) {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(got, ArtifactSlidesProjection{}) {
				t.Fatal("partial projection")
			}
		})
	}
	for name, section := range map[string]string{
		"unknown transition": `<section id="intro" data-transition="spin">` + richText + `</section>`,
		"build-in attribute": `<section id="intro" data-build-in="pop">` + richText + `</section>`,
	} {
		t.Run(name, func(t *testing.T) {
			files := slideFiles([]string{"intro"}, richText)
			files["slides/intro.html"] = VersionFile{Content: []byte(section), MediaType: "text/html"}
			if _, err := ParseSlidesArtifact("page-id", files); !errors.Is(err, ErrSlidesArtifactUnsupported) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
