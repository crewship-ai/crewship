package slidespdf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/phpdave11/gofpdi"
	"github.com/signintech/gopdf"

	"github.com/crewship-ai/crewship/internal/slides"
)

// Schema-1 output is pinned byte for byte: the rich profile must not change
// how an existing deck renders.
func TestRenderSchemaOneBytesUnchanged(t *testing.T) {
	data, err := Render(context.Background(), fixture())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != "1c81fb818c0be623dbf7e3afbaad45e2b0d14cbb9b72a1f70896c28af1980c4c" {
		t.Fatalf("schema-1 PDF changed: sha256 %s", got)
	}
}

func richDeck() slides.Deck {
	return slides.Deck{SchemaVersion: slides.RichSchemaVersion, DeckID: "rich", Width: 1920, Height: 1080, Theme: slides.Theme{Background: "#0F1F33", Color: "#F4F1EA", FontFamily: "Go Regular", FontSize: 32}, SlideOrder: []string{"cover"},
		Slides: []slides.Slide{{ID: "cover", Transition: "fade", Elements: []slides.Element{
			{ID: "panel", Type: "shape", X: 1200, Y: 0, Width: 720, Height: 1080, Shape: &slides.Shape{Kind: "rectangle", Fill: "#16304F", Stroke: "#16304F", Gradient: &slides.Gradient{Angle: 160, Stops: []slides.GradientStop{{Color: "#16304F", Position: 0}, {Color: "#0F1F33", Position: 70}}}}},
			{ID: "card", Type: "shape", X: 100, Y: 600, Width: 500, Height: 300, Shape: &slides.Shape{Kind: "rectangle", Fill: "#FFFDF8", Stroke: "#E2DCCF", StrokeWidth: 1, CornerRadius: 24}},
			{ID: "eyebrow", Type: "text", X: 128, Y: 128, Width: 1000, Height: 40, Text: &slides.Text{Value: "TEMPLATE · SLIDES", FontFamily: "JetBrains Mono", FontSize: 28, Color: "#E8833A", Align: "left", LetterSpacing: 4}},
			{ID: "title", Type: "text", X: 128, Y: 300, Width: 1400, Height: 160, Text: &slides.Text{Value: "Crewship", FontFamily: "IBM Plex Sans", FontSize: 240, Color: "#F4F1EA", Align: "left", FontWeight: 700, LineHeight: 80}},
			{ID: "body", Type: "text", X: 128, Y: 500, Width: 1000, Height: 80, Text: &slides.Text{Value: "Posádka AI agentů", FontFamily: "IBM Plex Sans", FontSize: 44, Color: "#BFCBD8", Align: "left", FontWeight: 600, LineHeight: 130}},
			{ID: "link", Type: "shape", X: 700, Y: 700, Width: 96, Height: 24, Shape: &slides.Shape{Kind: "line", Fill: "#E8833A", Stroke: "#E8833A", StrokeWidth: 4, Line: &slides.Line{X1: 0, Y1: 12, X2: 96, Y2: 12, Start: "triangle", End: "triangle"}}},
			{ID: "icon", Type: "icon", X: 1440, Y: 400, Width: 280, Height: 280, Icon: &slides.Icon{Name: "users", Color: "#E8833A"}},
			{ID: "grid", Type: "table", X: 900, Y: 800, Width: 300, Height: 100, Table: &slides.Table{Cells: [][]string{{"T6", "38"}}, FontFamily: "JetBrains Mono", FontSize: 24, Color: "#F4F1EA", Fill: "#16304F", Border: "#BFCBD8"}},
		}}}}
}

