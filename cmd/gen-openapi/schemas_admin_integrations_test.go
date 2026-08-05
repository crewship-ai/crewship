package main

import "testing"

func TestDomainSchemaMapCoversRequestedDomains(t *testing.T) {
	catalog := operationalDomainSchemaCatalog()
	for _, domain := range []string{"admin", "backups", "memory", "notifications", "integrations", "files-media", "auth-public", "system"} {
		if len(catalog[domain]) == 0 {
			t.Errorf("domain %q has no schemas", domain)
		}
	}
}

func TestDomainSchemaMapUsesAccurateMediaAndPayloadShapes(t *testing.T) {
	catalog := operationalDomainSchemaCatalog()

	backup := catalog["backups"]["GET /api/v1/admin/backups/download"]
	if backup.Response["format"] != "binary" || len(backup.ResponseMedia) != 1 || backup.ResponseMedia[0] != "application/zstd" {
		t.Fatalf("backup download = %#v", backup)
	}
	memory := catalog["memory"]["PATCH /api/v1/admin/memory/config"]
	if memory.Request["type"] != "object" || memory.Request["properties"].(map[string]any)["versions_retention_days"] == nil {
		t.Fatalf("memory config request = %#v", memory.Request)
	}
	if memory.Response["properties"].(map[string]any)["versions_retention_days"] == nil {
		t.Fatalf("memory config response = %#v", memory.Response)
	}
	avatar := catalog["files-media"]["GET /api/v1/users/{id}/avatar"]
	if avatar.Response["format"] != "binary" || avatar.ResponseMedia[0] != "image/svg+xml" {
		t.Fatalf("avatar response = %#v", avatar)
	}
	signup := catalog["auth-public"]["POST /api/v1/auth/signup"]
	if signup.Request["type"] != "object" {
		t.Fatalf("signup request = %#v", signup.Request)
	}
	attachment := catalog["files-media"]["POST /api/v1/agents/{agentId}/chats/{chatId}/attachments"]
	if len(attachment.RequestMedia) != 1 || attachment.RequestMedia[0] != "multipart/form-data" {
		t.Fatalf("attachment request media = %#v", attachment.RequestMedia)
	}
	if attachment.Request["properties"].(map[string]any)["file"].(map[string]any)["format"] != "binary" {
		t.Fatalf("attachment request = %#v", attachment.Request)
	}
	for _, key := range []string{
		"PUT /api/v1/agents/{agentId}/files/save",
		"PUT /api/v1/crews/{crewId}/files/save",
	} {
		fileSave := catalog["files-media"][key]
		if len(fileSave.RequestMedia) != 1 || fileSave.RequestMedia[0] != "application/octet-stream" || fileSave.Request["format"] != "binary" {
			t.Fatalf("file save %s = %#v", key, fileSave)
		}
	}
}

func TestDomainSchemaMapReturnsIndependentCatalogs(t *testing.T) {
	first := operationalDomainSchemaCatalog()
	first["admin"]["GET /api/v1/admin/stats"] = DomainSchema{}
	if _, ok := operationalDomainSchemaCatalog()["admin"]["GET /api/v1/admin/stats"]; !ok {
		t.Fatal("DomainSchemaMap returned shared mutable state")
	}
}
