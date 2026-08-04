package main

import (
	"encoding/json"
	"testing"
)

func TestResponseSchemaQuality(t *testing.T) {
	response := func(schema string) openAPIResponse {
		var parsed map[string]json.RawMessage
		if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
			t.Fatal(err)
		}
		return openAPIResponse{Content: map[string]openAPIMediaType{
			"application/json": {Schema: parsed},
		}}
	}

	tests := []struct {
		name     string
		response openAPIResponse
		concrete bool
	}{
		{name: "generic object", response: response(`{"type":"object"}`), concrete: false},
		{name: "array", response: response(`{"type":"array","items":{"type":"string"}}`), concrete: true},
		{name: "object properties", response: response(`{"type":"object","properties":{"id":{"type":"string"}}}`), concrete: true},
		{name: "component reference", response: response(`{"$ref":"#/components/schemas/Workspace"}`), concrete: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			responses := map[string]openAPIResponse{"200": tt.response}
			if got := hasConcreteSuccessSchema(responses); got != tt.concrete {
				t.Fatalf("hasConcreteSuccessSchema() = %v, want %v", got, tt.concrete)
			}
			if got := hasSuccessSchema(responses); !got {
				t.Fatal("hasSuccessSchema() = false, want true")
			}
		})
	}
}

func TestResponseSchemaQualityIgnoresErrors(t *testing.T) {
	responses := map[string]openAPIResponse{
		"400": {Content: map[string]openAPIMediaType{"application/json": {Schema: map[string]json.RawMessage{"type": json.RawMessage(`"object"`)}}}},
	}
	if hasSuccessSchema(responses) || hasConcreteSuccessSchema(responses) {
		t.Fatal("error responses must not count as success response schemas")
	}
}
