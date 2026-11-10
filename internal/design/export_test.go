package design

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"
)

func exportFixture(nodes ...map[string]any) ExportInput {
	in := ExportInput{Name: "Checkout", Version: 7}
	for _, node := range nodes {
		raw, _ := json.Marshal(node)
		in.Nodes = append(in.Nodes, raw)
		in.Order = append(in.Order, node["id"].(string))
	}
	return in
}

func TestRenderHTMLEscapesLayerContent(t *testing.T) {
	in := exportFixture(
		map[string]any{"id": `s"1`, "type": "frame", "name": "<script>alert('screen')</script>", "x": 0, "y": 0, "width": 360, "height": 640, "fill": "#fff"},
		map[string]any{"id": "t", "type": "text", "parentId": `s"1`, "name": "</style><script>alert(1)</script>", "x": 10, "y": 10, "width": 200, "height": 30, "text": `"><img src=x onerror=alert(2)>`, "fill": "red;background:url(https://evil.test/x)"},
		map[string]any{"id": "r", "type": "rect", "parentId": `s"1`, "x": 10, "y": 60, "width": 20, "height": 20, "fill": "expression(alert(3))"},
		map[string]any{"id": "i", "type": "image", "parentId": `s"1`, "name": `" onload="alert(4)`, "x": 0, "y": 0, "width": 10, "height": 10, "src": "https://evil.test/track.png"},
		map[string]any{"id": "j", "type": "image", "parentId": `s"1`, "x": 0, "y": 0, "width": 10, "height": 10, "src": "data:image/svg+xml;base64,PHN2Zz48L3N2Zz4="},
	)
	out, err := RenderHTML(in, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	page := string(out)
	for _, forbidden := range []string{"<script", "<img src=x", "url(", "expression(", "https://evil", `onload="`, "data:image/svg", `id="s"1"`} {
		if strings.Contains(page, forbidden) {
			t.Errorf("export contains %q:\n%s", forbidden, page)
		}
	}
	for _, escaped := range []string{"&lt;script&gt;alert(&#39;screen&#39;)&lt;/script&gt;", "&#34;&gt;&lt;img src=x onerror=alert(2)&gt;", `data-node-id="s&#34;1"`, "color:#172554", "background:#ffffff"} {
		if !strings.Contains(page, escaped) {
			t.Errorf("export lacks %q:\n%s", escaped, page)
		}
	}
	if !strings.Contains(page, `<meta http-equiv="Content-Security-Policy" content="default-src &#39;none&#39;; style-src &#39;unsafe-inline&#39;; img-src data:">`) {
		t.Error("CSP meta missing")
	}
}

func TestRenderHTMLPrototypeLinksAndGeometry(t *testing.T) {
	in := exportFixture(
		map[string]any{"id": "a", "type": "frame", "name": "Cart", "x": 100, "y": 50, "width": 360, "height": 640, "fill": "#ffffff"},
		map[string]any{"id": "b", "type": "frame", "name": "Pay", "x": 600, "y": 50, "width": 360, "height": 640},
		map[string]any{"id": "group", "type": "rect", "parentId": "a", "x": 10, "y": 20, "width": 200, "height": 100, "fill": "#eeeeee"},
		map[string]any{"id": "go", "type": "rect", "parentId": "group", "name": "Checkout", "x": 5, "y": 6, "width": 120.5, "height": 48, "fill": "#2563eb", "linkTo": "b"},
		map[string]any{"id": "gone", "type": "text", "parentId": "b", "x": 0, "y": 0, "width": 10, "height": 10, "text": "Back", "linkTo": "deleted-screen"},
		map[string]any{"id": "loose", "type": "rect", "x": 0, "y": 0, "width": 10, "height": 10, "linkTo": "a"},
		map[string]any{"id": "pic", "type": "image", "parentId": "b", "x": 1, "y": 2, "width": 0, "height": 3, "src": "data:image/png;base64,iVBORw0KGgo="},
	)
	out, err := RenderHTML(in, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	page := string(out)
	for _, want := range []string{
		`<section class="screen" id="screen-1" data-node-id="a"`,
		`<section class="screen" id="screen-2" data-node-id="b"`,
		`<li><a href="#screen-1">Cart</a></li><li><a href="#screen-2">Pay</a></li>`,
		`<a class="layer rect hotspot" data-node-id="go" href="#screen-2" aria-label="Checkout: open Pay" style="left:15px;top:26px;width:120.5px;height:48px;background:#2563eb"></a>`,
		`<div class="layer text broken-link" data-node-id="gone" title="Linked screen was deleted"`,
		`<div class="layer image" data-node-id="pic" style="left:1px;top:2px;width:1px;height:3px"><img src="data:image/png;base64,iVBORw0KGgo=" alt=""></div>`,
		"Design version 7 · 2 screens",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("missing %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, `data-node-id="loose"`) {
		t.Error("a layer outside every screen was drawn")
	}
	if strings.Contains(page, "<script") || strings.Contains(page, "http://") || strings.Contains(page, "https://") {
		t.Error("export references script or network")
	}
}

func TestRenderHTMLIsDeterministicAndBounded(t *testing.T) {
	in := exportFixture(
		map[string]any{"id": "a", "type": "frame", "name": "One", "x": 0, "y": 0, "width": 100, "height": 100},
		map[string]any{"id": "b", "type": "image", "parentId": "a", "x": 0, "y": 0, "width": 100, "height": 100, "src": "data:image/png;base64," + strings.Repeat("A", 4096)},
	)
	first, err := RenderHTML(in, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		again, err := RenderHTML(in, 1<<20)
		if err != nil || !bytes.Equal(first, again) {
			t.Fatal("same version rendered to different bytes")
		}
	}
	if _, err := RenderHTML(in, 4096); !errors.Is(err, ErrExportTooLarge) {
		t.Fatalf("over-limit export not refused: %v", err)
	}
	empty, err := RenderHTML(ExportInput{Name: "", Version: 1}, 1<<20)
	if err != nil || !bytes.Contains(empty, []byte("This version has no screens.")) {
		t.Fatalf("empty Design export: %v %s", err, empty)
	}
}

// playerGeometry ports the prototype player's geometry: position() in
// design-prototype-player.tsx (recursive, a revisited node returns its own
// offset) and designScreenOf() in design-document.ts. It is quadratic on deep
// chains, which is why the renderer does not use it.
func playerGeometry(nodes []*exportNode, byID map[string]*exportNode) map[string]exportPlacement {
	var position func(node *exportNode, seen map[string]bool) (float64, float64)
	position = func(node *exportNode, seen map[string]bool) (float64, float64) {
		if seen[node.ID] {
			return node.X, node.Y
		}
		seen[node.ID] = true
		var ox, oy float64
		if parent := byID[node.ParentID]; node.ParentID != "" && parent != nil {
			ox, oy = position(parent, seen)
		}
		return ox + node.X, oy + node.Y
	}
	out := map[string]exportPlacement{}
	for _, node := range nodes {
		var at exportPlacement
		at.x, at.y = position(node, map[string]bool{})
		seen := map[string]bool{}
		for current := node; current != nil && !seen[current.ID]; {
			if current.Type == "frame" {
				at.screen = current.ID
				break
			}
			seen[current.ID] = true
			if current.ParentID == "" {
				break
			}
			current = byID[current.ParentID]
		}
		out[node.ID] = at
	}
	return out
}

func reachesCycle(node *exportNode, byID map[string]*exportNode) bool {
	seen := map[string]bool{}
	for current := node; current != nil && current.ParentID != ""; current = byID[current.ParentID] {
		if seen[current.ID] {
			return true
		}
		seen[current.ID] = true
	}
	return false
}

// The linear geometry must place every layer exactly where the prototype
// player does, including missing parents and parentId cycles with and
// without frames, and with fractional offsets outside cycles.
func TestExportGeometryMatchesPrototypePlayer(t *testing.T) {
	random := rand.New(rand.NewSource(4745))
	types := []string{"frame", "rect", "text", "image"}
	for round := 0; round < 400; round++ {
		count := 1 + random.Intn(24)
		nodes := make([]*exportNode, count)
		byID := map[string]*exportNode{}
		for i := range nodes {
			nodes[i] = &exportNode{ID: fmt.Sprint("n", i), Type: types[random.Intn(len(types))], X: float64(random.Intn(50)) / 10, Y: float64(random.Intn(50)) / 10}
			byID[nodes[i].ID] = nodes[i]
		}
		for _, node := range nodes {
			switch random.Intn(4) {
			case 0: // root
			case 1:
				node.ParentID = "missing"
			default:
				node.ParentID = nodes[random.Intn(count)].ID // may form cycles
			}
		}
		random.Shuffle(len(nodes), func(i, j int) { nodes[i], nodes[j] = nodes[j], nodes[i] })
		want, got := playerGeometry(nodes, byID), exportGeometry(nodes, byID)
		for _, node := range nodes {
			w, g := want[node.ID], got[node.ID]
			if w == g {
				continue
			}
			// Above a cycle the sum is regrouped (the cycle total is shared), so
			// only rounding may differ there; everywhere else it is exact.
			if !reachesCycle(node, byID) || w.screen != g.screen || math.Abs(w.x-g.x) > 1e-9 || math.Abs(w.y-g.y) > 1e-9 {
				t.Fatalf("round %d node %s: got %+v, want %+v", round, node.ID, g, w)
			}
		}
	}
}

// A frame inside a parentId cycle (the engine accepts one) is placed as the
// player places it: F=100 and R=20 make F 220 and R 140, so R is drawn at -80.
func TestExportGeometryCycleWithFrameMatchesPlayer(t *testing.T) {
	f := &exportNode{ID: "F", Type: "frame", ParentID: "R", X: 100}
	r := &exportNode{ID: "R", Type: "rect", ParentID: "F", X: 20}
	byID := map[string]*exportNode{"F": f, "R": r}
	got := exportGeometry([]*exportNode{f, r}, byID)
	if got["F"] != (exportPlacement{x: 220, screen: "F"}) || got["R"] != (exportPlacement{x: 140, screen: "F"}) {
		t.Fatalf("cycle geometry %+v", got)
	}
	// Fractional offsets add up in the player's order (parent first).
	a := &exportNode{ID: "a", Type: "frame", X: 0.3}
	b := &exportNode{ID: "b", Type: "rect", ParentID: "a", X: 0.2}
	c := &exportNode{ID: "c", Type: "rect", ParentID: "b", X: 0.1}
	nodes := []*exportNode{c, b, a}
	byID = map[string]*exportNode{"a": a, "b": b, "c": c}
	want, have := playerGeometry(nodes, byID), exportGeometry(nodes, byID)
	for _, node := range nodes {
		if want[node.ID] != have[node.ID] {
			t.Fatalf("%s: got %+v, want %+v", node.ID, have[node.ID], want[node.ID])
		}
	}
}

// A deep but engine-valid chain of layers must not make rendering quadratic:
// the export runs inside the Design read transaction.
func TestRenderHTMLDeepChainIsLinear(t *testing.T) {
	nodes := []map[string]any{}
	for i := 0; i < 700; i++ {
		nodes = append(nodes, map[string]any{"id": fmt.Sprint("f", i), "type": "frame", "x": 0, "y": 0, "width": 1, "height": 1})
	}
	parent := "f0"
	for i := 0; i < 1400; i++ {
		id := fmt.Sprint("r", i)
		nodes = append(nodes, map[string]any{"id": id, "type": "rect", "parentId": parent, "x": 1, "y": 0, "width": 1, "height": 1})
		parent = id
	}
	started := time.Now()
	out, err := RenderHTML(exportFixture(nodes...), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("deep chain took %s", elapsed)
	}
	if !strings.Contains(string(out), `data-node-id="r1399" style="left:1400px;`) {
		t.Fatal("deepest layer is not placed at the sum of its ancestors")
	}
}

// Text style (#5161) reaches the exported style attribute only as the closed
// values the engine accepts; a layer without style exports exactly as before.
func TestRenderHTMLTextStyle(t *testing.T) {
	screen := map[string]any{"id": "s", "type": "frame", "x": 0, "y": 0, "width": 360, "height": 640}
	plain := map[string]any{"id": "plain", "type": "text", "parentId": "s", "x": 10, "y": 10, "width": 200, "height": 30, "text": "Plain"}
	before, err := RenderHTML(exportFixture(screen, plain), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before), "font-family:") || strings.Contains(string(before), "pre-wrap") {
		t.Fatalf("unstyled text gained style:\n%s", before)
	}
	styled := map[string]any{"id": "title", "type": "text", "parentId": "s", "x": 10, "y": 60, "width": 200, "height": 90, "text": "Title", "fontFamily": "serif", "fontWeight": 700, "fontSize": 48, "lineHeight": 1.1, "textAlign": "center", "textWrap": "wrap", "overflow": "clip"}
	hostile := map[string]any{"id": "bad", "type": "text", "parentId": "s", "x": 10, "y": 200, "width": 200, "height": 30, "text": "Bad", "fontFamily": "x;background:url(https://evil.test)", "fontWeight": 450, "fontSize": -3, "lineHeight": 99, "textAlign": "justify;color:red", "textWrap": "balance", "overflow": "auto"}
	out, err := RenderHTML(exportFixture(screen, plain, styled, hostile), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	page := string(out)
	for _, want := range []string{"font-family:Georgia,'Times New Roman',serif", "font-weight:700", "font-size:48px", "line-height:1.1", "text-align:center", "white-space:pre-wrap;overflow-wrap:anywhere", "overflow:hidden"} {
		if !strings.Contains(page, want) {
			t.Errorf("export lacks %q:\n%s", want, page)
		}
	}
	layerLine := func(page, id string) string {
		for _, line := range strings.Split(page, "\n") {
			if strings.Contains(line, `data-node-id="`+id+`"`) {
				return line
			}
		}
		return ""
	}
	plainLine := func(page string) string { return layerLine(page, "plain") }
	bad := layerLine(page, "bad")
	if bad == "" {
		t.Fatal("hostile layer not exported")
	}
	for _, forbidden := range []string{"evil.test", "font-", "line-height", "text-align", "justify", "balance", "white-space", "overflow"} {
		if strings.Contains(bad, forbidden) || strings.Contains(page, "evil.test") {
			t.Errorf("hostile layer exports %q: %s", forbidden, bad)
		}
	}
	if plainLine(string(before)) == "" || plainLine(string(before)) != plainLine(page) {
		t.Errorf("plain layer changed:\n%s\n%s", plainLine(string(before)), plainLine(page))
	}
}

// A hidden layer (#5218) is left out of the exported page; a locked one is
// exported as usual (locking only guards editing).
func TestRenderHTMLLeavesOutHiddenLayers(t *testing.T) {
	screen := map[string]any{"id": "s", "type": "frame", "x": 0, "y": 0, "width": 360, "height": 640}
	shown := map[string]any{"id": "shown", "type": "text", "parentId": "s", "x": 10, "y": 10, "width": 200, "height": 30, "text": "Shown", "locked": true}
	hidden := map[string]any{"id": "secret", "type": "text", "parentId": "s", "x": 10, "y": 60, "width": 200, "height": 30, "text": "Hidden draft", "hidden": true}
	out, err := RenderHTML(exportFixture(screen, shown, hidden), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	page := string(out)
	if !strings.Contains(page, "Shown") {
		t.Fatalf("a locked layer must be exported:\n%s", page)
	}
	if strings.Contains(page, "Hidden draft") || strings.Contains(page, `data-node-id="secret"`) {
		t.Fatalf("a hidden layer must not be exported:\n%s", page)
	}
}

// Shape style (#5219) reaches the exported page as border radius and an inset
// stroke; a rectangle without it keeps the 6 px corners of the class.
func TestRenderHTMLShapeStyle(t *testing.T) {
	screen := map[string]any{"id": "s", "type": "frame", "x": 0, "y": 0, "width": 360, "height": 640}
	plain := map[string]any{"id": "plain", "type": "rect", "parentId": "s", "x": 10, "y": 10, "width": 100, "height": 40, "fill": "#2563eb"}
	card := map[string]any{"id": "card", "type": "rect", "parentId": "s", "x": 10, "y": 60, "width": 200, "height": 100, "fill": "#ffffff", "radius": 16, "stroke": "#0f172a", "strokeWidth": 2}
	photo := map[string]any{"id": "photo", "type": "image", "parentId": "s", "x": 10, "y": 200, "width": 200, "height": 100, "src": "https://example.test/a.png", "radius": 50}
	// An <img> fills its layer and would cover an inset box-shadow, so the
	// stroke must be drawn above the content, as on the canvas.
	framed := map[string]any{"id": "framed", "type": "image", "parentId": "s", "x": 10, "y": 320, "width": 200, "height": 100, "src": "https://example.test/b.png", "stroke": "#dc2626", "strokeWidth": 3}
	out, err := RenderHTML(exportFixture(screen, plain, card, photo, framed), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	line := func(id string) string {
		for _, l := range strings.Split(string(out), "\n") {
			if strings.Contains(l, `data-node-id="`+id+`"`) {
				return l
			}
		}
		return ""
	}
	if strings.Contains(line("plain"), "border-radius") || strings.Contains(line("plain"), "outline") {
		t.Fatalf("an unstyled rectangle gained style: %s", line("plain"))
	}
	for _, want := range []string{"border-radius:16px", "outline:2px solid #0f172a", "outline-offset:-2px"} {
		if !strings.Contains(line("card"), want) {
			t.Errorf("card lacks %q: %s", want, line("card"))
		}
	}
	if !strings.Contains(line("photo"), "border-radius:50px") || !strings.Contains(line("photo"), "overflow:hidden") {
		t.Errorf("rounded image not clipped: %s", line("photo"))
	}
	if l := line("framed"); !strings.Contains(l, "outline:3px solid #dc2626") || !strings.Contains(l, "outline-offset:-3px") || strings.Contains(l, "box-shadow") {
		t.Errorf("image stroke is not drawn above the picture: %s", l)
	}
}

// A stroke is an inline outline, which beats a stylesheet rule on the same
// element: the focus ring of a linked layer and the broken-link warning must
// still win, or a stroked hotspot has no visible keyboard focus (#5219).
func TestRenderHTMLStrokeKeepsFocusAndBrokenLinkOutline(t *testing.T) {
	a := map[string]any{"id": "a", "type": "frame", "x": 0, "y": 0, "width": 360, "height": 640}
	b := map[string]any{"id": "b", "type": "frame", "x": 400, "y": 0, "width": 360, "height": 640}
	cta := map[string]any{"id": "cta", "type": "rect", "parentId": "a", "x": 10, "y": 10, "width": 100, "height": 40, "fill": "#2563eb", "stroke": "#0f172a", "strokeWidth": 2, "linkTo": "b"}
	gone := map[string]any{"id": "gone", "type": "rect", "parentId": "a", "x": 10, "y": 80, "width": 100, "height": 40, "fill": "#2563eb", "stroke": "#0f172a", "linkTo": "missing"}
	out, err := RenderHTML(exportFixture(a, b, cta, gone), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	html := string(out)
	for _, want := range []string{"a.hotspot:focus-visible{outline:2px solid #2563eb!important", ".broken-link{outline:2px dashed #d97706!important"} {
		if !strings.Contains(html, want) {
			t.Errorf("stylesheet lacks %q, so an inline stroke outline hides it", want)
		}
	}
	if !strings.Contains(html, `data-node-id="cta"`) || !strings.Contains(html, "outline:2px solid #0f172a") {
		t.Errorf("the linked layer lost its stroke")
	}
}
