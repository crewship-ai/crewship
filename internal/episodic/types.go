// Package episodic provides vector-similarity recall over the Crew Journal.
// It is deliberately narrow: it embeds a selective subset of journal entries
// and serves top-K similarity queries to agents planning new work. The
// "selective" part is the load-bearing design choice — per 2025-2026
// multi-agent memory research, indexing every event causes catastrophic
// drift, so this package refuses to embed high-volume low-value types
// (exec.output_chunk, container.metrics, network.*) and ingests only
// escalations, summaries, terminal mission status, denied keeper calls,
// and operator-tagged entries.
//
// SQLite has no pgvector, so recall is a brute-force cosine scan over the
// scope-filtered rows. For the expected scale (~1% of entries embedded,
// low thousands per agent) the scan completes in low milliseconds. If the
// scale grows beyond that the right move is an external vector store, not
// a SQLite extension — so the code keeps its storage behind an interface.
package episodic

import "time"

// Scope controls what an agent may recall. Regular agents see only their
// own past (scope=own); lead agents see own plus crew-shared high-value
// entries from any agent in their crew (scope=crew_shared). Workspace
// isolation is always enforced at the query boundary — a cross-workspace
// recall is impossible even with a misconfigured scope.
type Scope string

const (
	ScopeOwn         Scope = "own"
	ScopeCrewShared  Scope = "crew_shared"
)

// Role maps an agent role string to the Scope it unlocks. Invariant:
// LEAD and COORDINATOR get crew_shared; anything else (AGENT default) is
// confined to own. Centralizing this mapping keeps the policy decision
// from drifting across call sites.
func ScopeForRole(role string) Scope {
	switch role {
	case "LEAD", "COORDINATOR":
		return ScopeCrewShared
	default:
		return ScopeOwn
	}
}

// Hit is a single recall result. Score is the cosine similarity in [0,1]
// where 1 is identical. Age is the time.Duration since the original
// entry's ts — consumers typically weight fresher hits higher when
// injecting into prompts.
type Hit struct {
	EntryID   string
	Score     float64
	Age       time.Duration
	Summary   string
	EntryType string
	AgentID   string
	Payload   map[string]any
}

// Query is the recall request. WorkspaceID is required; AgentID is
// required for ScopeOwn; CrewID is required for ScopeCrewShared. K caps
// the number of hits returned (1-50, defaults to 5 when 0).
type Query struct {
	WorkspaceID string
	CrewID      string
	AgentID     string
	Scope       Scope
	QueryText   string
	K           int
}

// EmbeddableEntryTypes is the single source of truth for which entry_type
// values the indexer's SQL selects as candidates. shouldEmbed then applies
// the severity-aware second pass. Both the indexer's WHERE clause and the
// shouldEmbed switch consult this slice so the coarse DB filter and the
// Go-side refinement can't drift — if you add a type here, both queries
// stay consistent.
var EmbeddableEntryTypes = []string{
	"peer.escalation",
	"peer.conversation",
	"summary.generated",
	"memory.consolidated",
	"approval.denied",
	"eval.regression_detected",
	"keeper.decision",
	"mission.status_change",
}

// shouldEmbed decides whether a given entry_type is worth the embedding
// cost + index bloat. The list is intentionally short — adding types
// here without evidence is how vector memories get polluted. Tag an
// entry manually (by setting a refs.episodic=true flag) to force
// embedding without adding the type globally.
func shouldEmbed(entryType string, severity string) bool {
	switch entryType {
	case "peer.escalation",
		"summary.generated",
		"memory.consolidated",
		"approval.denied",
		"eval.regression_detected":
		return true
	case "keeper.decision":
		return severity == "warn" || severity == "error"
	case "mission.status_change":
		return severity == "warn" || severity == "error"
	case "peer.conversation":
		// Only embed questions that ended in escalation — plain Q&A is
		// too high-volume and dilutes recall. The indexer looks at the
		// payload to decide (see indexer.go).
		return false
	}
	return false
}
