package manifest

import "testing"

// TestCrewBodyDiffers_DriftMatrix is the regression backstop for the
// "apply hlásil 0 updated po reálné změně manifestu" class of bugs.
// Each row is one of: a field the manifest can change, plus the
// expectation about whether crewBodyDiffers picks the change up.
//
// The pre-fix function only covered name/description/color/icon/
// runtime_image/devcontainer_config/mise_config. services_json and
// the container/network limits were absent, so a manifest editing
// only those fields planned as "= crew" (unchanged) and the server
// never received the patch — which in turn meant
// crews_update.go::cached_image=NULL never fired, leaving the
// runtime container with stale code.
func TestCrewBodyDiffers_DriftMatrix(t *testing.T) {
	t.Parallel()

	// baseExisting is the "what's already in the DB" half of the
	// comparison. Each subtest patches one field on a *copy* to
	// produce drift; the body half is the post-edit manifest output.
	baseExisting := func() *CrewResponse {
		desc := "old"
		col := "#000"
		icon := "i"
		img := "old/image:1"
		dc := `{"image":"old"}`
		mise := "old-mise"
		svcs := `[{"name":"redis","image":"redis:7"}]`
		mem := 1024
		cpus := 1.0
		ttl := 24
		net := "isolated"
		return &CrewResponse{
			ID:                 "c1",
			Name:               "Old name",
			Slug:               "crew",
			Description:        &desc,
			Color:              &col,
			Icon:               &icon,
			RuntimeImage:       &img,
			DevcontainerConfig: &dc,
			MiseConfig:         &mise,
			ServicesJSON:       &svcs,
			ContainerMemoryMB:  &mem,
			ContainerCPUs:      &cpus,
			ContainerTTLHours:  &ttl,
			NetworkMode:        &net,
			AllowedDomains:     []string{"api.example.com"},
		}
	}

	matchingBody := func() map[string]any {
		return map[string]any{
			"name":                "Old name",
			"description":         "old",
			"color":               "#000",
			"icon":                "i",
			"runtime_image":       "old/image:1",
			"devcontainer_config": `{"image":"old"}`,
			"mise_config":         "old-mise",
			"services_json":       `[{"name":"redis","image":"redis:7"}]`,
			"container_memory_mb": 1024,
			"container_cpus":      1.0,
			"container_ttl_hours": 24,
			"network_mode":        "isolated",
			"allowed_domains":     []string{"api.example.com"},
		}
	}

	if crewBodyDiffers(baseExisting(), matchingBody()) {
		t.Fatal("identical body+existing must report no drift; otherwise re-apply of an unchanged manifest will spam updates")
	}

	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"name", func(b map[string]any) { b["name"] = "New" }},
		{"description", func(b map[string]any) { b["description"] = "new" }},
		{"color", func(b map[string]any) { b["color"] = "#fff" }},
		{"icon", func(b map[string]any) { b["icon"] = "j" }},
		{"runtime_image", func(b map[string]any) { b["runtime_image"] = "new/image:2" }},
		{"devcontainer_config (added feature)", func(b map[string]any) {
			b["devcontainer_config"] = `{"image":"old","features":{"claude-code:1":{}}}`
		}},
		{"mise_config", func(b map[string]any) { b["mise_config"] = "new-mise" }},
		// The four pre-fix gaps:
		{"services_json (added sidecar)", func(b map[string]any) {
			b["services_json"] = `[{"name":"redis","image":"redis:7"},{"name":"postgres","image":"postgres:16-alpine"}]`
		}},
		{"container_memory_mb", func(b map[string]any) { b["container_memory_mb"] = 2048 }},
		{"container_cpus", func(b map[string]any) { b["container_cpus"] = 2.0 }},
		{"container_ttl_hours", func(b map[string]any) { b["container_ttl_hours"] = 48 }},
		{"network_mode", func(b map[string]any) { b["network_mode"] = "open" }},
		{"allowed_domains (added)", func(b map[string]any) {
			b["allowed_domains"] = []string{"api.example.com", "another.example.com"}
		}},
		{"allowed_domains (removed)", func(b map[string]any) {
			b["allowed_domains"] = []string{}
		}},
		{"allowed_domains (reordered)", func(b map[string]any) {
			// Reorder counts as drift — the server stores the array
			// in the order the operator declared it and downstream
			// allowlist matching may not be order-insensitive. If a
			// future iteration normalises ordering, relax this case.
			b["allowed_domains"] = []string{"another.example.com", "api.example.com"}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := matchingBody()
			tc.mutate(body)
			if !crewBodyDiffers(baseExisting(), body) {
				t.Errorf("change to %s must trigger drift; otherwise apply will report 0 updated despite the manifest having changed", tc.name)
			}
		})
	}
}
