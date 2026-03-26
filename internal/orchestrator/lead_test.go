package orchestrator

import (
	"strings"
	"testing"
)

func TestBuildLeadContext(t *testing.T) {
	tests := []struct {
		name           string
		members        []CrewMember
		wantEmpty      bool
		wantContains   []string
		wantNotContain []string
		wantMemberLines int
	}{
		{
			name:      "nil members returns empty",
			members:   nil,
			wantEmpty: true,
		},
		{
			name:      "empty slice returns empty",
			members:   []CrewMember{},
			wantEmpty: true,
		},
		{
			name: "includes all fields",
			members: []CrewMember{
				{Name: "Charlie", Slug: "charlie", RoleTitle: "DevOps Engineer", Description: "Manages infrastructure", Status: "IDLE"},
			},
			wantContains:    []string{"[CREW CONTEXT]", "[END CREW CONTEXT]", "Charlie", "charlie", "DevOps Engineer", "Manages infrastructure"},
			wantMemberLines: 1,
		},
		{
			name: "includes assignment instructions",
			members: []CrewMember{
				{Name: "Viktor", Slug: "viktor", RoleTitle: "Backend Developer"},
			},
			wantContains: []string{
				"localhost:9119/assign",
				"localhost:9119/results/",
				"target",
				"task",
			},
		},
		{
			name: "includes query and standup instructions",
			members: []CrewMember{
				{Name: "Viktor", Slug: "viktor", RoleTitle: "Backend Developer"},
			},
			wantContains: []string{
				"localhost:9119/query",
				"localhost:9119/standup",
				"quick question",
				"standup summary",
			},
		},
		{
			name: "multiple members with equality phrasing",
			members: []CrewMember{
				{Name: "Alice", Slug: "alice", RoleTitle: "Backend Developer", Description: "Handles API development", Status: "IDLE"},
				{Name: "Bob", Slug: "bob", RoleTitle: "Frontend Developer", Description: "Builds UI components", Status: "BUSY"},
			},
			wantContains:    []string{"[CREW CONTEXT]", "[END CREW CONTEXT]", "Alice", "Bob", "Backend Developer", "crew member"},
			wantNotContain:  []string{"subordinate", "report to"},
			wantMemberLines: 2,
		},
		{
			name: "member without role_title",
			members: []CrewMember{
				{Name: "Bob", Slug: "bob", RoleTitle: "", Description: "Does stuff", Status: "IDLE"},
			},
			wantContains:    []string{"Bob", "bob"},
			wantMemberLines: 1,
		},
		{
			name: "members with integrations",
			members: []CrewMember{
				{
					Name: "Pepa", Slug: "pepa", RoleTitle: "Backend Dev",
					Integrations: []MemberIntegration{
						{Name: "Gmail", ServerName: "gmail", Tools: []string{"gmail_send", "gmail_search"}},
						{Name: "GDrive", ServerName: "gdrive"},
					},
				},
				{
					Name: "Franta", Slug: "franta", RoleTitle: "Frontend Dev",
					Integrations: []MemberIntegration{
						{Name: "Gmail", ServerName: "gmail", Tools: []string{"gmail_send"}},
					},
				},
			},
			wantContains: []string{
				"Integrations: Gmail (gmail_send, gmail_search), GDrive",
				"Integrations: Gmail (gmail_send)",
				"Pepa", "Franta",
			},
			wantMemberLines: 2,
		},
		{
			name: "member without integrations shows no Integrations line",
			members: []CrewMember{
				{Name: "Solo", Slug: "solo", RoleTitle: "Dev"},
			},
			wantContains:    []string{"Solo"},
			wantNotContain:  []string{"Integrations:"},
			wantMemberLines: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildLeadContext(tt.members)

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
					t.Errorf("result missing %q", s)
				}
			}

			for _, s := range tt.wantNotContain {
				if strings.Contains(result, s) {
					t.Errorf("result should not contain %q", s)
				}
			}

			if tt.wantMemberLines > 0 {
				lines := strings.Split(result, "\n")
				memberLines := 0
				for _, line := range lines {
					if strings.HasPrefix(strings.TrimSpace(line), "- ") {
						memberLines++
					}
				}
				if memberLines != tt.wantMemberLines {
					t.Errorf("expected %d member lines, got %d", tt.wantMemberLines, memberLines)
				}
			}
		})
	}
}

func TestBuildCoordinatorContext(t *testing.T) {
	tests := []struct {
		name         string
		crews        []CrewInfo
		wantEmpty    bool
		wantContains []string
	}{
		{
			name:      "nil crews returns empty",
			crews:     nil,
			wantEmpty: true,
		},
		{
			name: "single crew with members",
			crews: []CrewInfo{
				{
					ID: "c1", Name: "Dev Crew", Slug: "dev-crew",
					Members: []CrewMember{
						{Name: "Alice", Slug: "alice", RoleTitle: "Backend Dev"},
						{Name: "Bob", Slug: "bob", RoleTitle: "Frontend Dev"},
					},
				},
			},
			wantContains: []string{
				"[COORDINATOR CONTEXT]",
				"[END COORDINATOR CONTEXT]",
				"Dev Crew",
				"@dev-crew",
				"Alice",
				"Bob",
				"crew_id=c1",
				"PROPOSAL WORKFLOW",
			},
		},
		{
			name: "multiple crews",
			crews: []CrewInfo{
				{ID: "c1", Name: "Dev Crew", Slug: "dev"},
				{ID: "c2", Name: "QA Crew", Slug: "qa", Members: []CrewMember{
					{Name: "Tester", Slug: "tester"},
				}},
			},
			wantContains: []string{"Dev Crew", "QA Crew", "Tester", "PROPOSAL WORKFLOW"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildCoordinatorContext(tt.crews, nil)

			if tt.wantEmpty {
				if result != "" {
					t.Errorf("expected empty, got %q", result)
				}
				return
			}

			for _, s := range tt.wantContains {
				if !strings.Contains(result, s) {
					t.Errorf("missing %q in result", s)
				}
			}
		})
	}
}
