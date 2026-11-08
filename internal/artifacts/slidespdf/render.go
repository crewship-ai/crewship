// Package slidespdf renders a fixed, offline PDF profile of the existing Slides model.
// It accepts no fonts, files, URLs, scripts or PDF imports from a caller.
package slidespdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/signintech/gopdf"
	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"

	"github.com/crewship-ai/crewship/internal/slides"
)

const (
	MaxOutputBytes   = 8 << 20
	MaxVisibleGlyphs = 200000
	MaxLines         = 20000
	MaxPrimitives    = 100000
	fontFamily       = "Go Regular"
)

var (
	ErrInvalidDeck = errors.New("invalid Slides PDF deck")
	ErrUnsupported = errors.New("unsupported Slides PDF profile")
	ErrOverflow    = errors.New("Slides PDF text overflows its frame")
	ErrLimit       = errors.New("Slides PDF resource limit")
	renderSlot     = make(chan struct{}, 1)
)

// Error identifies the slide and element that could not be represented faithfully.
// Kind supports errors.Is; unsuccessful renders never return partial PDF bytes.
type Error struct {
	Kind                       error
	SlideID, ElementID, Reason string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%v (slide %q, element %q): %s", e.Kind, e.SlideID, e.ElementID, e.Reason)
}
func (e *Error) Unwrap() error { return e.Kind }

type line struct {
	text           string
	width, offsetX float64
}
type textPlan struct {
	x, y, w, h           float64
	size                 int
	face                 string
	spacing              float64
	color, align         string
	lines                []line
	ascender, lineHeight float64
	inkBottom            float64
}
type elementPlan struct {
	element     slides.Element
	icon        iconPlan
	texts       []textPlan
	rows        []float64
	tableHeight float64
}
type pagePlan struct{ elements []elementPlan }
type layout struct {
	pdf               *gopdf.GoPdf
	lines, primitives int
	buffer            sfnt.Buffer
	faces             map[string]*faceMetrics
}

// faceMetrics caches one registered face's parsed font, advances and ink.
type faceMetrics struct {
	font   *sfnt.Font
	widths map[int]map[rune]float64
	ink    map[int]map[rune]glyphInk
}

// textStyle is one text frame's typography. Zero spacing and lineHeight 120
// are the schema-1 profile.
type textStyle struct {
	face         string
	size         int
	color, align string
	spacing      float64
	lineHeight   int
}
type glyphInk struct{ minX, minY, maxX, maxY float64 }

// Render returns a complete PDF or an error and no bytes. A cancellable single
// render slot bounds concurrent font and PDF compilation allocations. The input
// must remain unchanged until Render returns.
//
// The profile is schema 1, 1920×1080, Go Regular, and text/rectangle/ellipse/table
// elements. Text wraps words first, with glyph fallback for overlong words and
// no discarded whitespace, using 1.2×font-size line spacing. Tables use equal
// column widths, natural row heights and four-unit cell padding.
//
// Schema 3 adds the embedded IBM Plex Sans and JetBrains Mono faces, font
// weight, letter spacing (added after every glyph, as CSS does), line height,
// rounded rectangles, linear gradients (128 strips, exact colour per strip),
// lines with triangle arrowheads and allow-listed outline icons. Transitions
// do not apply to a PDF.
// Unsupported glyphs or shaping, frame overflow and resource limits are errors.
func Render(ctx context.Context, deck slides.Deck) ([]byte, error) {
	return render(ctx, deck, MaxOutputBytes)
}

