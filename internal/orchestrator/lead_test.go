package orchestrator

import (
	"strings"
	"testing"
)

func TestBuildLeadContext_WithCrewMembers(t *testing.T) {
	members := []CrewMember{
		{Name: "Alice", Slug: "alice", RoleTitle: "Backend Developer", Description: "Handles API development", Status: "IDLE"},
		{Name: "Bob", Slug: "bob", RoleTitle: "Frontend Developer", Description: "Builds UI components", Status: "BUSY"},
	}

	result := BuildLeadContext(members)

	if result == "" {
		t.Fatal("expected non-empty context, got empty")
	}
	if !strings.Contains(result, "[CREW CONTEXT]") {
		t.Error("missing [CREW CONTEXT] header")
	}
	if !strings.Contains(result, "Alice") {
		t.Error("missing crew member Alice")
	}
	if !strings.Contains(result, "Bob") {
		t.Error("missing crew member Bob")
	}
	if !strings.Contains(result, "Backend Developer") {
		t.Error("missing role title for Alice")
	}
	if !strings.Contains(result, "[END CREW CONTEXT]") {
		t.Error("missing [END CREW CONTEXT] footer")
	}
}

func TestBuildLeadContext_EmptyCrew(t *testing.T) {
	result := BuildLeadContext(nil)

	if result != "" {
		t.Errorf("expected empty string for nil members, got %q", result)
	}

	result = BuildLeadContext([]CrewMember{})

	if result != "" {
		t.Errorf("expected empty string for empty members, got %q", result)
	}
}

func TestBuildLeadContext_IncludesAllFields(t *testing.T) {
	members := []CrewMember{
		{Name: "Charlie", Slug: "charlie", RoleTitle: "DevOps Engineer", Description: "Manages infrastructure", Status: "IDLE"},
	}

	result := BuildLeadContext(members)

	if !strings.Contains(result, "Charlie") {
		t.Error("missing name")
	}
	if !strings.Contains(result, "charlie") {
		t.Error("missing slug")
	}
	if !strings.Contains(result, "DevOps Engineer") {
		t.Error("missing role_title")
	}
	if !strings.Contains(result, "Manages infrastructure") {
		t.Error("missing description")
	}
}

func TestBuildLeadContext_FormatsCorrectly(t *testing.T) {
	members := []CrewMember{
		{Name: "Alice", Slug: "alice", RoleTitle: "Backend Developer", Description: "Handles API development", Status: "IDLE"},
		{Name: "Bob", Slug: "bob", RoleTitle: "", Description: "", Status: "IDLE"},
	}

	result := BuildLeadContext(members)

	// Should contain the "fellow crew members" phrasing (equality, not hierarchy)
	if !strings.Contains(result, "crew member") {
		t.Error("expected 'crew member' phrasing for equality")
	}

	// Should NOT contain boss/subordinate language
	if strings.Contains(result, "subordinate") || strings.Contains(result, "report to") {
		t.Error("found hierarchical language, should use equality phrasing")
	}

	// Each member on separate line with dash prefix
	lines := strings.Split(result, "\n")
	memberLines := 0
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			memberLines++
		}
	}
	if memberLines != 2 {
		t.Errorf("expected 2 member lines, got %d", memberLines)
	}
}

func TestBuildLeadContext_MemberWithoutRoleTitle(t *testing.T) {
	members := []CrewMember{
		{Name: "Bob", Slug: "bob", RoleTitle: "", Description: "Does stuff", Status: "IDLE"},
	}

	result := BuildLeadContext(members)

	if !strings.Contains(result, "Bob") {
		t.Error("missing name even without role_title")
	}
	if !strings.Contains(result, "bob") {
		t.Error("missing slug even without role_title")
	}
}