func TestRenderRichProfile(t *testing.T) {
	d := richDeck()
	// 240 px at line height 0.8 overflows the fixture's 160 px frame; the
	// refusal test relies on that, this one gives the title room.
	d.Slides[0].Elements[3].Text.LineHeight = 100
	d.Slides[0].Elements[3].Height = 300
	data, err := Render(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	var stream io.ReadSeeker = bytes.NewReader(data)
	reader := gofpdi.NewImporter()
	reader.SetSourceStream(&stream)
	if reader.GetNumPages() != 1 {
		t.Fatalf("pages=%d", reader.GetNumPages())
	}
	for _, face := range []string{"IBM+Plex+Sans+700", "IBM+Plex+Sans+600", "JetBrains+Mono+400"} {
		if !bytes.Contains(data, []byte("/BaseFont /"+face)) {
			t.Errorf("embedded face %s missing", face)
		}
	}
	if bytes.Contains(data, []byte("IBM+Plex+Sans+400")) || bytes.Contains(data, []byte("JetBrains+Mono+600")) {
		t.Error("an unused face was embedded")
	}
	if out := os.Getenv("SLIDESPDF_RICH_OUT"); out != "" {
		_ = os.WriteFile(out, data, 0o644)
	}
}

func TestRenderRichRefusals(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*slides.Deck)
		want   error
	}{
		{"weight without face", func(d *slides.Deck) { d.Slides[0].Elements[2].Text.FontWeight = 700 }, ErrUnsupported},
		{"unknown family", func(d *slides.Deck) { d.Slides[0].Elements[2].Text.FontFamily = "Arial" }, ErrUnsupported},
		{"unknown icon", func(d *slides.Deck) { d.Slides[0].Elements[6].Icon.Name = "nope" }, ErrInvalidDeck},
		{"rich field in schema 1", func(d *slides.Deck) { d.SchemaVersion = 1 }, ErrUnsupported},
		// Schema 1 admits any family name; only schema 3 selects the bundled faces.
		{"bundled face in schema 1", func(d *slides.Deck) {
			*d = fixture()
			d.Slides[0].Elements[0].Text.FontFamily = "IBM Plex Sans"
		}, ErrUnsupported},
		{"tight line height", func(d *slides.Deck) {}, ErrOverflow},
		{"letter spacing overflow", func(d *slides.Deck) {
			d.Slides[0].Elements[3].Text.LineHeight = 100
			d.Slides[0].Elements[3].Height = 300
			d.Slides[0].Elements[2].Width = 470
			d.Slides[0].Elements[2].Text.LetterSpacing = 20
		}, ErrOverflow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := richDeck()
			tt.mutate(&d)
			out, err := Render(context.Background(), d)
			if !errors.Is(err, tt.want) || out != nil {
				t.Fatalf("bytes=%d err=%v want=%v", len(out), err, tt.want)
			}
		})
	}
}