func render(ctx context.Context, deck slides.Deck, outputLimit int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case renderSlot <- struct{}{}:
		defer func() { <-renderSlot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := admit(ctx, deck); err != nil {
		return nil, err
	}
	pdf := &gopdf.GoPdf{}
	pdf.Start(gopdf.Config{Unit: gopdf.UnitPT, PageSize: gopdf.Rect{W: 1920, H: 1080}})
	for _, key := range deckFaces(deck) {
		data, err := faceData(key)
		if err != nil {
			return nil, err
		}
		if err := pdf.AddTTFFontData(key, data); err != nil {
			return nil, fmt.Errorf("load fixed PDF font: %w", err)
		}
	}
	l := layout{pdf: pdf, faces: map[string]*faceMetrics{}}
	pages := make([]pagePlan, 0, len(deck.SlideOrder))
	byID := make(map[string]slides.Slide, len(deck.Slides))
	for _, s := range deck.Slides {
		byID[s.ID] = s
	}
	for _, id := range deck.SlideOrder {
		s := byID[id]
		page := pagePlan{}
		for _, el := range s.Elements {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			p, err := l.element(ctx, el)
			if err != nil {
				return nil, &Error{Kind: err, SlideID: id, ElementID: el.ID, Reason: "element cannot fit the fixed PDF profile"}
			}
			page.elements = append(page.elements, p)
		}
		pages = append(pages, page)
	}
	// No drawing is performed until every text frame, glyph and resource budget
	// has been checked, including tables on later pages.
	for _, page := range pages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pdf.AddPage()
		setColor(pdf.SetFillColor, deck.Theme.Background)
		pdf.RectFromUpperLeftWithStyle(0, 0, 1920, 1080, "F")
		for _, p := range page.elements {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			el := p.element
			clip(pdf, float64(el.X), float64(el.Y), float64(el.Width), float64(el.Height))
			switch el.Type {
			case "icon":
				drawIcon(pdf, p.icon, el.Icon.Color)
			case "shape":
				sh := el.Shape
				if sh.Kind == "line" {
					drawLine(pdf, el)
					break
				}
				if sh.CornerRadius != 0 || sh.Gradient != nil {
					drawShape(pdf, el)
					break
				}
				setColor(pdf.SetFillColor, sh.Fill)
				setColor(pdf.SetStrokeColor, sh.Stroke)
				pdf.SetLineWidth(float64(sh.StrokeWidth))
				style := "F"
				if sh.StrokeWidth > 0 {
					style = "DF"
				}
				inset := float64(sh.StrokeWidth) / 2
				x, y, w, h := float64(el.X)+inset, float64(el.Y)+inset, float64(el.Width)-2*inset, float64(el.Height)-2*inset
				if sh.Kind == "rectangle" {
					pdf.RectFromUpperLeftWithStyle(x, y, w, h, style)
				} else {
					// A fixed 128-vertex closed vector approximates the ellipse to <0.3
					// canvas units at this profile's largest permitted radius.
					points := make([]gopdf.Point, 128)
					for i := range points {
						a := float64(i) * 2 * math.Pi / 128
						points[i] = gopdf.Point{X: x + w*(1+math.Cos(a))/2, Y: y + h*(1+math.Sin(a))/2}
					}
					pdf.Polygon(points, style)
				}
			case "table":
				rows, cols := len(el.Table.Cells), len(el.Table.Cells[0])
				w := float64(el.Width) / float64(cols)
				setColor(pdf.SetFillColor, el.Table.Fill)
				pdf.RectFromUpperLeftWithStyle(float64(el.X), float64(el.Y), float64(el.Width), p.tableHeight, "F")
				setColor(pdf.SetStrokeColor, el.Table.Border)
				pdf.SetLineWidth(1)
				pdf.RectFromUpperLeftWithStyle(float64(el.X)+.5, float64(el.Y)+.5, float64(el.Width)-1, p.tableHeight-1, "D")
				y := float64(el.Y)
				for r := 0; r < rows-1; r++ {
					y += p.rows[r]
					pdf.Line(float64(el.X)+.5, y, float64(el.X+el.Width)-.5, y)
				}
				for c := 1; c < cols; c++ {
					x := float64(el.X) + float64(c)*w
					pdf.Line(x, float64(el.Y)+.5, x, float64(el.Y)+p.tableHeight-.5)
				}
			}
			for _, text := range p.texts {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if el.Type == "table" {
					clip(pdf, text.x-4, text.y-4, text.w+8, text.h+8)
				}
				if err := pdf.SetFont(text.face, "", text.size); err != nil {
					return nil, err
				}
				if text.spacing != 0 {
					if err := pdf.SetCharSpacing(text.spacing); err != nil {
						return nil, err
					}
				}
				setColor(pdf.SetTextColor, text.color)
				for i, ln := range text.lines {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					x := text.x + ln.offsetX
					if text.align == "center" {
						x += (text.w - ln.width) / 2
					} else if text.align == "right" {
						x += text.w - ln.width
					}
					pdf.SetXY(x, text.y+text.ascender+float64(i)*text.lineHeight)
					if err := pdf.Text(ln.text); err != nil {
						return nil, err
					}
				}
				if text.spacing != 0 {
					if err := pdf.SetCharSpacing(0); err != nil {
						return nil, err
					}
				}
				if el.Type == "table" {
					pdf.RestoreGraphicsState()
				}
			}
			pdf.RestoreGraphicsState()
		}
	}
	out := boundedWriter{ctx: ctx, limit: outputLimit}
	_, err := pdf.WriteTo(&out)
	// gopdf's compile loop does not propagate every writer error. The sticky
	// writer result is authoritative even if WriteTo incorrectly reports nil.
	if out.err != nil {
		return nil, out.err
	}
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return out.buf.Bytes(), nil
}

