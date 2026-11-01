package design

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
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
