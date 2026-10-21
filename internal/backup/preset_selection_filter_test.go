package backup

import (
	"reflect"
	"testing"
)

func TestCustomContentsDropsOnlyDanglingPresetLabels(t *testing.T) {
	for _, categories := range [][]string{{CategoryAgents}, {CategoryAgents, CategoryRoutines}} {
		filter, err := newCategoryFilter(categories)
		if err != nil {
			t.Fatal(err)
		}
		canonical := []map[string]any{{"id": "grant", "routine_id": "routine"}}
		dump := &DBDump{Tables: map[string][]map[string]any{
			"workspaces": {{"id": "workspace"}}, "workspace_members": {{"id": "member"}}, "users": {{"id": "selector"}},
			"projects": {{"id": "project"}}, "pipelines": {{"id": "routine"}}, "access_grants": canonical,
			"access_preset_selections": {
				{"id": "project-choice", "workspace_id": "workspace", "member_id": "member", "resource_kind": "project", "project_id": "project", "selected_by": "selector"},
				{"id": "routine-choice", "workspace_id": "workspace", "member_id": "member", "resource_kind": "routine", "routine_id": "routine", "selected_by": "selector"},
			},
		}}
		filter.filterDump(dump)
		want := 0
		if len(categories) == 2 {
			want = 1
		}
		if got := dump.Tables["access_preset_selections"]; len(got) != want || want == 1 && got[0]["id"] != "routine-choice" {
			t.Fatalf("dangling choices retained: %+v", got)
		}
		if !reflect.DeepEqual(dump.Tables["access_grants"], canonical) {
			t.Fatal("canonical grants changed by label filtering")
		}
	}
}