func admit(ctx context.Context, d slides.Deck) error {
	if d.SchemaVersion != slides.SchemaVersion && d.SchemaVersion != slides.RichSchemaVersion || d.Width != 1920 || d.Height != 1080 || d.Theme.FontFamily != fontFamily {
		return ErrUnsupported
	}
	if len(d.Slides) > 100 || len(d.SlideOrder) > 100 {
		return ErrLimit
	}
	total, glyphs, textBytes, cells := 0, 0, 0, 0
	for _, s := range d.Slides {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(s.Elements) > 200 {
			return ErrLimit
		}
		total += len(s.Elements)
		if total > 1000 {
			return ErrLimit
		}
		if len(s.Notes) > slides.MaxNotesBytes {
			return ErrLimit
		}
		for _, el := range s.Elements {
			var values []string
			switch el.Type {
			case "text":
				if el.Text != nil {
					if el.Text.FontFamily != fontFamily && !slides.RichFontAllowed(el.Text.FontFamily, el.Text.FontWeight) {
						return ErrUnsupported
					}
					values = []string{el.Text.Value}
				}
			case "shape":
				if el.Shape != nil && el.Shape.Kind != "line" && (el.Shape.StrokeWidth >= el.Width || el.Shape.StrokeWidth >= el.Height) {
					return ErrUnsupported
				}
			case "icon":
			case "table":
				if el.Table != nil {
					if el.Table.FontFamily != fontFamily && !slides.RichFontAllowed(el.Table.FontFamily, 0) {
						return ErrUnsupported
					}
					if len(el.Table.Cells) > 30 {
						return ErrLimit
					}
					for _, row := range el.Table.Cells {
						if len(row) > 20 {
							return ErrLimit
						}
						cells += len(row)
						if cells > 10000 {
							return ErrLimit
						}
						values = append(values, row...)
					}
				}
			default:
				return ErrUnsupported
			}
			for _, v := range values {
				textBytes += len(v)
				if textBytes > slides.MaxDeckBytes {
					return ErrLimit
				}
				glyphs += utf8.RuneCountInString(v)
				if glyphs > MaxVisibleGlyphs {
					return ErrLimit
				}
			}
		}
	}
	if err := slides.ValidateProjection(d); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDeck, err)
	}
	return nil
}

