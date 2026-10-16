package main

// The public semantic API uses one bounded typed authority. No image, HTML or
// executable payload is admitted by schema1. These contracts feed CLI/MCP.
func slideSchemaComponents() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func(min, max int) map[string]any {
		if max == 9007199254740991 {
			return map[string]any{"type": "integer", "minimum": min, "format": "int64"}
		}
		return map[string]any{"type": "integer", "minimum": min, "maximum": max}
	}
	arr := func(item map[string]any, min, max int) map[string]any {
		return map[string]any{"type": "array", "items": item, "minItems": min, "maxItems": max}
	}
	obj := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
	}
	id := func() map[string]any { return map[string]any{"type": "string", "pattern": "^[a-zA-Z0-9_-]{1,128}$"} }
	hash := func() map[string]any { return map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"} }
	text := func(max int) map[string]any {
		return map[string]any{"type": "string", "maxLength": max, "description": "UTF-8 byte bound also enforced; LF/tab allowed, CR and other controls rejected."}
	}
	font := func() map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "At most128 UTF-8 bytes; no controls or LF/tab."}
	}
	color := func() map[string]any { return map[string]any{"type": "string", "pattern": "^#[a-fA-F0-9]{6}$"} }
	enum := func(values ...string) map[string]any { return map[string]any{"type": "string", "enum": values} }
	textElement := obj(map[string]any{"value": text(32768), "font_family": font(), "font_size": integer(1, 160), "color": color(), "align": enum("left", "center", "right")}, "value", "font_family", "font_size", "color", "align")
	shape := obj(map[string]any{"kind": enum("rectangle", "ellipse"), "fill": color(), "stroke": color(), "stroke_width": integer(0, 16)}, "kind", "fill", "stroke", "stroke_width")
	table := obj(map[string]any{"cells": arr(arr(text(4096), 1, 20), 1, 30), "font_family": font(), "font_size": integer(1, 160), "color": color(), "fill": color(), "border": color()}, "cells", "font_family", "font_size", "color", "fill", "border")
	table["description"] = "Rectangular cells; aggregate across ALL tables (including empty cells) at most2000/slide and10000/deck."
	var variants []any
	for _, kind := range []string{"text", "shape", "table"} {
		props := map[string]any{"id": id(), "type": enum(kind), "x": integer(0, 4096), "y": integer(0, 4096), "width": integer(1, 4096), "height": integer(1, 4096)}
		props[kind] = map[string]map[string]any{"text": textElement, "shape": shape, "table": table}[kind]
		variants = append(variants, obj(props, "id", "type", "x", "y", "width", "height", kind))
	}
	element := map[string]any{"oneOf": variants, "description": "Integer geometry must fit the deck frame; each element admits exactly one matching payload. IDs unique across slides/elements."}
	slide := obj(map[string]any{"id": id(), "notes": text(16384), "elements": arr(ref("SlideElement"), 0, 1000)}, "id", "notes", "elements")
	theme := obj(map[string]any{"background": color(), "color": color(), "font_family": font(), "font_size": integer(1, 160)}, "background", "color", "font_family", "font_size")
	deck := obj(map[string]any{"schema_version": integer(1, 1), "deck_id": id(), "width": integer(64, 4096), "height": integer(64, 4096), "theme": theme, "slide_order": arr(id(), 1, 100), "slides": arr(ref("Slide"), 1, 100)}, "schema_version", "deck_id", "width", "height", "theme", "slide_order", "slides")
	deck["description"] = "At most2MiB canonical UTF-8 JSON and1000 total elements. slides array sorted by ID in canonical encoding; slide_order owns presentation order and must contain every slide exactly once. deck_id equals immutable Page ID."
	state := obj(map[string]any{"schema_version": integer(1, 1), "revision": integer(1, 9007199254740991), "order_version": integer(1, 9007199254740991), "content_hash": hash(), "deck": ref("SlideDeck")}, "schema_version", "revision", "order_version", "content_hash", "deck")
	receipt := obj(map[string]any{"mutation_id": id(), "revision": integer(1, 9007199254740991), "order_version": integer(1, 9007199254740991), "content_hash": hash(), "duplicate": map[string]any{"type": "boolean"}}, "mutation_id", "revision", "order_version", "content_hash", "duplicate")
	revision := obj(map[string]any{"revision": integer(1, 9007199254740991), "parent_revision": integer(0, 9007199254740991), "order_version": integer(1, 9007199254740991), "schema_version": integer(1, 1), "content_hash": hash(), "actor": str(), "kind": enum("initialize", "patch", "reorder", "restore"), "mutation_id": id(), "created_at": str()}, "revision", "parent_revision", "order_version", "schema_version", "content_hash", "actor", "kind", "mutation_id", "created_at")
	outlineElement := obj(map[string]any{"id": id(), "type": enum("text", "shape", "table"), "hash": hash()}, "id", "type", "hash")
	outlineEntry := obj(map[string]any{"id": id(), "hash": hash(), "elements": arr(outlineElement, 0, 1000)}, "id", "hash", "elements")
	outline := obj(map[string]any{"page_id": id(), "revision": integer(1, 9007199254740991), "order_version": integer(1, 9007199254740991), "content_hash": hash(), "slides": arr(outlineEntry, 1, 100)}, "page_id", "revision", "order_version", "content_hash", "slides")
	var operations []any
	add := func(name string, props map[string]any, required ...string) {
		props["op"] = enum(name)
		operations = append(operations, obj(props, append([]string{"op"}, required...)...))
	}
	add("set_element", map[string]any{"slide_id": id(), "element_id": id(), "if_hash": hash(), "element": ref("SlideElement")}, "slide_id", "element_id", "if_hash", "element")
	add("delete_element", map[string]any{"slide_id": id(), "element_id": id(), "if_hash": hash()}, "slide_id", "element_id", "if_hash")
	add("insert_element", map[string]any{"slide_id": id(), "if_hash": hash(), "element": ref("SlideElement")}, "slide_id", "if_hash", "element")
	add("set_notes", map[string]any{"slide_id": id(), "if_hash": hash(), "notes": text(16384)}, "slide_id", "if_hash", "notes")
	add("insert_slide", map[string]any{"slide": ref("Slide"), "position": integer(0, 100)}, "slide", "position")
	add("delete_slide", map[string]any{"slide_id": id(), "if_hash": hash()}, "slide_id", "if_hash")
	return map[string]any{"SlideElement": element, "Slide": slide, "SlideDeck": deck, "SlideState": state, "SlideReceipt": receipt, "SlideRevision": revision, "SlideOutline": outline, "SlideOperation": map[string]any{"oneOf": operations}}
}
func slideSchemaCatalog() map[string]DomainSchema {
	integer := func(min int) map[string]any {
		return map[string]any{"type": "integer", "minimum": min, "format": "int64"}
	}
	id := func() map[string]any { return map[string]any{"type": "string", "pattern": "^[a-zA-Z0-9_-]{1,128}$"} }
	request := func(props map[string]any, required ...string) map[string]any {
		props["page_id"] = id()
		props["schema_version"] = map[string]any{"type": "integer", "enum": []int{1}}
		props["base_rev"] = integer(1)
		props["mutation_id"] = id()
		return map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": append([]string{"page_id", "schema_version", "base_rev", "mutation_id"}, required...), "description": "Exact case-sensitive keys, no duplicates, one JSON object at most2MiB; immutable Page binding and whole-revision CAS. No automatic rebase."}
	}
	base := "/api/v1/pages/{slug}/slides"
	out := map[string]DomainSchema{}
	get := func(path, name string) {
		out["GET "+base+path] = DomainSchema{Response: ref(name), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "500"}}
	}
	get("", "SlideState")
	get("/outline", "SlideOutline")
	get("/revisions/{revision}", "SlideState")
	out["GET "+base+"/history"] = DomainSchema{Response: map[string]any{"type": "array", "items": ref("SlideRevision"), "maxItems": 1000}, SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"401", "403", "404", "500"}}
	post := func(path string, body map[string]any) {
		out["POST "+base+path] = DomainSchema{RequestRequired: true, Request: body, Response: ref("SlideReceipt"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "429", "500"}}
	}
	initialize := request(map[string]any{"deck": ref("SlideDeck")}, "deck")
	initialize["properties"].(map[string]any)["base_rev"] = map[string]any{"type": "integer", "enum": []int{0}}
	post("/initialize", initialize)
	patch := request(map[string]any{"operations": map[string]any{"type": "array", "minItems": 1, "maxItems": 100, "items": ref("SlideOperation")}, "if_order_version": integer(1)}, "operations")
	patch["description"] = "Progressive candidate semantics: each if_hash sees earlier operations in this atomic batch. if_hash is element hash for set/delete_element, full slide hash for insert_element/set_notes/delete_slide. insert/delete_slide also require if_order_version. Aggregate final deck limits enforced."
	post("/patch", patch)
	post("/reorder", request(map[string]any{"if_order_version": integer(1), "slide_order": map[string]any{"type": "array", "minItems": 1, "maxItems": 100, "items": id()}}, "if_order_version", "slide_order"))
	post("/restore", request(map[string]any{"if_order_version": integer(1), "revision": integer(1)}, "if_order_version", "revision"))
	return out
}
