package artifacts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/crewship-ai/crewship/internal/slides"
)

// ErrSlidesArtifactUnsupported marks content outside the closed HTML profile.
var ErrSlidesArtifactUnsupported = errors.New("unsupported Slides artifact")
var artifactSlideID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
var artifactSlidePixels = regexp.MustCompile(`^(0|[1-9][0-9]*)px$`)
var artifactSlideColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

const artifactSlideSourceBytes = 2 << 20
const artifactSlideFileBytes = 256 << 10
const artifactSlideFont = "Go Regular"

// ArtifactSlideAnchor names actual authored content for the existing CommentQueue.
type ArtifactSlideAnchor struct {
	SlideID  string `json:"slide_id"`
	Path     string `json:"path"`
	Selector string `json:"selector"`
	Quote    string `json:"quote"`
}

// ArtifactSlidesProjection is transient and carries no persistence or access grant.
type ArtifactSlidesProjection struct {
	Deck    slides.Deck                    `json:"deck"`
	Anchors map[string]ArtifactSlideAnchor `json:"anchors"`
}

func unsupportedSlide(reason string) error {
	return fmt.Errorf("%w: %s", ErrSlidesArtifactUnsupported, reason)
}

// ParseSlidesArtifact derives a read-only view; VersionStore files remain the sole
// content authority. It grants no access, performs no I/O and never repairs source.
func ParseSlidesArtifact(pageID string, files map[string]VersionFile) (ArtifactSlidesProjection, error) {
	empty := ArtifactSlidesProjection{}
	index, ok := files["deck.json"]
	if !ok || index.MediaType != "application/json" || len(files) > 101 {
		return empty, unsupportedSlide("missing or unsupported deck index")
	}
	order, err := artifactSlideOrder(index.Content)
	if err != nil {
		return empty, err
	}
	if len(files) != len(order)+1 {
		return empty, unsupportedSlide("manifest has missing or unlisted files")
	}
	total := 0
	for _, file := range files {
		if len(file.Content) > artifactSlideSourceBytes-total {
			return empty, unsupportedSlide("source byte budget")
		}
		total += len(file.Content)
	}
	out := ArtifactSlidesProjection{Deck: slides.Deck{SchemaVersion: 1, DeckID: pageID, Width: 1920, Height: 1080, Theme: slides.Theme{Background: "#FFFFFF", Color: "#111111", FontFamily: artifactSlideFont, FontSize: 32}, SlideOrder: append([]string{}, order...), Slides: make([]slides.Slide, 0, len(order))}, Anchors: map[string]ArtifactSlideAnchor{}}
	ids := map[string]bool{}
	count := 0
	for _, id := range order {
		path := "slides/" + id + ".html"
		file, ok := files[path]
		if !ok || file.MediaType != "text/html" {
			return empty, unsupportedSlide("missing HTML slide")
		}
		root, err := artifactSlideHTML(file.Content)
		if err != nil {
			return empty, err
		}
		attrs, err := artifactSlideAttributes(root, []string{"id"}, "data-transition")
		if err != nil || attrs["id"] != id {
			return empty, unsupportedSlide("section identity mismatch")
		}
		if err := reserveSlideID(ids, id); err != nil {
			return empty, err
		}
		slide := slides.Slide{ID: id, Elements: []slides.Element{}}
		if transition, ok := attrs["data-transition"]; ok {
			if transition != "fade" && transition != "push" {
				return empty, unsupportedSlide("unsupported slide transition")
			}
			slide.Transition = transition
		}
		notesSeen := false
		for _, node := range slideChildren(root) {
			if node.Data == "aside" {
				if notesSeen {
					return empty, unsupportedSlide("duplicate notes")
				}
				notesSeen = true
				attr, err := artifactSlideAttributes(node, []string{"id"})
				if err != nil {
					return empty, err
				}
				if err = reserveSlideID(ids, attr["id"]); err != nil {
					return empty, err
				}
				note, err := plainSlideText(node)
				if err != nil || utf8.RuneCountInString(note) > 4000 {
					return empty, unsupportedSlide("unsupported notes or note budget")
				}
				slide.Notes = note
				continue
			}
			if notesSeen {
				return empty, unsupportedSlide("notes must follow slide elements")
			}
			count++
			if count > slides.MaxElements || len(slide.Elements) >= 200 {
				return empty, unsupportedSlide("element budget")
			}
			element, err := artifactSlideElement(node)
			if err != nil {
				return empty, err
			}
			if err = reserveSlideID(ids, element.ID); err != nil {
				return empty, err
			}
			quote := artifactSlideQuote(node)
			if quote != "" {
				out.Anchors[element.ID] = ArtifactSlideAnchor{SlideID: id, Path: path, Selector: "#" + element.ID, Quote: quote}
			}
			slide.Elements = append(slide.Elements, element)
		}
		out.Deck.Slides = append(out.Deck.Slides, slide)
	}
	// A deck that uses any rich-profile field projects as schema 3; every other
	// deck keeps its exact schema-1 projection.
	if artifactSlidesRich(out.Deck) {
		out.Deck.SchemaVersion = slides.RichSchemaVersion
	}
	// The renderer and this adapter share the existing typed admission, including
	// geometry, table-cell limits, Unicode/text limits and canonical JSON <=2 MiB.
	if err := slides.ValidateProjection(out.Deck); err != nil {
		return empty, unsupportedSlide("typed deck validation: " + err.Error())
	}
	return out, nil
}
func reserveSlideID(ids map[string]bool, id string) error {
	if !artifactSlideID.MatchString(id) || ids[id] {
		return unsupportedSlide("missing, invalid or duplicate authored ID")
	}
	ids[id] = true
	return nil
}
func artifactSlideOrder(raw []byte) ([]string, error) {
	if len(raw) > 16384 || !utf8.Valid(raw) {
		return nil, unsupportedSlide("index byte budget or UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, unsupportedSlide("index object required")
	}
	seen := map[string]bool{}
	var order []string
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return nil, unsupportedSlide("duplicate index key")
		}
		seen[key] = true
		switch key {
		case "schema_version":
			value, err := d.Token()
			n, ok := value.(json.Number)
			if err != nil || !ok || n.String() != "1" {
				return nil, unsupportedSlide("unsupported index schema")
			}
		case "order":
			if d.Decode(&order) != nil {
				return nil, unsupportedSlide("invalid slide order")
			}
		default:
			return nil, unsupportedSlide("unknown index field")
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || len(seen) != 2 || !seen["schema_version"] || !seen["order"] || len(order) < 1 || len(order) > slides.MaxSlides {
		return nil, unsupportedSlide("invalid index or slide count")
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, unsupportedSlide("trailing index JSON")
	}
	ids := map[string]bool{}
	for _, id := range order {
		if err := reserveSlideID(ids, id); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// Tokenize the original instead of accepting the HTML tree builder's repairs.
// A lexical attribute check also rejects duplicates that the tokenizer drops.
// Neither layer starts a browser or executes source.
func artifactSlideHTML(raw []byte) (*html.Node, error) {
	if len(raw) > artifactSlideFileBytes || !utf8.Valid(raw) {
		return nil, unsupportedSlide("slide byte budget or UTF-8")
	}
	for _, r := range string(raw) {
		if r < 32 && r != '\n' && r != '\t' || r == 127 {
			return nil, unsupportedSlide("HTML control character")
		}
	}
	z := html.NewTokenizer(bytes.NewReader(raw))
	z.SetMaxBuf(artifactSlideFileBytes)
	var root *html.Node
	stack := []*html.Node{}
	nodes := 0
	for {
		kind := z.Next()
		if kind == html.ErrorToken {
			if z.Err() != io.EOF || len(stack) != 0 || root == nil {
				return nil, unsupportedSlide("malformed HTML")
			}
			return root, nil
		}
		nodes++
		if nodes > 10000 {
			return nil, unsupportedSlide("HTML node budget")
		}
		rawToken := string(z.Raw())
		token := z.Token()
		switch kind {
		case html.StartTagToken:
			if !closedSlideStart(rawToken, len(token.Attr)) {
				return nil, unsupportedSlide("duplicate or malformed HTML attributes")
			}
			if len(stack) >= 5 {
				return nil, unsupportedSlide("HTML depth budget")
			}
			allowed := false
			if len(stack) == 0 {
				allowed = root == nil && token.Data == "section"
			} else {
				switch stack[len(stack)-1].Data {
				case "section":
					allowed = token.Data == "p" || token.Data == "div" || token.Data == "table" || token.Data == "aside"
				case "table":
					allowed = token.Data == "tbody"
				case "tbody":
					allowed = token.Data == "tr"
				case "tr":
					allowed = token.Data == "td"
				}
			}
			if !allowed {
				return nil, unsupportedSlide("unsupported or nested HTML element")
			}
			seen := map[string]bool{}
			for _, attr := range token.Attr {
				if attr.Namespace != "" || seen[attr.Key] {
					return nil, unsupportedSlide("duplicate or namespaced HTML attribute")
				}
				seen[attr.Key] = true
			}
			node := &html.Node{Type: html.ElementNode, Data: token.Data, Attr: token.Attr}
			if len(stack) == 0 {
				root = node
			} else {
				stack[len(stack)-1].AppendChild(node)
			}
			stack = append(stack, node)
		case html.EndTagToken:
			// End-tag attributes and browser recovery are outside this closed profile.
			end := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(rawToken, "</"), ">"))
			if len(stack) == 0 || stack[len(stack)-1].Data != token.Data || !strings.EqualFold(end, token.Data) {
				return nil, unsupportedSlide("unbalanced HTML")
			}
			stack = stack[:len(stack)-1]
		case html.TextToken:
			if len(stack) == 0 {
				if strings.TrimSpace(token.Data) != "" {
					return nil, unsupportedSlide("text outside slide")
				}
				continue
			}
			current := stack[len(stack)-1]
			if current.Data != "p" && current.Data != "td" && current.Data != "aside" && strings.TrimSpace(token.Data) != "" {
				return nil, unsupportedSlide("text outside a primitive")
			}
			current.AppendChild(&html.Node{Type: html.TextNode, Data: token.Data})
		default:
			return nil, unsupportedSlide("comments, doctypes and self-closing markup are unsupported")
		}
	}
}

// This deliberately narrow syntax requires quoted attribute values. It counts
// original attributes before x/net/html can deduplicate them (including boolean
// duplicates), without interpreting CSS, entities or any source as code.
func closedSlideStart(raw string, parsedCount int) bool {
	space := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' }
	name := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' }
	index := 1
	for index < len(raw) && name(raw[index]) {
		index++
	}
	count := 0
	seen := map[string]bool{}
	for index < len(raw) {
		if raw[index] == '>' {
			return index == len(raw)-1 && count == parsedCount
		}
		if !space(raw[index]) {
			return false
		}
		for index < len(raw) && space(raw[index]) {
			index++
		}
		if index < len(raw) && raw[index] == '>' {
			return index == len(raw)-1 && count == parsedCount
		}
		start := index
		for index < len(raw) && name(raw[index]) {
			index++
		}
		key := strings.ToLower(raw[start:index])
		if key == "" || seen[key] {
			return false
		}
		seen[key] = true
		count++
		for index < len(raw) && space(raw[index]) {
			index++
		}
		if index >= len(raw) || raw[index] != '=' {
			return false
		}
		index++
		for index < len(raw) && space(raw[index]) {
			index++
		}
		if index >= len(raw) || (raw[index] != '"' && raw[index] != '\'') {
			return false
		}
		quote := raw[index]
		index++
		for index < len(raw) && raw[index] != quote {
			index++
		}
		if index >= len(raw) {
			return false
		}
		index++
	}
	return false
}

func artifactSlideAttributes(node *html.Node, required []string, optional ...string) (map[string]string, error) {
	attrs := map[string]string{}
	for _, attr := range node.Attr {
		attrs[attr.Key] = attr.Val
	}
	present := len(required)
	for _, key := range optional {
		if _, ok := attrs[key]; ok {
			present++
		}
	}
	if len(attrs) != present {
		return nil, unsupportedSlide("unsupported HTML attributes")
	}
	for _, key := range required {
		if _, ok := attrs[key]; !ok {
			return nil, unsupportedSlide("missing HTML attribute")
		}
	}
	return attrs, nil
}
func slideChildren(node *html.Node) []*html.Node {
	children := []*html.Node{}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode {
			children = append(children, child)
		}
	}
	return children
}
func plainSlideText(node *html.Node) (string, error) {
	var text strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.TextNode {
			return "", unsupportedSlide("primitive requires plain text")
		}
		text.WriteString(child.Data)
	}
	return text.String(), nil
}
func slideNodeText(node *html.Node) string {
	var text strings.Builder
	var collect func(*html.Node)
	collect = func(n *html.Node) {
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			collect(child)
		}
	}
	collect(node)
	return text.String()
}

