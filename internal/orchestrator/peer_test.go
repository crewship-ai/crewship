package orchestrator

import (
	"strings"
	"testing"
)

func TestBuildPeerContext(t *testing.T) {
	tests := []struct {
		name           string
		members        []CrewMember
		selfSlug       string
		wantEmpty      bool
		wantContains   []string
		wantNotContain []string
	}{
		{
			name:      "nil members returns empty",
			members:   nil,
			selfSlug:  "alice",
			wantEmpty: true,
		},
		{
			name:      "empty slice returns empty",
			members:   []CrewMember{},
			selfSlug:  "alice",
			wantEmpty: true,
		},
		{
			name: "only self in members returns empty",
			members: []CrewMember{
				{Name: "Alice", Slug: "alice", RoleTitle: "Dev"},
			},
			selfSlug:  "alice",
			wantEmpty: true,
		},
		{
			name: "filters out self and shows others",
			members: []CrewMember{
				{Name: "Alice", Slug: "alice", RoleTitle: "Backend Dev"},
				{Name: "Bob", Slug: "bob", RoleTitle: "Frontend Dev"},
				{Name: "Charlie", Slug: "charlie", RoleTitle: "DevOps"},
			},
			selfSlug:       "alice",
			wantContains:   []string{"[PEER COMMUNICATION]", "[END PEER COMMUNICATION]", "Bob", "bob", "Charlie", "charlie"},
			wantNotContain: []string{"- Alice"},
		},
		{
			name: "includes query and escalate instructions",
			members: []CrewMember{
				{Name: "Alice", Slug: "alice"},
				{Name: "Bob", Slug: "bob"},
			},
			selfSlug: "alice",
			wantContains: []string{
				"localhost:9119/query",
				"localhost:9119/escalate",
				`"from":"alice"`,
			},
		},
		{
			name: "includes role title and description",
			members: []CrewMember{
				{Name: "Alice", Slug: "alice"},
				{Name: "Bob", Slug: "bob", RoleTitle: "Frontend Engineer", Description: "Builds UI"},
			},
			selfSlug:     "alice",
			wantContains: []string{"Frontend Engineer", "Builds UI"},
		},
		{
			name: "member without role_title",
			members: []CrewMember{
				{Name: "Alice", Slug: "alice"},
				{Name: "Bob", Slug: "bob", RoleTitle: ""},
			},
			selfSlug:       "alice",
			wantContains:   []string{"Bob (@bob)"},
			wantNotContain: []string{"Bob (@bob,"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildPeerContext(tt.members, tt.selfSlug)

			if tt.wantEmpty {
				if result != "" {
					t.Errorf("expected empty string, got %q", result)
				}
				return
			}

			if result == "" {
				t.Fatal("expected non-empty context, got empty")
			}

			for _, s := range tt.wantContains {
				if !strings.Contains(result, s) {
					t.Errorf("result missing %q\nresult:\n%s", s, result)
				}
			}

			for _, s := range tt.wantNotContain {
				if strings.Contains(result, s) {
					t.Errorf("result should not contain %q\nresult:\n%s", s, result)
				}
			}
		})
	}
}
