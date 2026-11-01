package design

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strconv"
)

// ExportCSP is the policy of every exported HTML file: no script, no network,
// inline styles and data: images only. The same value is sent as a response
// header and embedded as a meta tag, so the file stays inert when opened later.
const ExportCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src data:"

// ErrExportTooLarge refuses an export whose file would exceed the caller's limit.
var ErrExportTooLarge = errors.New("design export exceeds size limit")

// ExportInput is one immutable Design version as validated by the engine.
type ExportInput struct {
	Name    string
	Version int64
	Nodes   []json.RawMessage
	Order   []string
}

type exportNode struct {
	ID       string  `json:"id"`
	Type     string  `json:"type"`
	Name     string  `json:"name"`
	ParentID string  `json:"parentId"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Width    float64 `json:"width"`
	Height   float64 `json:"height"`
	Fill     string  `json:"fill"`
	Text     string  `json:"text"`
	Src      string  `json:"src"`
	LinkTo   string  `json:"linkTo"`
}

// Same image rule as the browser (safeImageSource): only base64 raster data
// URIs are embedded. https images are not fetched, so the file needs no network.
var exportImage = regexp.MustCompile(`^data:image/(png|jpeg|webp|gif);base64,[a-zA-Z0-9+/=]+$`)

// A fill reaches a style attribute, so only plain color syntax is accepted;
// anything else (url(), ;, quotes, comments) falls back to the default paint.
var exportColor = regexp.MustCompile(`^(#[0-9a-fA-F]{3,8}|[a-zA-Z]{1,32}|(rgb|rgba|hsl|hsla)\([0-9.,%/ +-]{1,64}\))$`)

const exportStyle = `*{box-sizing:border-box}body{margin:0;background:#f1f5f9;color:#0f172a;font:14px/1.5 system-ui,sans-serif}` +
	`.export-header{padding:24px 32px;background:#fff;border-bottom:1px solid #e2e8f0}.export-header h1{margin:0 0 4px;font-size:20px}` +
	`.export-header p{margin:0;color:#475569}nav ol{margin:12px 0 0;padding-left:20px}nav a{color:#1d4ed8}` +
	`main{padding:16px 32px 48px}.screen{margin:24px 0}.screen h2{margin:0 0 8px;font-size:16px}.screen h2 a{color:inherit;text-decoration:none}` +
	`.scroll{max-width:100%;overflow:auto}.canvas{position:relative;overflow:hidden;box-shadow:inset 0 0 0 1px #cbd5e1,0 4px 16px rgba(15,23,42,.12)}` +
	`.layer{position:absolute;display:block;color:inherit;text-decoration:none}.rect{border-radius:6px}` +
	`.text{white-space:pre;overflow:visible;font:20px/1.3 system-ui,sans-serif}.image img{display:block;width:100%;height:100%;object-fit:cover}` +
	`.placeholder{border-radius:4px;background:#e2e8f0;color:#64748b;padding:8px 12px;font-size:14px;overflow:hidden}` +
	`a.hotspot{cursor:pointer}a.hotspot:hover,a.hotspot:focus-visible{outline:2px solid #2563eb;outline-offset:1px}` +
	`.broken-link{outline:2px dashed #d97706;outline-offset:1px}.empty{color:#475569}`

// RenderHTML writes one self-contained HTML document with every screen
// (frame) of the version. Layers use the same geometry as the prototype
// player: positions accumulate through parentId and each non-screen layer is
// drawn on its nearest screen. Prototype links become fragment anchors. The
// output depends only on the input, so a version always exports to the same
// bytes.
func RenderHTML(in ExportInput, limit int) ([]byte, error) {
	nodes, err := exportOrderedNodes(in)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*exportNode, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	position := func(node *exportNode) (float64, float64) {
		var x, y float64
		seen := map[string]bool{}
		for current := node; current != nil && !seen[current.ID]; current = byID[current.ParentID] {
			seen[current.ID] = true
			x += current.X
			y += current.Y
			if current.ParentID == "" {
				break
			}
		}
		return x, y
	}
	screenOf := func(node *exportNode) string {
		seen := map[string]bool{}
		for current := node; current != nil && !seen[current.ID]; current = byID[current.ParentID] {
			if current.Type == "frame" {
				return current.ID
			}
			seen[current.ID] = true
			if current.ParentID == "" {
				break
			}
		}
		return ""
	}
	var screens []*exportNode
	anchors := map[string]string{}
	for _, node := range nodes {
		if node.Type == "frame" {
			screens = append(screens, node)
			anchors[node.ID] = "screen-" + strconv.Itoa(len(screens))
		}
	}
	name := in.Name
	if name == "" {
		name = "Design"
	}
	var b bytes.Buffer
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta http-equiv=\"Content-Security-Policy\" content=\"" + html.EscapeString(ExportCSP) + "\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n<meta name=\"generator\" content=\"Crewship Design export\">\n")
	fmt.Fprintf(&b, "<title>%s · Design version %d</title>\n<style>%s</style>\n</head>\n<body>\n", html.EscapeString(name), in.Version, exportStyle)
	fmt.Fprintf(&b, "<header class=\"export-header\">\n<h1>%s</h1>\n<p>Design version %d · %d %s · exported from Crewship. Linked layers jump to their screen.</p>\n", html.EscapeString(name), in.Version, len(screens), plural(len(screens), "screen", "screens"))
	if len(screens) > 0 {
		b.WriteString("<nav aria-label=\"Screens\"><ol>")
		for _, screen := range screens {
			fmt.Fprintf(&b, "<li><a href=\"#%s\">%s</a></li>", anchors[screen.ID], html.EscapeString(label(screen.Name, "Screen")))
		}
		b.WriteString("</ol></nav>\n")
	}
	b.WriteString("</header>\n<main>\n")
	if len(screens) == 0 {
		b.WriteString("<p class=\"empty\">This version has no screens.</p>\n")
	}
	for _, screen := range screens {
		anchor := anchors[screen.ID]
		ox, oy := position(screen)
		width, height := atLeastOne(screen.Width), atLeastOne(screen.Height)
		fmt.Fprintf(&b, "<section class=\"screen\" id=\"%s\" data-node-id=\"%s\" aria-labelledby=\"%s-title\">\n", anchor, html.EscapeString(screen.ID), anchor)
		fmt.Fprintf(&b, "<h2 id=\"%s-title\"><a href=\"#%s\">%s</a></h2>\n", anchor, anchor, html.EscapeString(label(screen.Name, "Screen")))
		fmt.Fprintf(&b, "<div class=\"scroll\"><div class=\"canvas\" role=\"img\" aria-label=\"%s\" style=\"width:%spx;height:%spx;background:%s\">\n", html.EscapeString(label(screen.Name, "Screen")), px(width), px(height), color(screen.Fill, "#ffffff"))
		for _, node := range nodes {
			if node.Type == "frame" || screenOf(node) != screen.ID {
				continue
			}
			x, y := position(node)
			geometry := fmt.Sprintf("left:%spx;top:%spx;width:%spx;height:%spx", px(x-ox), px(y-oy), px(atLeastOne(node.Width)), px(atLeastOne(node.Height)))
			writeLayer(&b, node, geometry, anchors, byID)
		}
		b.WriteString("</div></div>\n</section>\n")
		if b.Len() > limit {
			return nil, ErrExportTooLarge
		}
	}
	b.WriteString("</main>\n</body>\n</html>\n")
	if b.Len() > limit {
		return nil, ErrExportTooLarge
	}
	return b.Bytes(), nil
}

func writeLayer(b *bytes.Buffer, node *exportNode, geometry string, anchors map[string]string, byID map[string]*exportNode) {
	class, style, body := "layer "+node.Type, geometry, ""
	switch node.Type {
	case "text":
		style += ";color:" + color(node.Fill, "#172554")
		body = html.EscapeString(node.Text)
	case "image":
		if exportImage.MatchString(node.Src) {
			body = "<img src=\"" + html.EscapeString(node.Src) + "\" alt=\"" + html.EscapeString(node.Name) + "\">"
		} else {
			class += " placeholder"
			body = "Image not included"
			if node.Src == "" {
				body = "No image"
			}
		}
	default:
		style += ";background:" + color(node.Fill, "#ffffff")
	}
	tag, attrs := "div", ""
	if node.LinkTo != "" {
		if anchor, ok := anchors[node.LinkTo]; ok {
			target := byID[node.LinkTo]
			tag, class = "a", class+" hotspot"
			attrs = " href=\"#" + anchor + "\" aria-label=\"" + html.EscapeString(short(label(node.Name, label(node.Text, node.Type)))+": open "+short(label(target.Name, "Screen"))) + "\""
		} else {
			class += " broken-link"
			attrs = " title=\"Linked screen was deleted\""
		}
	}
	fmt.Fprintf(b, "<%s class=\"%s\" data-node-id=\"%s\"%s style=\"%s\">%s</%s>\n", tag, class, html.EscapeString(node.ID), attrs, style, body, tag)
}

// Order follows the document's order array, then any remaining nodes in
// engine order, without duplicates — the same order the browser draws.
func exportOrderedNodes(in ExportInput) ([]*exportNode, error) {
	parsed := make(map[string]*exportNode, len(in.Nodes))
	var engineOrder []string
	for _, raw := range in.Nodes {
		node := &exportNode{}
		if err := json.Unmarshal(raw, node); err != nil || node.ID == "" {
			return nil, ErrInvalid
		}
		if _, dup := parsed[node.ID]; dup {
			return nil, ErrInvalid
		}
		parsed[node.ID] = node
		engineOrder = append(engineOrder, node.ID)
	}
	seen := make(map[string]bool, len(parsed))
	out := make([]*exportNode, 0, len(parsed))
	for _, list := range [][]string{in.Order, engineOrder} {
		for _, id := range list {
			if node := parsed[id]; node != nil && !seen[id] {
				seen[id] = true
				out = append(out, node)
			}
		}
	}
	return out, nil
}

func color(value, fallback string) string {
	if exportColor.MatchString(value) {
		return value
	}
	return fallback
}

func px(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }

func atLeastOne(value float64) float64 {
	if value < 1 {
		return 1
	}
	return value
}

func label(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// short bounds accessible names; the full text stays in the layer itself.
func short(value string) string {
	runes := []rune(value)
	if len(runes) > 120 {
		return string(runes[:120]) + "…"
	}
	return value
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
