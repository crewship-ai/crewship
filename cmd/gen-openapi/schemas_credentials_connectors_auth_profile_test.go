package main

import "testing"

func TestCredentialsConnectorsAuthProfileCatalogHasConcreteContracts(t *testing.T) {
	routes, components := credentialsConnectorsAuthProfileSchemaCatalog()
	for _, domain := range routes {
		for key, contract := range domain {
			if contract.Response == nil && contract.Request == nil {
				t.Fatalf("%s has neither request nor response schema", key)
			}
			for label, schema := range map[string]map[string]any{"request": contract.Request, "response": contract.Response} {
				if schema == nil {
					continue
				}
				if schema["type"] == "object" && len(schema) == 1 {
					t.Fatalf("%s %s is generic object", key, label)
				}
			}
		}
	}
	for _, name := range []string{"Connector", "Integration", "CLIToken", "Profile", "PasswordChangeRequest"} {
		if _, ok := components[name]; !ok {
			t.Errorf("missing component %q", name)
		}
	}
}

func TestCredentialsConnectorsAuthProfileCatalogProtectsCredentialSecrets(t *testing.T) {
	routes, components := credentialsConnectorsAuthProfileSchemaCatalog()
	credential := routes["credentials"]["GET /api/v1/credentials/{credentialId}"].Response["$ref"]
	if credential != "#/components/schemas/Credential" {
		t.Fatalf("credential response must use existing secret-safe component, got %v", credential)
	}
	create := components["ConnectorInstallRequest"].(map[string]any)["properties"].(map[string]any)
	if create["fields"] == nil {
		t.Fatal("connector install must describe submitted fields")
	}
}

func TestCredentialsConnectorsAuthProfileCatalogIsUsedByDocumentBuilder(t *testing.T) {
	routes := []route{
		{method: "POST", path: "/api/v1/connectors/{connectorId}/install"},
		{method: "PATCH", path: "/api/v1/users/me"},
		{method: "GET", path: "/api/v1/auth/cli-tokens"},
	}
	doc := buildDocument(routes)
	paths := doc["paths"].(map[string]any)
	install := paths["/api/v1/connectors/{connectorId}/install"].(map[string]any)["post"].(map[string]any)
	body := install["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"]
	if body.(map[string]any)["$ref"] != "#/components/schemas/ConnectorInstallRequest" {
		t.Fatalf("connector install request schema = %#v", body)
	}
	profile := paths["/api/v1/users/me"].(map[string]any)["patch"].(map[string]any)
	profileBody := profile["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"]
	if profileBody.(map[string]any)["$ref"] != "#/components/schemas/ProfileUpdateRequest" {
		t.Fatalf("profile request schema = %#v", profileBody)
	}
	cli := paths["/api/v1/auth/cli-tokens"].(map[string]any)["get"].(map[string]any)
	response := cli["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"]
	if response.(map[string]any)["$ref"] != "#/components/schemas/CLITokenList" {
		t.Fatalf("CLI token response schema = %#v", response)
	}
}
