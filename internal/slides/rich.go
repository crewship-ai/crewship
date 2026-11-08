package slides

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
)

// Schema 3 is the artifact rich profile: schema 1 plus a closed set of
// typography, shape, icon and transition fields. It is produced only by the
// artifact Slides adapter (a transient read projection) and is never stored
// by the revision-based Slides authority, whose SupportedSchema stays 1|2.
// Every schema-3 field is omitted when it holds its default, so a deck that
// uses none of them is byte-identical to its schema-1 form.
const (
	RichSchemaVersion = 3
	RichHashDomain    = "crewship-slides-canonical-json/v3\n"
	MaxRichFontSize   = 320
	MaxFontSize       = 160
	MinLetterSpacing  = -20
	MaxLetterSpacing  = 100
	MinLineHeight     = 80
	MaxLineHeight     = 300
	MaxGradientStops  = 8
)

// Gradient is a CSS-style linear gradient: Angle in degrees (0 points up,
// clockwise), stops at whole percent positions along the gradient line.
type Gradient struct {
	Angle int            `json:"angle"`
	Stops []GradientStop `json:"stops"`
}
type GradientStop struct {
	Color    string `json:"color"`
	Position int    `json:"position"`
}

// Line is a straight segment in element-local pixels. Start and End are the
// arrowheads at (X1,Y1) and (X2,Y2): "none" or "triangle".
type Line struct {
	X1    int    `json:"x1"`
	Y1    int    `json:"y1"`
	X2    int    `json:"x2"`
	Y2    int    `json:"y2"`
	Start string `json:"start"`
	End   string `json:"end"`
}

// Icon names one glyph of the embedded allow-list, drawn as an outline in Color.
type Icon struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// RichFonts maps each admitted family to its admitted weights. Each pair has an
// embedded OFL/BSD font file in the PDF renderer and in the web app.
var RichFonts = map[string][]int{
	"Go Regular":     {400},
	"IBM Plex Sans":  {400, 600, 700},
	"JetBrains Mono": {400, 600},
}

// RichFontAllowed reports whether family and weight (0 = 400) are admitted.
func RichFontAllowed(family string, weight int) bool {
	if weight == 0 {
		weight = 400
	}
	for _, w := range RichFonts[family] {
		if w == weight {
			return true
		}
	}
	return false
}

//go:embed lucide_icons.json
var lucideIconsJSON []byte

// IconNode is one SVG primitive of an allow-listed icon on a 24-unit canvas.
type IconNode struct {
	Tag   string
	Attrs map[string]string
}

var iconSet map[string][]IconNode
var iconAttrs = map[string][]string{"path": {"d"}, "circle": {"cx", "cy", "r"}, "ellipse": {"cx", "cy", "rx", "ry"}, "line": {"x1", "y1", "x2", "y2"}, "rect": {"x", "y", "width", "height", "rx", "ry"}, "polyline": {"points"}, "polygon": {"points"}}
var iconValue = regexp.MustCompile(`^[0-9A-Za-z .,\-]+$`)

func init() {
	set, err := parseIcons(lucideIconsJSON)
	if err != nil {
		panic("slides: embedded icon set: " + err.Error())
	}
	iconSet = set
}

