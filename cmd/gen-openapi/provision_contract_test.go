package main

import (
	"reflect"
	"sort"
	"testing"
)

func TestProvisionContract(t *testing.T) {
	doc := loadSpec(t)
	components := doc["components"].(map[string]any)["schemas"].(map[string]any)
	status := components["RemainingCrewProvisionStatusV1"].(map[string]any)
	props := status["properties"].(map[string]any)
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
		triggered := components[name].(map[string]any)
		properties := triggered["properties"].(map[string]any)
		if len(properties) != 2 || properties["status"] == nil || properties["message"] == nil {
			t.Errorf("%s must describe only status and message: %v", name, properties)
		}
	}
	for _, path := range []string{"/api/v1/crews/{crewId}/provision", "/api/v1/crews/{crewId}/rebuild"} {
		t.Run(path, func(t *testing.T) {
			responses := doc["paths"].(map[string]any)[path].(map[string]any)["post"].(map[string]any)["responses"].(map[string]any)
			for code := range responses {
				if code[0] == '2' && code != "202" {
					t.Errorf("unexpected success %s", code)
				}
			}
			for _, code := range []string{"202", "404", "409", "429", "503"} {
				if responses[code] == nil {
					t.Errorf("missing response %s", code)
				}
			}
		})
	}
}