func (l *layout) element(ctx context.Context, el slides.Element) (elementPlan, error) {
	p := elementPlan{element: el}
	l.primitives += 5
	switch el.Type {
	case "shape":
		l.primitives += shapePrimitives(el.Shape)
	case "icon":
		plan, err := planIcon(el)
		if err != nil {
			return p, err
		}
		p.icon = plan
		l.primitives += plan.primitives
	case "text":
		t := el.Text
		plan, err := l.text(ctx, t.Value, textStyle{face: faceKey(t.FontFamily, t.FontWeight), size: t.FontSize, color: t.Color, align: t.Align, spacing: float64(t.LetterSpacing), lineHeight: t.LineHeight}, float64(el.X), float64(el.Y), float64(el.Width), float64(el.Height))
		if err != nil {
			return p, err
		}
		p.texts = append(p.texts, plan)
	case "table":
		t := el.Table
		w := float64(el.Width) / float64(len(t.Cells[0]))
		y := float64(el.Y)
		for _, row := range t.Cells {
			start := len(p.texts)
			rowHeight := 0.0
			for c, value := range row {
				l.primitives += 10
				plan, err := l.text(ctx, value, textStyle{face: faceKey(t.FontFamily, 0), size: t.FontSize, color: t.Color, align: "left"}, float64(el.X)+float64(c)*w+4, y+4, w-8, math.Inf(1))
				if err != nil {
					return p, err
				}
				p.texts = append(p.texts, plan)
				rowHeight = math.Max(rowHeight, float64(len(plan.lines))*plan.lineHeight+8)
			}
			for i := start; i < len(p.texts); i++ {
				p.texts[i].h = rowHeight - 8
				if p.texts[i].inkBottom > rowHeight-8 {
					return p, ErrOverflow
				}
			}
			p.rows = append(p.rows, rowHeight)
			p.tableHeight += rowHeight
			y += rowHeight
			if p.tableHeight > float64(el.Height) {
				return p, ErrOverflow
			}
		}
	}
	if l.primitives > MaxPrimitives {
		return p, ErrLimit
	}
	return p, nil
}

