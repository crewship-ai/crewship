package main

// The envelope is versioned here; the bounded native deck profile is validated
// by internal/slides and illustrated by testdata/slides-schema-v1.json.
func slidesHumanSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	obj := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required}
	}
	str := func() map[string]any { return map[string]any{"type": "string"} }
	positive := func() map[string]any { return map[string]any{"type": "integer", "minimum": 1} }
	deck := map[string]any{"type": "object", "description": "Complete schema-1 native Slides deck; validated against the bounded Go profile and canonical fixture."}
	state := obj(map[string]any{
		"schema_version": map[string]any{"type": "integer", "enum": []int{1}},
		"revision":       positive(), "order_version": positive(), "content_hash": str(), "deck": deck,
	}, "schema_version", "revision", "order_version", "content_hash", "deck")
	receipt := obj(map[string]any{
		"mutation_id": str(), "revision": positive(), "order_version": positive(),
		"content_hash": str(), "duplicate": map[string]any{"type": "boolean"},
	}, "mutation_id", "revision", "order_version", "content_hash", "duplicate")
	base := "/api/v1/pages/{slug}/slides"
	routes := map[string]DomainSchema{}
	for _, path := range []string{base, base + "/revisions/{revision}"} {
		routes["GET "+path] = DomainSchema{Response: ref("NativeSlidesState"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "500"}}
	}
	request := obj(map[string]any{
		"page_id": str(), "schema_version": map[string]any{"type": "integer", "enum": []int{1}},
		"base_rev": map[string]any{"type": "integer", "enum": []int{0}}, "mutation_id": str(), "deck": deck,
	}, "page_id", "schema_version", "base_rev", "mutation_id", "deck")
	request["additionalProperties"] = false
	routes["POST "+base+"/initialize"] = DomainSchema{Request: request, RequestRequired: true, Response: ref("NativeSlidesReceipt"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "429", "500"}}
	for key, contract := range routes {
		contract.SuccessHeaders = map[string]any{"Cache-Control": map[string]any{"schema": map[string]any{"type": "string", "enum": []string{"no-store"}}}}
		routes[key] = contract
	}
	return routes, map[string]any{"NativeSlidesState": state, "NativeSlidesReceipt": receipt}
}