func parseIcons(raw []byte) (map[string][]IconNode, error) {
	var body struct {
		Source      string                          `json:"source"`
		License     string                          `json:"license"`
		ViewBox     int                             `json:"view_box"`
		StrokeWidth int                             `json:"stroke_width"`
		Icons       map[string][][2]json.RawMessage `json:"icons"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	if body.ViewBox != 24 || body.StrokeWidth != 2 || len(body.Icons) == 0 {
		return nil, errors.New("unexpected icon set header")
	}
	out := map[string][]IconNode{}
	for name, nodes := range body.Icons {
		if !identifier.MatchString(name) || len(nodes) == 0 {
			return nil, fmt.Errorf("icon %q", name)
		}
		for _, node := range nodes {
			var tag string
			var attrs map[string]string
			if json.Unmarshal(node[0], &tag) != nil || json.Unmarshal(node[1], &attrs) != nil {
				return nil, fmt.Errorf("icon %q node", name)
			}
			allowed := iconAttrs[tag]
			if allowed == nil {
				return nil, fmt.Errorf("icon %q tag %q", name, tag)
			}
			for key, value := range attrs {
				ok := false
				for _, a := range allowed {
					ok = ok || a == key
				}
				if !ok || len(value) > 1024 || !iconValue.MatchString(value) {
					return nil, fmt.Errorf("icon %q attribute %q", name, key)
				}
			}
			out[name] = append(out[name], IconNode{Tag: tag, Attrs: attrs})
		}
	}
	return out, nil
}

// IconNodes returns the primitives of an allow-listed icon, or false.
func IconNodes(name string) ([]IconNode, bool) {
	nodes, ok := iconSet[name]
	return nodes, ok
}

// IconNames lists the allow-list in sorted order.
func IconNames() []string {
	names := make([]string, 0, len(iconSet))
	for name := range iconSet {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ValidateProjection admits the artifact read projection: schema 1, or schema 3
// with the rich fields. It shares every schema-1 rule with Validate.
func ValidateProjection(deck Deck) error {
	if deck.SchemaVersion != SchemaVersion && deck.SchemaVersion != RichSchemaVersion {
		return ErrSchema
	}
	return validate(deck)
}

func validRichText(v *Text, rich bool) bool {
	if !rich {
		return v.FontWeight == 0 && v.LetterSpacing == 0 && v.LineHeight == 0
	}
	return RichFontAllowed(v.FontFamily, v.FontWeight) && v.LetterSpacing >= MinLetterSpacing && 2*v.LetterSpacing >= -v.FontSize && v.LetterSpacing <= MaxLetterSpacing && (v.LineHeight == 0 || v.LineHeight >= MinLineHeight && v.LineHeight <= MaxLineHeight)
}

func validRichShape(element Element, rich bool) bool {
	v := element.Shape
	if !rich {
		return (v.Kind == "rectangle" || v.Kind == "ellipse") && v.CornerRadius == 0 && v.Gradient == nil && v.Line == nil
	}
	switch v.Kind {
	case "rectangle", "ellipse":
		if v.Line != nil || v.CornerRadius < 0 || v.Kind == "ellipse" && v.CornerRadius != 0 || 2*v.CornerRadius > min(element.Width, element.Height) {
			return false
		}
	case "line":
		l := v.Line
		if l == nil || v.CornerRadius != 0 || v.Gradient != nil || v.StrokeWidth < 1 || v.Fill != v.Stroke || l.X1 < 0 || l.X2 < 0 || l.Y1 < 0 || l.Y2 < 0 || l.X1 > element.Width || l.X2 > element.Width || l.Y1 > element.Height || l.Y2 > element.Height || l.X1 == l.X2 && l.Y1 == l.Y2 || !validArrow(l.Start) || !validArrow(l.End) {
			return false
		}
	default:
		return false
	}
	if g := v.Gradient; g != nil {
		if g.Angle < 0 || g.Angle > 359 || len(g.Stops) < 2 || len(g.Stops) > MaxGradientStops {
			return false
		}
		last := 0
		for _, stop := range g.Stops {
			if !color.MatchString(stop.Color) || stop.Position < last || stop.Position > 100 {
				return false
			}
			last = stop.Position
		}
	}
	return true
}

func validArrow(value string) bool { return value == "none" || value == "triangle" }

func validIcon(element Element, rich bool) bool {
	v := element.Icon
	if !rich || v == nil || element.Text != nil || element.Shape != nil || element.Table != nil || element.Image != nil || !color.MatchString(v.Color) {
		return false
	}
	_, ok := iconSet[v.Name]
	return ok
}
