package main

import (
	"encoding/json"
	"os"
	"strings"
)

// Reuse the frozen A1 structural schemas instead of maintaining another DTO.
// Only a registered source route references these OpenAPI components.
func chatDecisionSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	var raw []byte
	var err error
	for _, path := range []string{"internal/api/testdata/chat-e2/schema.json", "../../internal/api/testdata/chat-e2/schema.json"} {
		raw, err = os.ReadFile(path)
		if err == nil {
			break
		}
	}
	if err != nil {
		panic(err)
	}
	var bundle map[string]json.RawMessage
	if err = json.Unmarshal(raw, &bundle); err != nil {
		panic(err)
	}
	components := map[string]any{}
	load := func(name string, raw json.RawMessage) map[string]any {
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			panic(err)
		}
		if defs, ok := schema["$defs"].(map[string]any); ok {
			for key, value := range defs {
				components[name+key] = openAPIChatSchema(value, name)
			}
		}
		return openAPIChatSchema(schema, name).(map[string]any)
	}
	cards := load("ChatDecision", bundle["cards"])
	var decision map[string]any
	for _, candidate := range cards["oneOf"].([]any) {
		item := candidate.(map[string]any)
		props := item["properties"].(map[string]any)
		if _, ok := props["decision"]; ok {
			decision = item
			break
		}
	}
	if decision == nil {
		panic("A1 decision schema missing")
	}
	components["ChatDecisionReference"] = decision
	page := load("ChatDecisionPage", bundle["page"])
	page["properties"].(map[string]any)["refs"] = map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"$ref": "#/components/schemas/ChatDecisionReference"}}
	components["ChatDecisionReferencePage"] = page
	var commands map[string]json.RawMessage
	if err = json.Unmarshal(bundle["commands"], &commands); err != nil {
		panic(err)
	}
	components["ChatDecisionAnswer"] = load("ChatDecisionAnswer", commands["decision_answer"])
	conflict := load("ChatDecisionConflict", bundle["conflict"])
	conflict["properties"].(map[string]any)["error"] = map[string]any{"type": "string", "enum": []string{"decision_version_conflict"}}
	conflict["properties"].(map[string]any)["current"] = map[string]any{"$ref": "#/components/schemas/ChatDecisionReference"}
	components["ChatDecisionVersionConflict"] = conflict
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	routes := map[string]DomainSchema{
		"GET /api/v1/chats/{chatId}/decisions":                                 {Response: ref("ChatDecisionReferencePage"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "404", "503"}},
		"GET /api/v1/chats/{chatId}/decisions/{sourceType}/{sourceId}":         {Response: ref("ChatDecisionReference"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"404", "500", "503"}},
		"POST /api/v1/chats/{chatId}/decisions/{sourceType}/{sourceId}/answer": {Request: ref("ChatDecisionAnswer"), RequestRequired: true, Response: ref("ChatDecisionReference"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "403", "404", "409", "500"}, Parameters: []map[string]any{{"name": "Idempotency-Key", "in": "header", "required": true, "schema": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}, "description": "Stable key scoped to this source and authenticated human. Reuse with different intent returns409 without a receipt."}}},
	}
	for _, key := range []string{"GET /api/v1/chats/{chatId}/decisions/{sourceType}/{sourceId}", "POST /api/v1/chats/{chatId}/decisions/{sourceType}/{sourceId}/answer"} {
		route := routes[key]
		route.Parameters = append(route.Parameters, map[string]any{"name": "sourceType", "in": "path", "required": true, "schema": map[string]any{"type": "string", "enum": []string{"escalation", "waitpoint", "run_approval"}}, "description": "Exact authoritative source scope; workflow and whole-run decisions are not interchangeable."})
		routes[key] = route
	}
	answer := routes["POST /api/v1/chats/{chatId}/decisions/{sourceType}/{sourceId}/answer"]
	answer.ErrorMedia = []string{"application/json"}
	answer.ErrorResponse = map[string]any{"anyOf": []any{errorSchema(), ref("ChatDecisionVersionConflict")}}
	routes["POST /api/v1/chats/{chatId}/decisions/{sourceType}/{sourceId}/answer"] = answer
	// Narrow the generated union to the actual decision route and retain only its
	// reachable definitions; proposal-only definitions belong to A2's catalog.
	used := map[string]bool{}
	var visit func(any)
	visit = func(value any) {
		switch v := value.(type) {
		case []any:
			for _, item := range v {
				visit(item)
			}
		case map[string]any:
			if raw, ok := v["$ref"].(string); ok && strings.HasPrefix(raw, "#/components/schemas/") {
				name := strings.TrimPrefix(raw, "#/components/schemas/")
				if !used[name] {
					used[name] = true
					visit(components[name])
				}
			}
			for _, item := range v {
				visit(item)
			}
		}
	}
	for _, route := range routes {
		visit(route.Request)
		visit(route.Response)
		visit(route.ErrorResponse)
	}
	for name := range components {
		if !used[name] {
			delete(components, name)
		}
	}
	return routes, components
}

func openAPIChatSchema(value any, namespace string) any {
	switch v := value.(type) {
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, openAPIChatSchema(item, namespace))
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for key, item := range v {
			switch key {
			case "$schema", "$defs":
				continue
			case "$ref":
				out[key] = strings.Replace(item.(string), "#/$defs/", "#/components/schemas/"+namespace, 1)
			case "const":
				out["enum"] = []any{item}
			default:
				out[key] = openAPIChatSchema(item, namespace)
			}
		}
		if options, ok := out["anyOf"].([]any); ok {
			rest := []any{}
			for _, option := range options {
				if object, ok := option.(map[string]any); ok && object["type"] == "null" {
					out["nullable"] = true
				} else {
					rest = append(rest, option)
				}
			}
			if len(rest) == 1 {
				delete(out, "anyOf")
				for key, value := range rest[0].(map[string]any) {
					out[key] = value
				}
			} else {
				out["anyOf"] = rest
			}
		}
		return out
	default:
		return value
	}
}