// Match the existing Project browser/API anchor: concatenate actual descendant
// text, normalize Go whitespace, then normalize the first 500 Unicode codepoints.
// This bounds even four-byte quotes to the existing ingress limit of 2000 bytes.
func artifactSlideQuote(node *html.Node) string {
	text := strings.Join(strings.Fields(slideNodeText(node)), " ")
	count := 0
	for offset := range text {
		if count == 500 {
			return strings.Join(strings.Fields(text[:offset]), " ")
		}
		count++
	}
	return text
}

var artifactSlideSignedPixels = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,3})px$`)
var artifactSlideLineHeight = regexp.MustCompile(`^([0-9])(\.[0-9]{1,2})?$`)
var artifactSlideWeight = regexp.MustCompile(`^[1-9]00$`)
var artifactSlideGradient = regexp.MustCompile(`^linear-gradient\(\s*(0|[1-9][0-9]{0,2})deg((?:\s*,\s*#[0-9A-Fa-f]{6}\s+(?:0|[1-9][0-9]{0,2})%){2,8})\s*\)$`)
var artifactSlideGradientStop = regexp.MustCompile(`#([0-9A-Fa-f]{6})\s+(0|[1-9][0-9]{0,2})%`)
var artifactSlideLine = regexp.MustCompile(`^(0|[1-9][0-9]{0,3}) (0|[1-9][0-9]{0,3}) (0|[1-9][0-9]{0,3}) (0|[1-9][0-9]{0,3})$`)

// artifactSlideStyles parses a closed inline style: every required key, any of
// the optional keys and nothing else. It interprets no CSS beyond the profile.
func artifactSlideStyles(source string, required, optional []string) (map[string]string, error) {
	styles := map[string]string{}
	for _, part := range strings.Split(strings.TrimSuffix(strings.TrimSpace(source), ";"), ";") {
		key, value, ok := strings.Cut(part, ":")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || key == "" || value == "" {
			return nil, unsupportedSlide("malformed style")
		}
		if _, ok := styles[key]; ok {
			return nil, unsupportedSlide("duplicate style")
		}
		styles[key] = value
	}
	present := len(required)
	for _, key := range optional {
		if _, ok := styles[key]; ok {
			present++
		}
	}
	if len(styles) != present {
		return nil, unsupportedSlide("unsupported styles")
	}
	for _, key := range required {
		if _, ok := styles[key]; !ok {
			return nil, unsupportedSlide("missing style")
		}
	}
	return styles, nil
}

// artifactSlideFamily maps one quoted or bare family name of the allow-list.
// The legacy profile's "sans-serif" keeps meaning Go Regular. Fallback lists
// are refused: a deck names exactly the face it is rendered with.
func artifactSlideFamily(value string, present bool) (string, error) {
	if !present {
		return artifactSlideFont, nil
	}
	if value == "sans-serif" {
		return artifactSlideFont, nil
	}
	if len(value) >= 2 && (value[0] == '\'' || value[0] == '"') && value[len(value)-1] == value[0] {
		value = value[1 : len(value)-1]
	}
	if _, ok := slides.RichFonts[value]; !ok {
		return "", unsupportedSlide("unsupported font")
	}
	return value, nil
}

func slidePixel(value string) (int, error) {
	if !artifactSlidePixels.MatchString(value) {
		return 0, unsupportedSlide("integer CSS pixels required")
	}
	number, err := strconv.Atoi(strings.TrimSuffix(value, "px"))
	if err != nil {
		return 0, unsupportedSlide("pixel overflow")
	}
	return number, nil
}

func artifactSlideText(style map[string]string) (*slides.Text, error) {
	family, err := artifactSlideFamily(style["font-family"], style["font-family"] != "")
	if err != nil {
		return nil, err
	}
	size, err := slidePixel(style["font-size"])
	if err != nil {
		return nil, err
	}
	text := &slides.Text{FontFamily: family, FontSize: size, Color: style["color"], Align: style["text-align"]}
	if value, ok := style["font-weight"]; ok {
		if !artifactSlideWeight.MatchString(value) {
			return nil, unsupportedSlide("numeric font weight required")
		}
		text.FontWeight, _ = strconv.Atoi(value)
		if text.FontWeight == 400 {
			text.FontWeight = 0
		}
	}
	if value, ok := style["letter-spacing"]; ok {
		if !artifactSlideSignedPixels.MatchString(value) {
			return nil, unsupportedSlide("integer letter spacing required")
		}
		text.LetterSpacing, _ = strconv.Atoi(strings.TrimSuffix(value, "px"))
	}
	if value, ok := style["line-height"]; ok {
		m := artifactSlideLineHeight.FindStringSubmatch(value)
		if m == nil {
			return nil, unsupportedSlide("unitless line height required")
		}
		whole, _ := strconv.Atoi(m[1])
		fraction := strings.TrimPrefix(m[2], ".")
		for len(fraction) < 2 {
			fraction += "0"
		}
		hundredths, _ := strconv.Atoi(fraction)
		text.LineHeight = whole*100 + hundredths
		if text.LineHeight == 0 {
			return nil, unsupportedSlide("line height out of range")
		}
	}
	return text, nil
}

func artifactSlideGradientValue(value string) (*slides.Gradient, error) {
	m := artifactSlideGradient.FindStringSubmatch(value)
	if m == nil {
		return nil, unsupportedSlide("only linear-gradient(<deg>, <hex> <percent>, ...) is supported")
	}
	angle, _ := strconv.Atoi(m[1])
	g := &slides.Gradient{Angle: angle}
	for _, stop := range artifactSlideGradientStop.FindAllStringSubmatch(m[2], -1) {
		position, _ := strconv.Atoi(stop[2])
		g.Stops = append(g.Stops, slides.GradientStop{Color: "#" + stop[1], Position: position})
	}
	return g, nil
}

func artifactSlideElement(node *html.Node) (slides.Element, error) {
	empty := slides.Element{}
	geometryKeys := []string{"left", "top", "width", "height"}
	var attrs, style map[string]string
	var err error
	kind := node.Data
	switch node.Data {
	case "p":
		if attrs, err = artifactSlideAttributes(node, []string{"id", "style"}); err == nil {
			style, err = artifactSlideStyles(attrs["style"], append(geometryKeys, "font-size", "color", "text-align"), []string{"font-family", "font-weight", "letter-spacing", "line-height"})
		}
	case "div":
		if attrs, err = artifactSlideAttributes(node, []string{"id", "style"}, "data-shape", "data-icon", "data-line", "data-arrow-start", "data-arrow-end"); err != nil {
			break
		}
		_, shape := attrs["data-shape"]
		_, icon := attrs["data-icon"]
		_, line := attrs["data-line"]
		_, start := attrs["data-arrow-start"]
		_, end := attrs["data-arrow-end"]
		switch {
		case icon && !shape && !line && !start && !end:
			kind = "icon"
			style, err = artifactSlideStyles(attrs["style"], append(geometryKeys, "color"), nil)
		case shape && !icon && attrs["data-shape"] == "line" && line:
			kind = "line"
			style, err = artifactSlideStyles(attrs["style"], append(geometryKeys, "border-color", "border-width"), nil)
		case shape && !icon && !line && !start && !end:
			style, err = artifactSlideStyles(attrs["style"], append(geometryKeys, "background-color", "border-color", "border-width"), []string{"border-radius", "background-image"})
		default:
			err = unsupportedSlide("unsupported shape attributes")
		}
	case "table":
		if attrs, err = artifactSlideAttributes(node, []string{"id", "style"}); err == nil {
			style, err = artifactSlideStyles(attrs["style"], append(geometryKeys, "font-size", "color", "background-color", "border-color"), []string{"font-family"})
		}
	default:
		return empty, unsupportedSlide("unsupported primitive")
	}
	if err != nil {
		return empty, err
	}
	element := slides.Element{ID: attrs["id"]}
	geometry := []*int{&element.X, &element.Y, &element.Width, &element.Height}
	for i, key := range geometryKeys {
		*geometry[i], err = slidePixel(style[key])
		if err != nil {
			return empty, err
		}
	}
	for _, key := range []string{"color", "background-color", "border-color"} {
		if value, ok := style[key]; ok && !artifactSlideColor.MatchString(value) {
			return empty, unsupportedSlide("hex colors required")
		}
	}
	if kind == "div" || kind == "icon" || kind == "line" {
		if value, err := plainSlideText(node); err != nil || value != "" {
			return empty, unsupportedSlide("shape must be empty")
		}
	}
	switch kind {
	case "p":
		value, err := plainSlideText(node)
		if err != nil {
			return empty, err
		}
		text, err := artifactSlideText(style)
		if err != nil {
			return empty, err
		}
		text.Value = value
		element.Type = "text"
		element.Text = text
	case "icon":
		element.Type = "icon"
		element.Icon = &slides.Icon{Name: attrs["data-icon"], Color: style["color"]}
	case "line":
		width, err := slidePixel(style["border-width"])
		if err != nil {
			return empty, err
		}
		m := artifactSlideLine.FindStringSubmatch(attrs["data-line"])
		if m == nil {
			return empty, unsupportedSlide(`data-line must be "x1 y1 x2 y2" in whole pixels`)
		}
		points := [4]int{}
		for i := range points {
			points[i], _ = strconv.Atoi(m[i+1])
		}
		arrow := func(key string) string {
			if value, ok := attrs[key]; ok {
				return value
			}
			return "none"
		}
		element.Type = "shape"
		element.Shape = &slides.Shape{Kind: "line", Fill: style["border-color"], Stroke: style["border-color"], StrokeWidth: width, Line: &slides.Line{X1: points[0], Y1: points[1], X2: points[2], Y2: points[3], Start: arrow("data-arrow-start"), End: arrow("data-arrow-end")}}
	case "div":
		width, err := slidePixel(style["border-width"])
		if err != nil {
			return empty, err
		}
		element.Type = "shape"
		element.Shape = &slides.Shape{Kind: attrs["data-shape"], Fill: style["background-color"], Stroke: style["border-color"], StrokeWidth: width}
		if value, ok := style["border-radius"]; ok {
			if element.Shape.CornerRadius, err = slidePixel(value); err != nil {
				return empty, err
			}
		}
		if value, ok := style["background-image"]; ok {
			if element.Shape.Gradient, err = artifactSlideGradientValue(value); err != nil {
				return empty, err
			}
		}
	case "table":
		bodies := slideChildren(node)
		if len(bodies) != 1 || len(bodies[0].Attr) != 0 {
			return empty, unsupportedSlide("one plain tbody required")
		}
		size, err := slidePixel(style["font-size"])
		if err != nil {
			return empty, err
		}
		family, err := artifactSlideFamily(style["font-family"], style["font-family"] != "")
		if err != nil {
			return empty, err
		}
		cells := [][]string{}
		for _, row := range slideChildren(bodies[0]) {
			if len(row.Attr) != 0 {
				return empty, unsupportedSlide("table row attributes")
			}
			values := []string{}
			for _, cell := range slideChildren(row) {
				if len(cell.Attr) != 0 {
					return empty, unsupportedSlide("table cell attributes")
				}
				value, err := plainSlideText(cell)
				if err != nil {
					return empty, err
				}
				values = append(values, value)
			}
			cells = append(cells, values)
		}
		element.Type = "table"
		element.Table = &slides.Table{Cells: cells, FontFamily: family, FontSize: size, Color: style["color"], Fill: style["background-color"], Border: style["border-color"]}
	}
	return element, nil
}

// artifactSlidesRich reports whether a parsed deck needs schema 3.
func artifactSlidesRich(deck slides.Deck) bool {
	for _, slide := range deck.Slides {
		if slide.Transition != "" {
			return true
		}
		for _, e := range slide.Elements {
			switch {
			case e.Icon != nil:
				return true
			case e.Text != nil && (e.Text.FontFamily != artifactSlideFont || e.Text.FontSize > slides.MaxFontSize || e.Text.FontWeight != 0 || e.Text.LetterSpacing != 0 || e.Text.LineHeight != 0):
				return true
			case e.Shape != nil && (e.Shape.Kind == "line" || e.Shape.CornerRadius != 0 || e.Shape.Gradient != nil):
				return true
			case e.Table != nil && (e.Table.FontFamily != artifactSlideFont || e.Table.FontSize > slides.MaxFontSize):
				return true
			}
		}
	}
	return false
}
