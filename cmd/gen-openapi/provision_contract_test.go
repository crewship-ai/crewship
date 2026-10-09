package main

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestProvisionContract(t *testing.T) {
	doc := loadSpec(t)
	components := provisionContractObject(t, provisionContractObject(t, doc, "components"), "schemas")
	status := provisionContractObject(t, components, "RemainingCrewProvisionStatusV1")
	props := provisionContractObject(t, status, "properties")
	wantRequired := []string{"agents_pending_restart", "cached_image", "config_hash", "devcontainer_config", "devcontainer_config_defaulted", "status", "toolchain"}
	var required []string
	values, _ := status["required"].([]any)
	for _, v := range values {
		required = append(required, v.(string))
	}
	sort.Strings(required)
	if !reflect.DeepEqual(required, wantRequired) {
		t.Errorf("required = %v, want %v", required, wantRequired)
	}
	for _, field := range append(wantRequired, "resolved_features", "step", "total", "message", "steps", "log_tail", "started_at", "completed_at", "error") {
		if props[field] == nil {
			t.Errorf("missing property %s", field)
		}
	}
	for _, field := range []string{"crew_id", "phase", "updated_at"} {
		if props[field] != nil {
			t.Errorf("handler never returns %s", field)
		}
	}
	for _, name := range []string{"RemainingCrewProvisionTriggeredV1", "RemainingCrewRebuildTriggeredV1"} {
		triggered := provisionContractObject(t, components, name)
		properties := provisionContractObject(t, triggered, "properties")
		if len(properties) != 2 || properties["status"] == nil || properties["message"] == nil {
			t.Errorf("%s must describe only status and message: %v", name, properties)
		}
	}
	for _, path := range []string{"/api/v1/crews/{crewId}/provision", "/api/v1/crews/{crewId}/rebuild"} {
		t.Run(path, func(t *testing.T) {
			paths := provisionContractObject(t, doc, "paths")
			operation := provisionContractObject(t, provisionContractObject(t, paths, path), "post")
			responses := provisionContractObject(t, operation, "responses")
			for code := range responses {
				if strings.HasPrefix(code, "2") && code != "202" {
					t.Errorf("unexpected success %s", code)
				}
			}
			for _, code := range []string{"202", "400", "401", "403", "404", "409", "429", "500", "503"} {
				if responses[code] == nil {
					t.Errorf("missing response %s", code)
				} else if code != "202" {
					response := provisionContractObject(t, responses, code)
					content := provisionContractObject(t, response, "content")
					media := provisionContractObject(t, content, "application/json")
					schema := provisionContractObject(t, media, "schema")
					if schema["anyOf"] == nil || schema["oneOf"] != nil {
						t.Errorf("%s error union must permit overlapping extension fields", code)
					}
				}
			}
		})
	}
}

// Report malformed generated objects as contract failures rather than panics.
func provisionContractObject(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	object, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%s: want schema object, got %T (%v)", key, parent[key], parent[key])
	}
	return object
}
