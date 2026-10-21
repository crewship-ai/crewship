package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
)

// Compare audited schema fields and requiredness to the actual handler DTOs,
// guarding against metadata/source leaks and accidental wire-shape drift.
func TestWorkspacePageOwnershipSchemasMatchWireDTOs(t *testing.T) {
	_, schemas := workspacePageOwnershipSchemaCatalog()
	for name, dto := range map[string]any{
		"WorkspacePageOwnershipSuccessor":       api.WorkspacePageOwnershipSuccessor{},
		"WorkspacePageOwnershipSummary":         api.WorkspacePageOwnershipSummary{},
		"WorkspacePageOwnershipListResponse":    api.WorkspacePageOwnershipListResponse{},
		"WorkspacePageOwnershipTransferRequest": api.WorkspacePageOwnershipTransferRequest{},
		"WorkspacePageOwnershipError":           api.WorkspacePageOwnershipError{},
	} {
		t.Run(name, func(t *testing.T) {
			schema := schemas[name].(map[string]any)
			fields := schema["properties"].(map[string]any)
			required := map[string]bool{}
			for _, field := range schema["required"].([]string) {
				required[field] = true
			}
			typ := reflect.TypeOf(dto)
			if len(fields) != typ.NumField() {
				t.Fatalf("schema field count%d wire%d", len(fields), typ.NumField())
			}
			for i := 0; i < typ.NumField(); i++ {
				tag := typ.Field(i).Tag.Get("json")
				field := strings.Split(tag, ",")[0]
				if _, ok := fields[field]; !ok {
					t.Errorf("missing wire field%s", field)
				}
				if required[field] == strings.Contains(tag, "omitempty") {
					t.Errorf("required mismatch%s", tag)
				}
			}
		})
	}
}

func TestWorkspacePageOwnershipOperationsUseAuditedSchemas(t *testing.T) {
	for _, key := range []string{"GET /api/v1/workspaces/{workspaceId}/page-ownership", "POST /api/v1/workspaces/{workspaceId}/page-ownership/{pageID}"} {
		rt := routeFromKey(key)
		doc := buildDocument([]route{rt})
		operation := doc["paths"].(map[string]any)[openAPIPath(rt.path)].(map[string]any)[strings.ToLower(rt.method)].(map[string]any)
		response := operation["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		want := "#/components/schemas/WorkspacePageOwnershipSummary"
		if rt.method == "GET" {
			want = "#/components/schemas/WorkspacePageOwnershipListResponse"
		}
		if response["$ref"] != want {
			t.Fatalf("%s response%v", key, response)
		}
		if rt.method == "POST" {
			body := operation["requestBody"].(map[string]any)
			schema := body["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
			if body["required"] != true || schema["$ref"] != "#/components/schemas/WorkspacePageOwnershipTransferRequest" {
				t.Fatalf("transfer request%v", body)
			}
		} else {
			found := false
			for _, p := range operation["parameters"].([]map[string]any) {
				if p["name"] == "limit" {
					s := p["schema"].(map[string]any)
					if s["minimum"] != 1 || s["maximum"] != 100 || s["default"] != 25 {
						t.Fatalf("pagination%v", s)
					}
					found = true
				}
			}
			if !found {
				t.Fatal("missing bounded pagination")
			}
		}
	}
}

// Custody failures are returned by a shared handler helper, so the route must
// retain its explicit conflict response in the generated public contract.
func TestRemovingWorkspaceMemberDocumentsCustodyConflict(t *testing.T) {
	content := errorContentOf(t, loadSpec(t), "/api/v1/workspaces/{workspaceId}/members/{memberId}", "delete", "409")
	if _, ok := content["application/json"]; !ok {
		t.Fatal("custody conflict must document the JSON error response")
	}
}