func (l *layout) text(ctx context.Context, value string, style textStyle, x, y, w, h float64) (textPlan, error) {
	size := style.size
	lineHeight := float64(size) * 1.2
	if style.lineHeight != 0 {
		lineHeight = float64(size) * float64(style.lineHeight) / 100
	}
	p := textPlan{x: x, y: y, w: w, h: h, size: size, face: style.face, spacing: style.spacing, color: style.color, align: style.align, lineHeight: lineHeight}
	if w <= 0 || h <= 0 {
		return p, ErrOverflow
	}
	if err := l.pdf.SetFont(style.face, "", size); err != nil {
		return p, err
	}
	fm := l.faces[style.face]
	if fm == nil {
		data, err := faceData(style.face)
		if err != nil {
			return p, err
		}
		f, err := sfnt.Parse(data)
		if err != nil {
			return p, err
		}
		fm = &faceMetrics{font: f, widths: make(map[int]map[rune]float64), ink: make(map[int]map[rune]glyphInk)}
		l.faces[style.face] = fm
	}
	metrics, err := fm.font.Metrics(&l.buffer, fixed.I(size), font.HintingNone)
	if err != nil {
		return p, err
	}
	ascent, descent := float64(metrics.Ascent)/64, float64(metrics.Descent)/64
	p.ascender = ascent + (p.lineHeight-ascent-descent)/2
	cache := fm.widths[size]
	if cache == nil {
		cache = make(map[rune]float64)
		fm.widths[size] = cache
	}
	inks := fm.ink[size]
	if inks == nil {
		inks = make(map[rune]glyphInk)
		fm.ink[size] = inks
	}
	type glyph struct {
		r       rune
		advance float64
		ink     glyphInk
	}
	glyphs := make([]glyph, 0, utf8.RuneCountInString(value))
	minY, maxY := 0.0, 0.0
	for _, r := range value {
		if err := ctx.Err(); err != nil {
			return p, err
		}
		if r == '\n' {
			glyphs = append(glyphs, glyph{r: r})
			continue
		}
		// Go Regular has no shaping engine. Unsupported sequences must not be
		// silently substituted, reordered or clipped by the fixed-font profile.
		if unicode.IsControl(r) || unicode.Is(unicode.M, r) || unicode.Is(unicode.Cf, r) {
			return p, ErrUnsupported
		}
		advance, ok := cache[r]
		if !ok {
			has, err := l.pdf.IsCurrFontContainGlyph(r)
			if err != nil {
				return p, err
			}
			if !has {
				return p, ErrUnsupported
			}
			advance, err = l.pdf.MeasureTextWidth(string(r))
			if err != nil {
				return p, err
			}
			cache[r] = advance
			index, err := fm.font.GlyphIndex(&l.buffer, r)
			if err != nil {
				return p, err
			}
			bounds, _, err := fm.font.GlyphBounds(&l.buffer, index, fixed.I(size), font.HintingNone)
			if err != nil {
				return p, err
			}
			inks[r] = glyphInk{minX: float64(bounds.Min.X) / 64, minY: float64(bounds.Min.Y) / 64, maxX: float64(bounds.Max.X) / 64, maxY: float64(bounds.Max.Y) / 64}
		}
		ink := inks[r]
		minY = math.Min(minY, ink.minY)
		maxY = math.Max(maxY, ink.maxY)
		// CSS letter-spacing follows every glyph, the last one of a line included.
		glyphs = append(glyphs, glyph{r: r, advance: advance + style.spacing, ink: ink})
	}
	var b strings.Builder
	width, minX, maxX := 0.0, 0.0, 0.0
	flush := func() error {
		l.lines++
		l.primitives++
		if l.lines > MaxLines || l.primitives > MaxPrimitives {
			return ErrLimit
		}
		p.lines = append(p.lines, line{text: b.String(), width: math.Max(width, maxX) - math.Min(0, minX), offsetX: -math.Min(0, minX)})
		b.Reset()
		width = 0
		minX = 0
		maxX = 0
		return nil
	}
	span := func(advance, left, right float64) float64 {
		return math.Max(width+advance, math.Max(maxX, width+right)) - math.Min(0, math.Min(minX, width+left))
	}
	// Words are admitted as units first. Only an overlong word falls back to
	// glyph boundaries. Each glyph is visited a bounded number of times; no
	// substring measurement or repeated prefix rebuilding is used.
	for start := 0; start < len(glyphs); {
		if err := ctx.Err(); err != nil {
			return p, err
		}
		if glyphs[start].r == '\n' {
			if err := flush(); err != nil {
				return p, err
			}
			start++
			continue
		}
		end := start
		advance, left, right := 0.0, 0.0, 0.0
		for end < len(glyphs) && glyphs[end].r != '\n' {
			g := glyphs[end]
			left = math.Min(left, advance+g.ink.minX)
			right = math.Max(right, advance+g.ink.maxX)
			advance += g.advance
			end++
			if g.r == ' ' || g.r == '-' {
				break
			}
		}
		if span(advance, left, right) > w && b.Len() > 0 {
			if err := flush(); err != nil {
				return p, err
			}
		}
		for _, g := range glyphs[start:end] {
			if span(g.advance, g.ink.minX, g.ink.maxX) > w && b.Len() > 0 {
				if err := flush(); err != nil {
					return p, err
				}
			}
			if span(g.advance, g.ink.minX, g.ink.maxX) > w {
				return p, ErrOverflow
			}
			minX = math.Min(minX, width+g.ink.minX)
			maxX = math.Max(maxX, width+g.ink.maxX)
			width += g.advance
			b.WriteRune(g.r)
		}
		start = end
	}
	if err := flush(); err != nil {
		return p, err
	}
	p.inkBottom = p.ascender + float64(len(p.lines)-1)*p.lineHeight + maxY
	if p.ascender+minY < 0 || float64(len(p.lines))*p.lineHeight > h || p.ascender+float64(len(p.lines)-1)*p.lineHeight+maxY > h {
		return p, ErrOverflow
	}
	return p, nil
}

func clip(pdf *gopdf.GoPdf, x, y, w, h float64) {
	pdf.SaveGraphicsState()
	pdf.ClipPolygon([]gopdf.Point{{X: x, Y: y}, {X: x + w, Y: y}, {X: x + w, Y: y + h}, {X: x, Y: y + h}})
}
func setColor(set func(uint8, uint8, uint8), color string) {
	n, _ := strconv.ParseUint(color[1:], 16, 24)
	set(uint8(n>>16), uint8(n>>8), uint8(n))
}

type boundedWriter struct {
	ctx   context.Context
	limit int
	buf   bytes.Buffer
	err   error
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if err := w.ctx.Err(); err != nil {
		w.err = err
		return 0, err
	}
	if len(p) > w.limit-w.buf.Len() {
		w.err = ErrLimit
		return 0, w.err
	}
	return w.buf.Write(p)
}
