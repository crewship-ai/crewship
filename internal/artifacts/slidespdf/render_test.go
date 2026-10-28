package slidespdf

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/phpdave11/gofpdi"
	"github.com/signintech/gopdf"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"

	"github.com/crewship-ai/crewship/internal/slides"
)

func fixture() slides.Deck {
	text := func(id, value string) slides.Element {
		return slides.Element{ID: id, Type: "text", X: 40, Y: 40, Width: 1800, Height: 100, Text: &slides.Text{Value: value, FontFamily: "Go Regular", FontSize: 32, Color: "#111111", Align: "left"}}
	}
	return slides.Deck{SchemaVersion: 1, DeckID: "pdf-deck", Width: 1920, Height: 1080, Theme: slides.Theme{Background: "#FFFFFF", Color: "#111111", FontFamily: "Go Regular", FontSize: 32}, SlideOrder: []string{"second", "first"}, Slides: []slides.Slide{
		{ID: "first", Elements: []slides.Element{text("title-first", "Druhá stránka")}},
		{ID: "second", Elements: []slides.Element{text("title-second", "Příliš žluťoučký kůň úpěl ďábelské ódy"), {ID: "rect", Type: "shape", X: 40, Y: 200, Width: 50, Height: 50, Shape: &slides.Shape{Kind: "rectangle", Fill: "#FF0000", Stroke: "#000000", StrokeWidth: 16}}, {ID: "ellipse", Type: "shape", X: 600, Y: 200, Width: 500, Height: 100, Shape: &slides.Shape{Kind: "ellipse", Fill: "#00FF00", Stroke: "#000000", StrokeWidth: 2}}, {ID: "table", Type: "table", X: 40, Y: 400, Width: 1000, Height: 200, Table: &slides.Table{Cells: [][]string{{"Tržby", "Praha"}, {"42", "Brno"}}, FontFamily: "Go Regular", FontSize: 24, Color: "#111111", Fill: "#FFFFFF", Border: "#000000"}}}},
	}}
}

func TestRenderReadablePDF(t *testing.T) {
	d := fixture()
	d.Slides[1].Elements = append(d.Slides[1].Elements, slides.Element{ID: "wrap", Type: "text", X: 1200, Y: 400, Width: 65, Height: 100, Text: &slides.Text{Value: "One two", FontFamily: fontFamily, FontSize: 20, Color: "#111111", Align: "left"}})
	data, err := Render(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	var stream io.ReadSeeker = bytes.NewReader(data)
	reader := gofpdi.NewImporter()
	reader.SetSourceStream(&stream)
	if reader.GetNumPages() != 2 {
		t.Fatalf("pages=%d", reader.GetNumPages())
	}
	for page, boxes := range reader.GetPageSizes() {
		box := boxes["/MediaBox"]
		if box["w"] != 1920 || box["h"] != 1080 {
			t.Fatalf("page %d: %#v", page, box)
		}
	}
	// This optional semantic check uses the repository's existing PDF.js dependency.
	// Go-only environments still execute the independent structural reader above.
	if os.Getenv("CREWSHIP_SLIDES_PDF_PDFJS") == "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "node", "testdata/inspect-pdf.mjs")
		cmd.Stdin = bytes.NewReader(data)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("PDF.js: %v\n%s", err, output)
		}
	}
}

func TestRenderRejectsUnsupportedAndOverflow(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*slides.Deck)
		want   error
	}{
		{"font", func(d *slides.Deck) { d.Theme.FontFamily = "Arial" }, ErrUnsupported},
		{"schema", func(d *slides.Deck) { d.SchemaVersion = 2 }, ErrUnsupported},
		{"degenerate stroke", func(d *slides.Deck) {
			d.Slides[1].Elements[1].Width = 4
			d.Slides[1].Elements[1].Shape.StrokeWidth = 16
		}, ErrUnsupported},
		{"slide cap", func(d *slides.Deck) { d.Slides = make([]slides.Slide, 101) }, ErrLimit},
		{"glyph", func(d *slides.Deck) { d.Slides[0].Elements[0].Text.Value = "🙂" }, ErrUnsupported},
		{"text overflow", func(d *slides.Deck) { d.Slides[0].Elements[0].Height = 1 }, ErrOverflow},
		{"cell overflow", func(d *slides.Deck) { d.Slides[1].Elements[3].Height = 1 }, ErrOverflow},
		{"per-slide cap", func(d *slides.Deck) { d.Slides[0].Elements = make([]slides.Element, 201) }, ErrLimit},
		{"glyph cap", func(d *slides.Deck) {
			for i := 0; i < 7; i++ {
				el := d.Slides[0].Elements[0]
				el.ID = strings.Repeat("x", i+1)
				el.Text = &slides.Text{Value: strings.Repeat("a", 32000), FontFamily: "Go Regular", FontSize: 1, Color: "#111111", Align: "left"}
				d.Slides[0].Elements = append(d.Slides[0].Elements, el)
			}
		}, ErrLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := fixture()
			tt.mutate(&d)
			out, err := Render(context.Background(), d)
			if !errors.Is(err, tt.want) || out != nil {
				t.Fatalf("bytes=%d err=%v want=%v", len(out), err, tt.want)
			}
		})
	}
}

func TestRenderCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := Render(ctx, fixture())
	if !errors.Is(err, context.Canceled) || out != nil {
		t.Fatalf("bytes=%d err=%v", len(out), err)
	}
}

func TestRenderWaitingCancellation(t *testing.T) {
	renderSlot <- struct{}{}
	defer func() { <-renderSlot }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	out, err := Render(ctx, fixture())
	if !errors.Is(err, context.DeadlineExceeded) || out != nil {
		t.Fatalf("bytes=%d err=%v", len(out), err)
	}
}

func TestRenderOutputCapDiscardsPartialPDF(t *testing.T) {
	out, err := render(context.Background(), fixture(), 100)
	if !errors.Is(err, ErrLimit) || out != nil {
		t.Fatalf("bytes=%d err=%v", len(out), err)
	}
}

func TestLayoutPreservesGlyphInkAtFrameEdges(t *testing.T) {
	pdf := &gopdf.GoPdf{}
	pdf.Start(gopdf.Config{PageSize: gopdf.Rect{W: 1920, H: 1080}})
	if err := pdf.AddTTFFontData(fontFamily, goregular.TTF); err != nil {
		t.Fatal(err)
	}
	l := layout{pdf: pdf, widths: make(map[int]map[rune]float64)}
	p, err := l.text(context.Background(), "j", 160, "#111111", "left", 0, 0, 100, 300)
	if err != nil {
		t.Fatal(err)
	}
	f, err := sfnt.Parse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	var b sfnt.Buffer
	g, err := f.GlyphIndex(&b, 'j')
	if err != nil {
		t.Fatal(err)
	}
	bounds, _, err := f.GlyphBounds(&b, g, fixed.I(160), font.HintingNone)
	if err != nil {
		t.Fatal(err)
	}
	if bounds.Min.X >= 0 {
		t.Fatal("fixture must have a negative glyph bearing")
	}
	if p.x+p.lines[0].offsetX+float64(bounds.Min.X)/64 < 0 {
		t.Fatal("the glyph ink would be silently clipped at the left frame")
	}
}
