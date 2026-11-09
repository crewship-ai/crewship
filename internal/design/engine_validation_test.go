package design

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestEngineRejectsMalformedNodeFieldsWithoutChangingState(t *testing.T) {
	base, err := Run(t.Context(), Request{Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"safe","type":"frame","name":"Safe screen","x":0,"y":0,"width":300,"height":600,"fill":"#ffffff"}}]`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"id", "type", "name", "parentId", "fill", "text", "src", "linkTo"} {
		t.Run(field, func(t *testing.T) {
			node := map[string]any{"id": "safe", field: map[string]any{"unsafe": "object"}}
			raw, _ := json.Marshal([]any{map[string]any{"op": "upsert", "node": node}})
			result, err := Run(t.Context(), Request{State: base.State, Operations: raw})
			if !errors.Is(err, ErrInvalid) || len(result.State) != 0 {
				t.Fatalf("malformed %s escaped validation: error %v state %d", field, err, len(result.State))
			}
			recovered, err := Run(t.Context(), Request{State: base.State})
			if err != nil || len(recovered.Nodes) != 1 || string(recovered.Nodes[0]) != string(base.Nodes[0]) {
				t.Fatalf("rejected %s mutation changed the original state: %s (%v)", field, recovered.Nodes, err)
			}
		})
	}
	for _, node := range []map[string]any{
		{"id": "safe", "unexpected": "unsafe"},
		{"id": "safe", "name": nil},
		{"id": "safe", "fill": []string{"#ffffff"}},
		{"id": "safe", "parentId": strings.Repeat("x", 129)},
		{"id": "safe", "name": strings.Repeat("x", 2001)},
		{"id": "safe", "fill": strings.Repeat("x", 257)},
		{"id": "safe", "linkTo": strings.Repeat("x", 129)},
		{"id": "safe", "linkTo": 7},
	} {
		raw, _ := json.Marshal([]any{map[string]any{"op": "upsert", "node": node}})
		if _, err := Run(t.Context(), Request{State: base.State, Operations: raw}); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted invalid bounded fixed-field node %s: %v", raw, err)
		}
	}
	// Optional labels and paint fields remain optional for existing API callers.
	if _, err := Run(t.Context(), Request{Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"unnamed","type":"rect","x":0,"y":0,"width":10,"height":10}}]`)}); err != nil {
		t.Fatalf("optional string fields became required: %v", err)
	}
}

// A standard Yjs update produced by Yjs 13 with synthetic client ID1337 and a
// frame node whose name is an object. Keep this raw fixture independent of the
// validation engine so it also covers arbitrary browser sync and restore input.
func TestEngineRejectsMalformedFieldInYjsSyncAndRestore(t *testing.T) {
	const stateBase64 = "AQm5CgAnAQVub2RlcwZ1bnNhZmUBKAC5CgACaWQBdwZ1bnNhZmUoALkKAAR0eXBlAXcFZnJhbWUoALkKAARuYW1lAXYBBnVuc2FmZXcGb2JqZWN0KAC5CgABeAF9ACgAuQoAAXkBfQAoALkKAAV3aWR0aAF9rAQoALkKAAZoZWlnaHQBfZgJCAEFb3JkZXIBdwZ1bnNhZmUA"
	const frameBase64 = "AAGZAQEJuQoAJwEFbm9kZXMGdW5zYWZlASgAuQoAAmlkAXcGdW5zYWZlKAC5CgAEdHlwZQF3BWZyYW1lKAC5CgAEbmFtZQF2AQZ1bnNhZmV3Bm9iamVjdCgAuQoAAXgBfQAoALkKAAF5AX0AKAC5CgAFd2lkdGgBfawEKAC5CgAGaGVpZ2h0AX2YCQgBBW9yZGVyAXcGdW5zYWZlAA=="
	state, err := base64.StdEncoding.DecodeString(stateBase64)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := base64.StdEncoding.DecodeString(frameBase64)
	if err != nil {
		t.Fatal(err)
	}
	for name, request := range map[string]Request{"sync": {Frame: frame}, "restore": {Replace: state}, "state": {State: state}} {
		t.Run(name, func(t *testing.T) {
			result, err := Run(t.Context(), request)
			if !errors.Is(err, ErrInvalid) || len(result.State) != 0 {
				t.Fatalf("malformed Yjs %s escaped validation: %v state %d", name, err, len(result.State))
			}
		})
	}
}

