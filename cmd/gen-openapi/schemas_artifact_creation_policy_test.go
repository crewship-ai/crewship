package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestArtifactCreationPolicyContractDocumentsCeilingAndCAS(t *testing.T) {
	for _, method := range []string{"GET", "PUT"} {
		t.Run(method, func(t *testing.T) {
			rt := routeFromKey(method + " /api/v1/workspaces/{workspaceId}/artifact-creation-policy")
			doc := buildDocument([]route{rt})
			operation := doc["paths"].(map[string]any)[openAPIPath(rt.path)].(map[string]any)[strings.ToLower(method)].(map[string]any)
			response := operation["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
			if response["$ref"] != "#/components/schemas/WorkspaceArtifactCreationPolicyResponse" {
				t.Fatalf("administrative policy lacks its named wire contract: %v", response)
			}
			if method == "PUT" {
				body := operation["requestBody"].(map[string]any)
				schema := body["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
				if body["required"] != true || schema["$ref"] != "#/components/schemas/WorkspaceArtifactCreationPolicyRequest" {
					t.Fatal("policy update lacks explicit compare-and-swap request")
				}
			}
		})
	}
}

func TestArtifactCreationPolicyDocumentsBoundedBodyStatus(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(specBytes(t), &document); err != nil {
		t.Fatal(err)
	}
	responses := document["paths"].(map[string]any)["/api/v1/workspaces/{workspaceId}/artifact-creation-policy"].(map[string]any)["put"].(map[string]any)["responses"].(map[string]any)
	if responses["413"] == nil {
		t.Fatal("64 KiB policy body rejection is missing from the published contract")
	}
}
