package main

import "testing"

func TestAdminSpecialRequestCatalogUsesUniqueNamedContracts(t *testing.T) {
	routes, components := adminSpecialRequestSchemaCatalog()
	if len(routes) < 30 {
		t.Fatalf("catalog covers %d routes, want the audited special-domain surface", len(routes))
	}
	for route, contract := range routes {
		if contract.Request == nil || contract.Request["$ref"] == nil {
			t.Errorf("%s has no named request contract: %#v", route, contract.Request)
		}
	}
	for _, name := range []string{
		"AdminBackupCreateRequest", "AdminKeeperConfigRequest", "MemoryImportRequest",
		"RecipeInstallRequest", "TemplateCreateRequest", "ConsolidateRunRequest",
		"EvalReplayRequest", "EmptyRequest", "InstanceSettingRequest", "FeedbackCreateRequest",
	} {
		if _, ok := components[name]; !ok {
			t.Errorf("missing unique component %q", name)
		}
	}
}

func TestAdminSpecialRequestCatalogExactRequiredFields(t *testing.T) {
	_, components := adminSpecialRequestSchemaCatalog()
	checks := map[string][]string{
		"AdminBackupCreateRequest":    {"scope"},
		"AdminBackupRestoreRequest":   {"path"},
		"AdminBackupSelfTestRequest":  {"crew_id"},
		"MemoryImportRequest":         {"crew_id", "agent_slug", "documents"},
		"MemoryVersionRestoreRequest": {"path", "canonical_path", "tier"},
		"EvalRegressionRequest":       {"baseline_mission_id", "candidate_mission_id"},
		"InstanceSettingRequest":      {"value"},
		"FeedbackCreateRequest":       {"message_id", "signal"},
	}
	for name, want := range checks {
		schema := components[name].(map[string]any)
		got, ok := schema["required"].([]string)
		if !ok {
			t.Fatalf("%s required = %#v, want %v", name, schema["required"], want)
		}
		if len(got) != len(want) {
			t.Fatalf("%s required = %v, want %v", name, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s required[%d] = %q, want %q", name, i, got[i], want[i])
			}
		}
	}
}