// A prototype link is an optional screen ID on a layer (#4449). It is stored
// as a plain bounded string; an empty string means "no link" for REST and
// agent callers, which cannot send null. A dangling target stays valid data:
// the screen may be deleted later and playback treats the hotspot as inert.
func TestEngineAcceptsPrototypeLinksAsBoundedScreenIDs(t *testing.T) {
	result, err := Run(t.Context(), Request{Operations: json.RawMessage(`[
		{"op":"upsert","node":{"id":"a","type":"frame","x":0,"y":0,"width":360,"height":640}},
		{"op":"upsert","node":{"id":"b","type":"frame","x":400,"y":0,"width":360,"height":640}},
		{"op":"upsert","node":{"id":"next","type":"rect","parentId":"a","x":24,"y":24,"width":120,"height":48,"linkTo":"b"}},
		{"op":"upsert","node":{"id":"dangling","type":"rect","parentId":"a","x":24,"y":96,"width":120,"height":48,"linkTo":"deleted-screen"}}
	]`)})
	if err != nil {
		t.Fatalf("prototype link refused: %v", err)
	}
	var next map[string]any
	if err = json.Unmarshal(result.Nodes[2], &next); err != nil || next["linkTo"] != "b" {
		t.Fatalf("link not returned to readers: %s (%v)", result.Nodes[2], err)
	}
	cleared, err := Run(t.Context(), Request{State: result.State, Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"next","linkTo":""}}]`)})
	if err != nil {
		t.Fatalf("clearing a link refused: %v", err)
	}
	if err = json.Unmarshal(cleared.Nodes[2], &next); err != nil || next["linkTo"] != "" {
		t.Fatalf("link not cleared: %s (%v)", cleared.Nodes[2], err)
	}
}

// Text style (#5161) is a set of optional, closed node fields: absent means
// the original 20 px sans, no wrap, no clip, so older documents keep their
// look. Each field takes only its listed values, so nothing reaches a style
// attribute or the canvas that the renderers do not know.
func TestEngineAcceptsBoundedTextStyleFields(t *testing.T) {
	styled := `{"id":"title","type":"text","x":0,"y":0,"width":320,"height":120,"text":"Hello","fontFamily":"serif","fontWeight":700,"fontSize":48,"lineHeight":1.1,"textAlign":"center","textWrap":"wrap","overflow":"clip"}`
	result, err := Run(t.Context(), Request{Operations: json.RawMessage(`[{"op":"upsert","node":` + styled + `}]`)})
	if err != nil {
		t.Fatalf("text style refused: %v", err)
	}
	var got, want map[string]any
	if err = json.Unmarshal(result.Nodes[0], &got); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(styled), &want)
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %v, want %v", key, got[key], value)
		}
	}
	for _, field := range []map[string]any{
		{"fontFamily": "Comic Sans; background:url(x)"},
		{"fontFamily": "Inter"},
		{"fontFamily": 3},
		{"fontWeight": 450},
		{"fontWeight": 1000},
		{"fontWeight": "bold"},
		{"fontSize": 0},
		{"fontSize": 1001},
		{"fontSize": "20px"},
		{"lineHeight": 0.4},
		{"lineHeight": 5.1},
		{"textAlign": "justify"},
		{"textWrap": "balance"},
		{"overflow": "hidden"},
		{"overflow": true},
	} {
		node := map[string]any{"id": "title"}
		for key, value := range field {
			node[key] = value
		}
		raw, _ := json.Marshal([]any{map[string]any{"op": "upsert", "node": node}})
		if _, err := Run(t.Context(), Request{State: result.State, Operations: raw}); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted invalid text style %s: %v", raw, err)
		}
	}
}

// Hide and lock (#5218) are optional boolean node fields: absent means shown
// and unlocked, the state of every older document. Only true and false pass.
func TestEngineAcceptsHiddenAndLockedFlags(t *testing.T) {
	flagged := `{"id":"logo","type":"rect","x":0,"y":0,"width":10,"height":10,"hidden":true,"locked":false}`
	result, err := Run(t.Context(), Request{Operations: json.RawMessage(`[{"op":"upsert","node":` + flagged + `}]`)})
	if err != nil {
		t.Fatalf("hide/lock refused: %v", err)
	}
	var got map[string]any
	if err = json.Unmarshal(result.Nodes[0], &got); err != nil {
		t.Fatal(err)
	}
	if got["hidden"] != true || got["locked"] != false {
		t.Fatalf("flags not stored: %v", got)
	}
	for _, field := range []map[string]any{{"hidden": "true"}, {"hidden": 1}, {"locked": "yes"}, {"locked": nil}, {"locked": map[string]any{}}} {
		node := map[string]any{"id": "logo"}
		for key, value := range field {
			node[key] = value
		}
		raw, _ := json.Marshal([]any{map[string]any{"op": "upsert", "node": node}})
		if _, err := Run(t.Context(), Request{State: result.State, Operations: raw}); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted invalid flag %s: %v", raw, err)
		}
	}
}

// Shape style (#5219): optional corner radius, stroke colour and stroke width.
// Absent means the look every older document has (rectangles 6 px round, no
// stroke). The colour is a closed hex form so nothing else reaches a style.
func TestEngineAcceptsBoundedShapeStyleFields(t *testing.T) {
	styled := `{"id":"card","type":"rect","x":0,"y":0,"width":200,"height":100,"fill":"#ffffff","radius":16,"stroke":"#0f172a","strokeWidth":2}`
	result, err := Run(t.Context(), Request{Operations: json.RawMessage(`[{"op":"upsert","node":` + styled + `}]`)})
	if err != nil {
		t.Fatalf("shape style refused: %v", err)
	}
	var got map[string]any
	if err = json.Unmarshal(result.Nodes[0], &got); err != nil {
		t.Fatal(err)
	}
	if got["radius"] != float64(16) || got["stroke"] != "#0f172a" || got["strokeWidth"] != float64(2) {
		t.Fatalf("shape style not stored: %v", got)
	}
	for _, field := range []map[string]any{
		{"radius": -1}, {"radius": 10001}, {"radius": "8px"},
		{"strokeWidth": -1}, {"strokeWidth": 1001}, {"strokeWidth": "2"},
		{"stroke": "red"}, {"stroke": "#12345"}, {"stroke": "#000;background:url(x)"}, {"stroke": 0},
	} {
		node := map[string]any{"id": "card"}
		for key, value := range field {
			node[key] = value
		}
		raw, _ := json.Marshal([]any{map[string]any{"op": "upsert", "node": node}})
		if _, err := Run(t.Context(), Request{State: result.State, Operations: raw}); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted invalid shape style %s: %v", raw, err)
		}
	}
}