// Letter spacing widens a line by exactly spacing × glyphs, as CSS does.
func TestLayoutLetterSpacing(t *testing.T) {
	d := richDeck()
	l := layout{faces: map[string]*faceMetrics{}}
	pdf, err := newTestPDF(d)
	if err != nil {
		t.Fatal(err)
	}
	l.pdf = pdf
	plain, err := l.text(context.Background(), "SLIDES", textStyle{face: "JetBrains Mono 400", size: 28, color: "#000000", align: "left"}, 0, 0, 1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	spaced, err := l.text(context.Background(), "SLIDES", textStyle{face: "JetBrains Mono 400", size: 28, color: "#000000", align: "left", spacing: 4}, 0, 0, 1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	if diff := spaced.lines[0].width - plain.lines[0].width; math.Abs(diff-24) > 0.01 {
		t.Fatalf("spacing added %.3f, want 24", diff)
	}
	tall, err := l.text(context.Background(), "a\nb", textStyle{face: "IBM Plex Sans 600", size: 40, color: "#000000", align: "left", lineHeight: 150}, 0, 0, 1000, 200)
	if err != nil || tall.lineHeight != 60 {
		t.Fatalf("line height %.1f err %v", tall.lineHeight, err)
	}
}

func newTestPDF(d slides.Deck) (*gopdfDoc, error) {
	pdf := newPDF()
	for _, key := range deckFaces(d) {
		data, err := faceData(key)
		if err != nil {
			return nil, err
		}
		if err := pdf.AddTTFFontData(key, data); err != nil {
			return nil, err
		}
	}
	return pdf, nil
}

func TestGradientBandsFollowCSSGeometry(t *testing.T) {
	g := &slides.Gradient{Angle: 90, Stops: []slides.GradientStop{{Color: "#000000", Position: 0}, {Color: "#FFFFFF", Position: 100}}}
	b := bands(g, 0, 0, 256, 100)
	if len(b) != gradientBands {
		t.Fatal(len(b))
	}
	// 90deg runs left to right; the first strip is near black, the last near white.
	if b[0].rgb[0] > 2 || b[len(b)-1].rgb[0] < 253 || b[0].points[0].X > b[len(b)-1].points[0].X {
		t.Fatalf("first %v last %v", b[0].rgb, b[len(b)-1].rgb)
	}
	g.Stops[1].Position = 50
	if c := gradientColor(g, 0.75); c[0] != 255 {
		t.Fatalf("after the last stop the colour holds: %v", c)
	}
	if _, _, length := gradientGeometry(160, 720, 1080); math.Abs(length-(720*math.Sin(160*math.Pi/180)+1080*math.Abs(math.Cos(160*math.Pi/180)))) > 1e-9 {
		t.Fatal("gradient line length differs from CSS")
	}
}

func TestEveryIconFlattensInsideItsGrid(t *testing.T) {
	for _, name := range slides.IconNames() {
		lines, err := iconPolylines(name)
		if err != nil || len(lines) == 0 {
			t.Errorf("%s: %v", name, err)
			continue
		}
		for _, l := range lines {
			for _, p := range l.points {
				if p.X < -0.5 || p.Y < -0.5 || p.X > 24.5 || p.Y > 24.5 || math.IsNaN(p.X) || math.IsNaN(p.Y) {
					t.Errorf("%s: point %v leaves the 24-unit grid", name, p)
				}
			}
		}
	}
	// The users icon's head is a circle of radius 4 at (9,7).
	lines, _ := iconPolylines("users")
	head := lines[len(lines)-1]
	if !head.closed || math.Abs(head.points[0].X-13) > 1e-9 || math.Abs(head.points[0].Y-7) > 1e-9 {
		t.Fatalf("users head %v", head.points[:1])
	}
}

// The icon budget counts what drawIcon emits: a line and a disc per vertex.
func TestIconPrimitiveBudget(t *testing.T) {
	el := slides.Element{X: 0, Y: 0, Width: 280, Height: 280, Icon: &slides.Icon{Name: "users", Color: "#000000"}}
	p, err := planIcon(el)
	if err != nil {
		t.Fatal(err)
	}
	points := 0
	for _, l := range p.lines {
		points += len(l.points)
	}
	if p.primitives != 18*points {
		t.Fatalf("primitives %d for %d vertices", p.primitives, points)
	}
	again, _ := iconPolylines("users")
	first, _ := iconPolylines("users")
	if &again[0] != &first[0] {
		t.Error("icon geometry is flattened again for every element")
	}
}

func TestFlattenPathRefusesMalformedData(t *testing.T) {
	for _, d := range []string{"L1 1", "M1", "M1 1 a1 1 0 2 0 2 2", "M1 1 Q", "M1e5 1", "M1 1 X2 2"} {
		if _, err := flattenPath(d); err == nil && !strings.HasPrefix(d, "M1e5") {
			t.Errorf("%q: admitted", d)
		}
	}
	got, err := flattenPath("M0 0h10v10H0z")
	if err != nil || len(got) != 1 || !got[0].closed || len(got[0].points) != 5 {
		t.Fatalf("%v %v", got, err)
	}
}

type gopdfDoc = gopdf.GoPdf

func newPDF() *gopdf.GoPdf {
	pdf := &gopdf.GoPdf{}
	pdf.Start(gopdf.Config{Unit: gopdf.UnitPT, PageSize: gopdf.Rect{W: 1920, H: 1080}})
	return pdf
}
