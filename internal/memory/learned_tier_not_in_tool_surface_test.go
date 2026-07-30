package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Sentinel: the agent's memory TOOL surface cannot reach a consolidated
// learned rule, so "the model can just pull it on demand" does not hold.
//
// The consolidator/approve path writes the canonical rule file to
// {crewMemory}/{crewSlug}/topics/learned-YYYY-MM-DD.md
// (internal/consolidate/approve.go:179). The only memory tools the model
// is given are the four served by this package's Dispatcher, injected as
// the sidecar MCP server "crewship-memory"
// (internal/orchestrator/mcp_memory_inject.go:43 →
// internal/sidecar/memory_mcp.go:336-337 → NewDispatcher).
//
// Both tests below show the same closed-enum reason:
//
//   - validTiers (tools.go:86-94) has no "learned" member, and neither
//     does the JSON-Schema enum the model actually sees, so memory.read
//     cannot name the file.
//   - candidateFiles (tools.go:959-1013) enumerates seven explicit paths
//     and two directories, none of which is the topics/ subtree, so
//     memory.search cannot stumble onto it either — not even with the
//     tier omitted, which is documented as "search every accessible
//     tier".
//
// Note what is NOT the blocker: assertMemoryFile/isInsideMemoryRoot
// (tools.go:911-956) would happily accept the path, since it sits under
// CrewMemoryDir. The file is inside the allowed root and still
// unreachable, purely because nothing enumerates it.
//
// A fix means adding a "learned" tier (resolvePath + capForTier +
// candidateFiles + both schema enums) — that changes what the model can
// call, so it is a product decision, not a cleanup. When it lands, both
// tests trip.

// TestMemoryToolSurface_HasNoLearnedTier asserts the closed enum the
// model is shown never mentions the learned tier.
func TestMemoryToolSurface_HasNoLearnedTier(t *testing.T) {
	if _, ok := validTiers["learned"]; ok {
		t.Fatalf(`SENTINEL TRIPPED: validTiers now accepts "learned". The dispatcher can reach consolidated
rules; update the doc comment in this file and in
internal/orchestrator/learned_rules_not_delivered_test.go (GAP 2).`)
	}

	schemas := ToolSchemas()
	for _, name := range []string{"memory.read", "memory.write", "memory.search"} {
		sch, ok := schemas[name]
		if !ok {
			t.Fatalf("control failed: %s missing from ToolSchemas()", name)
		}
		var parsed struct {
			Properties struct {
				Tier struct {
					Enum []string `json:"enum"`
				} `json:"tier"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(sch.InputSchema, &parsed); err != nil {
			t.Fatalf("%s: input schema is not valid JSON: %v", name, err)
		}
		tiers := parsed.Properties.Tier.Enum
		if len(tiers) == 0 {
			t.Fatalf("control failed: %s no longer declares a tier enum — re-derive this sentinel", name)
		}
		// Control: the tier that DOES work still advertised.
		if !contains(tiers, "CREW") {
			t.Errorf("control failed: %s tier enum lost CREW: %v", name, tiers)
		}
		for _, tier := range tiers {
			if strings.Contains(strings.ToLower(tier), "learn") {
				t.Errorf(`SENTINEL TRIPPED: %s advertises tier %q — consolidated rules are now callable.
Update the doc comment in this file.`, name, tier)
			}
		}
	}
}

// TestMemorySearch_DoesNotReachConsolidatedLearnedRules is the
// behavioural half: a real learned-*.md on disk, inside CrewMemoryDir,
// containing the query term, returns zero hits — while the control file
// (CREW.md, same needle, same root) is found.
func TestMemorySearch_DoesNotReachConsolidatedLearnedRules(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agents", "agent-1", ".memory")
	crewDir := filepath.Join(root, "shared", ".memory")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	// The consolidator's real layout: {crewMemory}/{crewSlug}/topics/.
	topicsDir := filepath.Join(crewDir, "alpha-crew", "topics")
	if err := os.MkdirAll(topicsDir, 0o755); err != nil {
		t.Fatalf("mkdir topics dir: %v", err)
	}

	const needle = "canarytoken"
	// Control: a tier the dispatcher does enumerate.
	crewMD := filepath.Join(crewDir, "CREW.md")
	if err := os.WriteFile(crewMD, []byte("crew-side "+needle+"\n"), 0o644); err != nil {
		t.Fatalf("write CREW.md: %v", err)
	}
	// Subject: an approved learned rule sitting in the same root.
	learnedMD := filepath.Join(topicsDir, "learned-2026-07-30.md")
	if err := os.WriteFile(learnedMD, []byte("## Rule: learned-side "+needle+"\n"), 0o644); err != nil {
		t.Fatalf("write learned-*.md: %v", err)
	}

	d := NewDispatcher(AgentContext{
		AgentID:        "a1",
		CrewID:         "crew1",
		WorkspaceID:    "ws1",
		AgentMemoryDir: agentDir,
		CrewMemoryDir:  crewDir,
	})

	// The learned file is inside the allowed root — containment is not
	// what blocks it.
	if err := d.assertMemoryFile(learnedMD); err != nil {
		t.Fatalf("premise failed: %s is expected to pass containment (it is under CrewMemoryDir), got %v", learnedMD, err)
	}

	// Nothing enumerates it, for any tier scope.
	for _, tier := range []string{"", "CREW", "pins", "lessons", "daily"} {
		for _, p := range d.candidateFiles(tier) {
			if strings.HasPrefix(filepath.Base(p), "learned-") {
				t.Fatalf(`SENTINEL TRIPPED: candidateFiles(%q) now enumerates %q — memory.search reaches
consolidated rules. Update the doc comment in this file.`, tier, p)
			}
		}
	}

	res, err := d.Dispatch(context.Background(), ToolCall{
		Name: "memory.search",
		Args: json.RawMessage(`{"q":"` + needle + `"}`),
	})
	if err != nil {
		t.Fatalf("memory.search dispatch: %v", err)
	}
	if res.IsError {
		t.Fatalf("memory.search returned an error result: %s", res.Content)
	}
	// Control: the mechanism works for the tier that is wired.
	if !strings.Contains(res.Content, "crew-side") {
		t.Fatalf("control failed: CREW.md hit missing from search result — the search harness is broken, repair it before trusting the assertion below.\nresult=%s", res.Content)
	}
	if strings.Contains(res.Content, "learned-side") {
		t.Fatalf(`SENTINEL TRIPPED: memory.search now returns consolidated learned rules.
Update the doc comment in this file and in
internal/orchestrator/learned_rules_not_delivered_test.go (GAP 2).
result=%s`, res.Content)
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
