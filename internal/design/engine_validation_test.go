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
