package orchestrator

import "testing"

// BenchmarkBuildLeadContext measures the cost of building the LEAD system-
// prompt block. Called on every LEAD agent run — the dynamic part is the
// member list, but the vast majority of the output is a static orchestration
// cheat-sheet (how to /assign, /query, build missions, etc.).
func BenchmarkBuildLeadContext(b *testing.B) {
	members := []CrewMember{
		{Name: "Anna", Slug: "anna", RoleTitle: "backend", Description: "Go services"},
		{Name: "Ben", Slug: "ben", RoleTitle: "frontend", Description: "React + Next"},
		{Name: "Cora", Slug: "cora", RoleTitle: "ops", Description: "CI/CD"},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildLeadContext(members)
	}
}
