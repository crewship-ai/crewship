package slidespdf

import (
	"embed"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/signintech/gopdf"
	"golang.org/x/image/font/gofont/goregular"

	"github.com/crewship-ai/crewship/internal/slides"
)

// The schema-3 faces are fixed, embedded OFL files (see licenses/). A deck can
// name only these; nothing is loaded from a caller, a URL or the host.
//
//go:embed fonts/*.ttf
var faceFiles embed.FS

var faceFile = map[string]string{
	"IBM Plex Sans 400":  "fonts/IBMPlexSans-Regular.ttf",
	"IBM Plex Sans 600":  "fonts/IBMPlexSans-SemiBold.ttf",
	"IBM Plex Sans 700":  "fonts/IBMPlexSans-Bold.ttf",
	"JetBrains Mono 400": "fonts/JetBrainsMono-Regular.ttf",
	"JetBrains Mono 600": "fonts/JetBrainsMono-SemiBold.ttf",
}

// faceKey names the registered PDF font. Go Regular keeps its schema-1 key so
// schema-1 output is byte-identical.
func faceKey(family string, weight int) string {
	if family == fontFamily {
		return fontFamily
	}
	if weight == 0 {
		weight = 400
	}
	return family + " " + strconv.Itoa(weight)
}

func faceData(key string) ([]byte, error) {
	if key == fontFamily {
		return goregular.TTF, nil
	}
	name, ok := faceFile[key]
	if !ok {
		return nil, ErrUnsupported
	}
	return faceFiles.ReadFile(name)
}

