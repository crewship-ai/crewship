package pipeline

import (
	"strings"
	"testing"
)

// ODCH-RT-24: `page.create` lets a routine step create a page (name + panels)
// owned by the routine's author crew. It is governed by page_create, which is
// a different authority from page.write (structure, not content). Putting the
// page into a folder is not part of the verb: a folder's sharing is decided by
// people (docs/prd/pages-folder-permissions-linux-model-2026-09-13.md).

func TestValidate_CrewshipPageCreateArgs(t *testing.T) {
	panels := []any{map[string]any{"id": "status", "type": "metric", "title": "Status"}}
	for _, tc := range []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{"complete", map[string]any{"name": "Weekly scan", "panels": panels}, ""},
		{"with a description", map[string]any{"name": "Weekly scan", "description": "d", "panels": panels}, ""},
		{"no name", map[string]any{"panels": panels}, "name"},
		{"blank name", map[string]any{"name": "  ", "panels": panels}, "name"},
		{"no panels", map[string]any{"name": "Weekly scan"}, "panels"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(crewshipDSL("page.create", tc.args), nil, nil)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("must save: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want one naming %q", err, tc.wantErr)
			}
		})
	}
}

func TestCrewshipVerbs_PageCreateIsGovernedAndRouted(t *testing.T) {
	if got := CrewshipVerbPolicyAction("page.create"); got != "page_create" {
		t.Fatalf("page.create is gated on %q, want page_create", got)
	}
	method, path, ok := CrewshipVerbRoute("page.create")
	if !ok || method != "POST" || path != "/api/v1/internal/pages/save" {
		t.Fatalf("route = %s %s (ok=%v), want POST /api/v1/internal/pages/save", method, path, ok)
	}
	if crewshipVerbs["page.create"].RequiresActingAgent {
		t.Error("page.create must not need an acting agent: the page is owned by the author crew")
	}
}
