package orchestrator

import "testing"

// benchmarkBuildPeerContextSink keeps BuildPeerContext's result observable to
// the runtime so the compiler can't dead-code-eliminate the allocation the
// benchmark is trying to measure.
var benchmarkBuildPeerContextSink string

// BenchmarkBuildPeerContext measures the cost of building the PEER
// COMMUNICATION block. Called on every non-LEAD agent run that has crew
// members, so it fires more often than BuildLeadContext.
func BenchmarkBuildPeerContext(b *testing.B) {
	members := []CrewMember{
		{Name: "Anna", Slug: "anna", RoleTitle: "backend", Description: "Go services"},
		{Name: "Ben", Slug: "ben", RoleTitle: "frontend", Description: "React + Next"},
		{Name: "Cora", Slug: "cora", RoleTitle: "ops", Description: "CI/CD"},
		{Name: "Self", Slug: "self"},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkBuildPeerContextSink = BuildPeerContext(members, "self")
	}
}