// deckFaces lists the faces a deck uses: Go Regular first (the theme face),
// then the rest in sorted order, so registration order is deterministic.
func deckFaces(d slides.Deck) []string {
	seen := map[string]bool{fontFamily: true}
	for _, s := range d.Slides {
		for _, el := range s.Elements {
			switch {
			case el.Text != nil:
				seen[faceKey(el.Text.FontFamily, el.Text.FontWeight)] = true
			case el.Table != nil:
				seen[faceKey(el.Table.FontFamily, 0)] = true
			}
		}
	}
	out := []string{}
	for key := range seen {
		if key != fontFamily {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return append([]string{fontFamily}, out...)
}

type point = gopdf.Point

// roundedRect is a closed outline with 16 segments per corner.
func roundedRect(x, y, w, h, r float64) []point {
	if r <= 0 {
		return []point{{X: x, Y: y}, {X: x + w, Y: y}, {X: x + w, Y: y + h}, {X: x, Y: y + h}}
	}
	r = math.Min(r, math.Min(w, h)/2)
	corners := []struct{ cx, cy, start float64 }{{x + w - r, y + r, -math.Pi / 2}, {x + w - r, y + h - r, 0}, {x + r, y + h - r, math.Pi / 2}, {x + r, y + r, math.Pi}}
	points := make([]point, 0, 68)
	for _, c := range corners {
		for i := 0; i <= 16; i++ {
			a := c.start + float64(i)*math.Pi/32
			points = append(points, point{X: c.cx + r*math.Cos(a), Y: c.cy + r*math.Sin(a)})
		}
	}
	return points
}

func ellipsePoints(x, y, w, h float64) []point {
	// A fixed 128-vertex closed vector approximates the ellipse to <0.3
	// canvas units at this profile's largest permitted radius.
	points := make([]point, 128)
	for i := range points {
		a := float64(i) * 2 * math.Pi / 128
		points[i] = point{X: x + w*(1+math.Cos(a))/2, Y: y + h*(1+math.Sin(a))/2}
	}
	return points
}

const gradientBands = 128

type band struct {
	points []point
	rgb    [3]float64
}

// gradientGeometry follows CSS linear-gradient over the element box: angle 0
// points up and turns clockwise; the gradient line passes through the centre
// and is long enough that the corners take the first and last colours.
func gradientGeometry(angle int, w, h float64) (dx, dy, length float64) {
	a := float64(angle) * math.Pi / 180
	dx, dy = math.Sin(a), -math.Cos(a)
	return dx, dy, math.Abs(w*dx) + math.Abs(h*dy)
}

func gradientColor(g *slides.Gradient, t float64) [3]float64 {
	rgb := func(hex string) [3]float64 {
		n, _ := strconv.ParseUint(hex[1:], 16, 24)
		return [3]float64{float64(n >> 16 & 255), float64(n >> 8 & 255), float64(n & 255)}
	}
	stops := g.Stops
	pos := func(i int) float64 { return float64(stops[i].Position) / 100 }
	if t <= pos(0) {
		return rgb(stops[0].Color)
	}
	for i := 1; i < len(stops); i++ {
		if t <= pos(i) {
			a, b := rgb(stops[i-1].Color), rgb(stops[i].Color)
			span := pos(i) - pos(i-1)
			f := 0.0
			if span > 0 {
				f = (t - pos(i-1)) / span
			}
			return [3]float64{a[0] + (b[0]-a[0])*f, a[1] + (b[1]-a[1])*f, a[2] + (b[2]-a[2])*f}
		}
	}
	return rgb(stops[len(stops)-1].Color)
}

// bands approximates the gradient with fixed-width strips perpendicular to the
// gradient line; each strip takes the exact colour at its centre. 128 strips
// keep each step below one 8-bit level for any two-stop span of up to 128
// levels per channel. Strips overlap by half a unit so no seam shows.
func bands(g *slides.Gradient, x, y, w, h float64) []band {
	dx, dy, length := gradientGeometry(g.Angle, w, h)
	cx, cy := x+w/2, y+h/2
	sx, sy := cx-dx*length/2, cy-dy*length/2
	nx, ny := -dy, dx
	reach := w + h
	out := make([]band, gradientBands)
	for i := range out {
		t0, t1 := float64(i)/gradientBands, float64(i+1)/gradientBands
		a0, a1 := t0*length-0.5, t1*length+0.5
		if i == 0 {
			a0 = -reach
		}
		if i == gradientBands-1 {
			a1 = length + reach
		}
		p0x, p0y := sx+dx*a0, sy+dy*a0
		p1x, p1y := sx+dx*a1, sy+dy*a1
		out[i] = band{points: []point{{X: p0x + nx*reach, Y: p0y + ny*reach}, {X: p1x + nx*reach, Y: p1y + ny*reach}, {X: p1x - nx*reach, Y: p1y - ny*reach}, {X: p0x - nx*reach, Y: p0y - ny*reach}}, rgb: gradientColor(g, (t0+t1)/2)}
	}
	return out
}

// Arrowhead returns the triangle for a line ending at (x2,y2) coming from
// (x1,y1), and the point where the shaft stops. The web renderer uses the same
// geometry: length 3w+4, half width 1.5w+2 for stroke width w.
func arrowhead(x1, y1, x2, y2, w float64) (tri []point, bx, by float64) {
	length, half := 3*w+4, 1.5*w+2
	dx, dy := x2-x1, y2-y1
	d := math.Hypot(dx, dy)
	ux, uy := dx/d, dy/d
	length = math.Min(length, d)
	bx, by = x2-ux*length, y2-uy*length
	return []point{{X: x2, Y: y2}, {X: bx - uy*half, Y: by + ux*half}, {X: bx + uy*half, Y: by - ux*half}}, bx, by
}

func drawLine(pdf *gopdf.GoPdf, el slides.Element) {
	sh, l := el.Shape, el.Shape.Line
	ox, oy, w := float64(el.X), float64(el.Y), float64(sh.StrokeWidth)
	x1, y1, x2, y2 := ox+float64(l.X1), oy+float64(l.Y1), ox+float64(l.X2), oy+float64(l.Y2)
	setColor(pdf.SetFillColor, sh.Stroke)
	setColor(pdf.SetStrokeColor, sh.Stroke)
	pdf.SetLineWidth(w)
	ax1, ay1, ax2, ay2 := x1, y1, x2, y2
	if l.End == "triangle" {
		tri, bx, by := arrowhead(x1, y1, x2, y2, w)
		pdf.Polygon(tri, "F")
		ax2, ay2 = bx, by
	}
	if l.Start == "triangle" {
		tri, bx, by := arrowhead(x2, y2, x1, y1, w)
		pdf.Polygon(tri, "F")
		ax1, ay1 = bx, by
	}
	if ax1 != ax2 || ay1 != ay2 {
		pdf.Line(ax1, ay1, ax2, ay2)
	}
}

// drawShape paints a schema-3 rectangle or ellipse (rounded corners and/or a
// gradient). Schema-1 shapes keep their original drawing calls in render.
func drawShape(pdf *gopdf.GoPdf, el slides.Element) {
	sh := el.Shape
	inset := float64(sh.StrokeWidth) / 2
	x, y, w, h := float64(el.X)+inset, float64(el.Y)+inset, float64(el.Width)-2*inset, float64(el.Height)-2*inset
	var outline []point
	if sh.Kind == "ellipse" {
		outline = ellipsePoints(x, y, w, h)
	} else {
		outline = roundedRect(x, y, w, h, math.Max(0, float64(sh.CornerRadius)-inset))
	}
	if sh.Gradient == nil {
		setColor(pdf.SetFillColor, sh.Fill)
		pdf.Polygon(outline, "F")
	} else {
		// The fill area is the outer edge of the stroke, as in the browser.
		var fillArea []point
		if sh.Kind == "ellipse" {
			fillArea = ellipsePoints(float64(el.X), float64(el.Y), float64(el.Width), float64(el.Height))
		} else {
			fillArea = roundedRect(float64(el.X), float64(el.Y), float64(el.Width), float64(el.Height), float64(sh.CornerRadius))
		}
		pdf.SaveGraphicsState()
		pdf.ClipPolygon(fillArea)
		for _, b := range bands(sh.Gradient, float64(el.X), float64(el.Y), float64(el.Width), float64(el.Height)) {
			pdf.SetFillColor(uint8(math.Round(b.rgb[0])), uint8(math.Round(b.rgb[1])), uint8(math.Round(b.rgb[2])))
			pdf.Polygon(b.points, "F")
		}
		pdf.RestoreGraphicsState()
	}
	if sh.StrokeWidth > 0 {
		setColor(pdf.SetStrokeColor, sh.Stroke)
		pdf.SetLineWidth(float64(sh.StrokeWidth))
		pdf.Polygon(outline, "D")
	}
}

func shapePrimitives(sh *slides.Shape) int {
	n := 128
	if sh.Gradient != nil {
		n += 2 * gradientBands
	}
	if sh.Kind == "line" {
		n = 3
	}
	return n
}

// ---- Icons --------------------------------------------------------------

type polyline struct {
	points []point
	closed bool
}

var errIconPath = errors.New("icon path")

var iconCache sync.Map // name -> []polyline; the set is fixed, so at most ~50 entries

// iconPolylines flattens the allow-listed icon's primitives on its 24-unit grid,
// once per name. Callers must not modify the returned points.
func iconPolylines(name string) ([]polyline, error) {
	if cached, ok := iconCache.Load(name); ok {
		return cached.([]polyline), nil
	}
	lines, err := flattenIcon(name)
	if err == nil {
		iconCache.Store(name, lines)
	}
	return lines, err
}

func flattenIcon(name string) ([]polyline, error) {
	nodes, ok := slides.IconNodes(name)
	if !ok {
		return nil, ErrUnsupported
	}
	var out []polyline
	num := func(attrs map[string]string, key string) (float64, error) {
		v, ok := attrs[key]
		if !ok {
			return 0, nil
		}
		return strconv.ParseFloat(v, 64)
	}
	for _, n := range nodes {
		a := n.Attrs
		switch n.Tag {
		case "path":
			p, err := flattenPath(a["d"])
			if err != nil {
				return nil, err
			}
			out = append(out, p...)
		case "circle", "ellipse":
			cx, e1 := num(a, "cx")
			cy, e2 := num(a, "cy")
			rx, e3 := num(a, "r")
			ry := rx
			var e4 error
			if n.Tag == "ellipse" {
				rx, e3 = num(a, "rx")
				ry, e4 = num(a, "ry")
			}
			if err := errors.Join(e1, e2, e3, e4); err != nil {
				return nil, err
			}
			pts := make([]point, 48)
			for i := range pts {
				t := float64(i) * 2 * math.Pi / 48
				pts[i] = point{X: cx + rx*math.Cos(t), Y: cy + ry*math.Sin(t)}
			}
			out = append(out, polyline{points: pts, closed: true})
		case "rect":
			x, e1 := num(a, "x")
			y, e2 := num(a, "y")
			w, e3 := num(a, "width")
			h, e4 := num(a, "height")
			r, e5 := num(a, "rx")
			if err := errors.Join(e1, e2, e3, e4, e5); err != nil {
				return nil, err
			}
			if r == 0 {
				r, _ = num(a, "ry")
			}
			out = append(out, polyline{points: roundedRect(x, y, w, h, r), closed: true})
		case "line":
			x1, e1 := num(a, "x1")
			y1, e2 := num(a, "y1")
			x2, e3 := num(a, "x2")
			y2, e4 := num(a, "y2")
			if err := errors.Join(e1, e2, e3, e4); err != nil {
				return nil, err
			}
			out = append(out, polyline{points: []point{{X: x1, Y: y1}, {X: x2, Y: y2}}})
		case "polyline", "polygon":
			fields := strings.FieldsFunc(a["points"], func(r rune) bool { return r == ' ' || r == ',' })
			if len(fields)%2 != 0 || len(fields) < 4 {
				return nil, errIconPath
			}
			pts := []point{}
			for i := 0; i < len(fields); i += 2 {
				x, e1 := strconv.ParseFloat(fields[i], 64)
				y, e2 := strconv.ParseFloat(fields[i+1], 64)
				if err := errors.Join(e1, e2); err != nil {
					return nil, err
				}
				pts = append(pts, point{X: x, Y: y})
			}
			out = append(out, polyline{points: pts, closed: n.Tag == "polygon"})
		default:
			return nil, errIconPath
		}
	}
	return out, nil
}

// flattenPath turns SVG path data (M L H V C S Q T A Z, absolute and relative)
// into polylines. It accepts only the committed icon set, never author input.
func flattenPath(d string) ([]polyline, error) {
	s := pathScanner{src: d}
	var out []polyline
	var cur []point
	var x, y, sx, sy, cx, cy float64 // current, subpath start, last control point
	var prev byte
	flush := func(closed bool) {
		if len(cur) > 1 {
			out = append(out, polyline{points: cur, closed: closed})
		}
		cur = nil
	}
	lineTo := func(nx, ny float64) {
		if len(cur) == 0 {
			cur = append(cur, point{X: x, Y: y})
		}
		cur = append(cur, point{X: nx, Y: ny})
		x, y = nx, ny
	}
	cubic := func(x1, y1, x2, y2, x3, y3 float64) {
		x0, y0 := x, y
		for i := 1; i <= 16; i++ {
			t := float64(i) / 16
			u := 1 - t
			lineTo(u*u*u*x0+3*u*u*t*x1+3*u*t*t*x2+t*t*t*x3, u*u*u*y0+3*u*u*t*y1+3*u*t*t*y2+t*t*t*y3)
		}
		cx, cy = x2, y2
	}
	quad := func(x1, y1, x2, y2 float64) {
		x0, y0 := x, y
		for i := 1; i <= 12; i++ {
			t := float64(i) / 12
			u := 1 - t
			lineTo(u*u*x0+2*u*t*x1+t*t*x2, u*u*y0+2*u*t*y1+t*t*y2)
		}
		cx, cy = x1, y1
	}
	var cmd byte
	for {
		s.skip()
		if s.done() {
			break
		}
		if c := s.src[s.i]; strings.IndexByte("MmLlHhVvCcSsQqTtAaZz", c) >= 0 {
			cmd = c
			s.i++
		} else if cmd == 0 {
			return nil, errIconPath
		}
		if prev == 0 && cmd|0x20 != 'm' {
			return nil, errIconPath
		}
		rel := cmd >= 'a'
		ox, oy := 0.0, 0.0
		if rel {
			ox, oy = x, y
		}
		n, err := s.numbers(map[byte]int{'m': 2, 'l': 2, 'h': 1, 'v': 1, 'c': 6, 's': 4, 'q': 4, 't': 2, 'a': 7, 'z': 0}[cmd|0x20], cmd|0x20 == 'a')
		if err != nil {
			return nil, err
		}
		switch cmd | 0x20 {
		case 'm':
			flush(false)
			x, y = ox+n[0], oy+n[1]
			sx, sy = x, y
			cmd = map[bool]byte{true: 'l', false: 'L'}[rel]
		case 'l':
			lineTo(ox+n[0], oy+n[1])
		case 'h':
			lineTo(ox+n[0], y)
		case 'v':
			lineTo(x, oy+n[0])
		case 'c':
			cubic(ox+n[0], oy+n[1], ox+n[2], oy+n[3], ox+n[4], oy+n[5])
		case 's':
			x1, y1 := x, y
			if prev|0x20 == 'c' || prev|0x20 == 's' {
				x1, y1 = 2*x-cx, 2*y-cy
			}
			cubic(x1, y1, ox+n[0], oy+n[1], ox+n[2], oy+n[3])
		case 'q':
			quad(ox+n[0], oy+n[1], ox+n[2], oy+n[3])
		case 't':
			x1, y1 := x, y
			if prev|0x20 == 'q' || prev|0x20 == 't' {
				x1, y1 = 2*x-cx, 2*y-cy
			}
			quad(x1, y1, ox+n[0], oy+n[1])
		case 'a':
			for _, p := range arcPoints(x, y, n[0], n[1], n[2], n[3] != 0, n[4] != 0, ox+n[5], oy+n[6]) {
				lineTo(p.X, p.Y)
			}
		case 'z':
			if len(cur) > 0 && (x != sx || y != sy) {
				lineTo(sx, sy)
			}
			flush(true)
			x, y = sx, sy
		}
		prev = cmd
		if cmd|0x20 != 'c' && cmd|0x20 != 's' && cmd|0x20 != 'q' && cmd|0x20 != 't' {
			cx, cy = x, y
		}
		if len(out) > 256 || len(cur) > 4096 {
			return nil, ErrLimit
		}
	}
	flush(false)
	return out, nil
}

type pathScanner struct {
	src string
	i   int
}

func (s *pathScanner) skip() {
	for s.i < len(s.src) && (s.src[s.i] == ' ' || s.src[s.i] == ',') {
		s.i++
	}
}
func (s *pathScanner) done() bool { return s.i >= len(s.src) }

// numbers reads count numbers; for arcs, values 4 and 5 are single-digit flags.
func (s *pathScanner) numbers(count int, arc bool) ([]float64, error) {
	out := make([]float64, count)
	for k := 0; k < count; k++ {
		s.skip()
		if s.done() {
			return nil, errIconPath
		}
		if arc && (k == 3 || k == 4) {
			if c := s.src[s.i]; c == '0' || c == '1' {
				out[k] = float64(c - '0')
				s.i++
				continue
			}
			return nil, errIconPath
		}
		start := s.i
		if s.src[s.i] == '-' || s.src[s.i] == '+' {
			s.i++
		}
		dot := false
		for s.i < len(s.src) && (s.src[s.i] >= '0' && s.src[s.i] <= '9' || s.src[s.i] == '.' && !dot) {
			dot = dot || s.src[s.i] == '.'
			s.i++
		}
		v, err := strconv.ParseFloat(s.src[start:s.i], 64)
		if err != nil {
			return nil, errIconPath
		}
		out[k] = v
	}
	return out, nil
}

// arcPoints implements SVG 1.1 F.6.5 (endpoint to centre parameterisation).
func arcPoints(x1, y1, rx, ry, phiDeg float64, large, sweep bool, x2, y2 float64) []point {
	if x1 == x2 && y1 == y2 {
		return nil
	}
	rx, ry = math.Abs(rx), math.Abs(ry)
	if rx == 0 || ry == 0 {
		return []point{{X: x2, Y: y2}}
	}
	phi := phiDeg * math.Pi / 180
	cos, sin := math.Cos(phi), math.Sin(phi)
	dx, dy := (x1-x2)/2, (y1-y2)/2
	x1p, y1p := cos*dx+sin*dy, -sin*dx+cos*dy
	if l := x1p*x1p/(rx*rx) + y1p*y1p/(ry*ry); l > 1 {
		rx, ry = rx*math.Sqrt(l), ry*math.Sqrt(l)
	}
	num := rx*rx*ry*ry - rx*rx*y1p*y1p - ry*ry*x1p*x1p
	den := rx*rx*y1p*y1p + ry*ry*x1p*x1p
	co := math.Sqrt(math.Max(0, num/den))
	if large == sweep {
		co = -co
	}
	cxp, cyp := co*rx*y1p/ry, -co*ry*x1p/rx
	cx, cy := cos*cxp-sin*cyp+(x1+x2)/2, sin*cxp+cos*cyp+(y1+y2)/2
	angle := func(ux, uy, vx, vy float64) float64 {
		return math.Atan2(ux*vy-uy*vx, ux*vx+uy*vy)
	}
	t1 := angle(1, 0, (x1p-cxp)/rx, (y1p-cyp)/ry)
	dt := angle((x1p-cxp)/rx, (y1p-cyp)/ry, (-x1p-cxp)/rx, (-y1p-cyp)/ry)
	if !sweep && dt > 0 {
		dt -= 2 * math.Pi
	} else if sweep && dt < 0 {
		dt += 2 * math.Pi
	}
	steps := int(math.Ceil(math.Abs(dt) / (math.Pi / 24)))
	if steps < 1 {
		steps = 1
	}
	out := make([]point, 0, steps)
	for i := 1; i <= steps; i++ {
		t := t1 + dt*float64(i)/float64(steps)
		px, py := rx*math.Cos(t), ry*math.Sin(t)
		out = append(out, point{X: cos*px - sin*py + cx, Y: sin*px + cos*py + cy})
	}
	out[len(out)-1] = point{X: x2, Y: y2}
	return out
}

// iconPlan places the 24-unit icon centred in the element box, scaled to fit
// (SVG preserveAspectRatio xMidYMid meet), with lucide's 2-unit round stroke.
type iconPlan struct {
	lines      []polyline
	stroke     float64
	primitives int
}

func planIcon(el slides.Element) (iconPlan, error) {
	lines, err := iconPolylines(el.Icon.Name)
	if err != nil {
		return iconPlan{}, err
	}
	s := math.Min(float64(el.Width), float64(el.Height)) / 24
	ox, oy := float64(el.X)+(float64(el.Width)-24*s)/2, float64(el.Y)+(float64(el.Height)-24*s)/2
	p := iconPlan{stroke: 2 * s}
	for _, l := range lines {
		pts := make([]point, len(l.points))
		for i, q := range l.points {
			pts[i] = point{X: ox + q.X*s, Y: oy + q.Y*s}
		}
		p.lines = append(p.lines, polyline{points: pts, closed: l.closed})
		// One line and one 16-point disc per vertex (drawIcon).
		p.primitives += 18 * len(pts)
	}
	return p, nil
}

// drawIcon strokes each segment with a butt line and paints a disc of the
// stroke width at every vertex: round caps and round joins, as in lucide.
func drawIcon(pdf *gopdf.GoPdf, p iconPlan, color string) {
	setColor(pdf.SetStrokeColor, color)
	setColor(pdf.SetFillColor, color)
	pdf.SetLineWidth(p.stroke)
	r := p.stroke / 2
	for _, l := range p.lines {
		n := len(l.points)
		for i := 0; i+1 < n; i++ {
			pdf.Line(l.points[i].X, l.points[i].Y, l.points[i+1].X, l.points[i+1].Y)
		}
		if l.closed && n > 2 {
			pdf.Line(l.points[n-1].X, l.points[n-1].Y, l.points[0].X, l.points[0].Y)
		}
		for _, q := range l.points {
			disc := make([]point, 16)
			for i := range disc {
				a := float64(i) * 2 * math.Pi / 16
				disc[i] = point{X: q.X + r*math.Cos(a), Y: q.Y + r*math.Sin(a)}
			}
			pdf.Polygon(disc, "F")
		}
	}
}
