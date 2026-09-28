package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestCrewFileAccessGeneratedContracts(t *testing.T) {
	raw, err := os.ReadFile("../../internal/api/openapi.gen.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err = json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	paths := spec["paths"].(map[string]any)
	put := paths["/api/v1/crew-connections/{connectionId}/file-access"].(map[string]any)["put"].(map[string]any)
	if put["requestBody"].(map[string]any)["required"] != true {
		t.Fatal("PUT body is optional")
	}
	params := paths["/api/v1/crew-connections/{connectionId}"].(map[string]any)["delete"].(map[string]any)["parameters"].([]any)
	count := 0
	for _, p := range params {
		param := p.(map[string]any)
		if param["name"] != "expected_version" {
			continue
		}
		count++
		schema := param["schema"].(map[string]any)
		if schema["type"] != "integer" || schema["minimum"] != float64(1) {
			t.Fatalf("bad version contract %#v", schema)
		}
	}
	if count != 1 {
		t.Fatalf("version parameters=%d", count)
	}
}
