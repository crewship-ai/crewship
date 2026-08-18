package memory

// Crew-shared memory tier → audit projection.
//
// §4.5 of the 2026-08-13 chat-surface audit: CREW.md HAS a writer
// (memory.write resolves the CREW tier to {CrewMemoryDir}/CREW.md,
// tools.go resolvePath), but nothing projected that write into
// memory_versions — the audit watcher's path parser only matched
// crews/{id}/agents/{slug}/.memory/…, and the crew-shared tree lives
// at crews/{id}/shared/.memory/ (crewpaths.HostCrewMemoryRoot,
// orchestrator/memory_persona.go). The memory panel reads
// crew:{crewID}/CREW.md off memory_versions, so a real shared file on
// disk rendered as an empty history — "we could not see it" shown as
// "there is nothing".
//
// These tests drive the REAL write path (the dispatcher an agent's
// MCP call lands on) through the REAL watcher loop, rather than
// inserting a row and asserting it can be read back.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// No host-side write helper lives here on purpose. An earlier draft had one,
// and the test below deliberately does not use it: writing the file directly
// would prove the watcher notices a file, which was never in doubt. What was
// in doubt is whether a container's memory.write reaches the projection the
// panel reads, so the test drives the real dispatcher and asserts on the two
// calls the panel makes.

// The parser must accept the shared tree and canonicalise it to the
// SAME "crew:{crewID}/{file}" shape the consolidator already records
// (consolidate.canonicalAuditPath) — otherwise the panel reads one
// path while the watcher writes another, and the 60 s dedup window
// between the two writers stops working.
func TestAuditWatcher_ParseMemoryPath_SharedCrewTree(t *testing.T) {
	base := "/tmp/cs-data"
	cases := []struct {
		name     string
		path     string
		wantOK   bool
		wantTier Tier
		wantRel  string
		wantCrew string
	}{
		{
			name:     "shared CREW.md → TierCrew at crew:{id}/CREW.md",
			path:     base + "/crews/crew_a/shared/.memory/CREW.md",
			wantOK:   true,
			wantTier: TierCrew,
			wantRel:  "crew:crew_a/CREW.md",
			wantCrew: "crew_a",
		},
		{
			name:     "shared pins.md → TierPins",
			path:     base + "/crews/crew_a/shared/.memory/pins.md",
			wantOK:   true,
			wantTier: TierPins,
			wantRel:  "crew:crew_a/pins.md",
			wantCrew: "crew_a",
		},
		{
			name:     "consolidator topics dir → same canonical path it records itself",
			path:     base + "/crews/crew_a/shared/.memory/alpha-crew/topics/learned-2026-08-13.md",
			wantOK:   true,
			wantTier: TierLearned,
			wantRel:  "crew:crew_a/learned-2026-08-13.md",
			wantCrew: "crew_a",
		},
		{
			name:     "shared daily log → TierCrew",
			path:     base + "/crews/crew_a/shared/.memory/daily/2026-08-13.md",
			wantOK:   true,
			wantTier: TierCrew,
			wantRel:  "crew:crew_a/daily/2026-08-13.md",
			wantCrew: "crew_a",
		},
		// ── and the rejections still hold ──────────────────────────
		{
			name:   "proposal staging is not canonical memory",
			path:   base + "/crews/crew_a/shared/.memory/.proposed/learned-2026-08-13.md",
			wantOK: false,
		},
		{
			name:   "agent-side snapshot copies are not canonical memory",
			path:   base + "/crews/crew_a/agents/martin/.memory/.snapshots/AGENT.md",
			wantOK: false,
		},
		{
			name:   "shared PERSONA.md has its own history surface, not this one",
			path:   base + "/crews/crew_a/shared/.memory/PERSONA.md",
			wantOK: false,
		},
		{
			name:   "shared operator model is not a memory tier",
			path:   base + "/crews/crew_a/shared/.memory/users/u_ab12.md",
			wantOK: false,
		},
		{
			name:   "non-memory file in the shared tree is skipped",
			path:   base + "/crews/crew_a/shared/.memory/index.sqlite",
			wantOK: false,
		},
		{
			name:   "shared file outside .memory is skipped",
			path:   base + "/crews/crew_a/shared/notes.md",
			wantOK: false,
		},
		{
			name:   "unknown third segment is skipped",
			path:   base + "/crews/crew_a/scratch/.memory/CREW.md",
			wantOK: false,
		},
		{
			name:   "outside basePath returns false",
			path:   "/etc/passwd",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseMemoryPath(base, tc.path)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (parsed = %+v)", ok, tc.wantOK, got)
			}
			if !tc.wantOK {
				return
			}
			if got.Tier != tc.wantTier {
				t.Errorf("tier = %q, want %q", got.Tier, tc.wantTier)
			}
			if got.RelPath != tc.wantRel {
				t.Errorf("rel = %q, want %q", got.RelPath, tc.wantRel)
			}
			if got.CrewID != tc.wantCrew {
				t.Errorf("crew = %q, want %q", got.CrewID, tc.wantCrew)
			}
			if got.AgentSlug != "" {
				t.Errorf("agent_slug = %q, want empty — the shared tier belongs to no single agent", got.AgentSlug)
			}
		})
	}
}

// The assertion the package exists for: an agent's memory.write on the
// CREW tier — the real dispatcher call, not a hand-built file — ends up
// in the rows the memory panel lists for crew:{crewID}/CREW.md.
func TestAuditWatcher_CrewTierWrite_ReachesPanelProjection(t *testing.T) {
	base, db, j, scr := auditTestRig(t)
	if err := os.MkdirAll(filepath.Join(base, "crews"), 0o755); err != nil {
		t.Fatal(err)
	}
	crewDir, err := HostCrewMemoryRoot(base, "crew_audit")
	if err != nil {
		t.Fatalf("HostCrewMemoryRoot: %v", err)
	}
	agentDir, err := HostAgentMemoryRoot(base, "crew_audit", "martin")
	if err != nil {
		t.Fatalf("HostAgentMemoryRoot: %v", err)
	}
	for _, d := range []string{crewDir, agentDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartAuditWatcher(ctx, db, j, AuditWatcherConfig{
		BasePath:             base,
		BlobRoot:             filepath.Join(base, "versions"),
		Scrubber:             scr,
		DebounceInterval:     20 * time.Millisecond,
		PollFallbackInterval: 50 * time.Millisecond,
	}, quietLogger())

	// The write an agent actually performs: memory.write, tier CREW.
	disp := NewDispatcher(AgentContext{
		AgentID:        "agent_martin",
		CrewID:         "crew_audit",
		WorkspaceID:    "ws_audit",
		AgentMemoryDir: agentDir,
		CrewMemoryDir:  crewDir,
	})
	body := "# Crew memory\n- the deploy runbook lives in infra/\n"
	args, _ := json.Marshal(writeArgs{Tier: "CREW", Content: body, Mode: "replace"})
	res, err := disp.Dispatch(ctx, ToolCall{Name: "memory.write", Args: args})
	if err != nil {
		t.Fatalf("memory.write: %v", err)
	}
	if res.IsError {
		t.Fatalf("memory.write returned is_error: %s", res.Content)
	}
	full := filepath.Join(crewDir, "CREW.md")
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("CREW.md not on disk after memory.write: %v", err)
	}

	// What the panel reads: GET /api/v1/memory/versions?path=… →
	// memory.LogVersions for that exact canonical path.
	const auditPath = "crew:crew_audit/CREW.md"
	deadline := time.Now().Add(8 * time.Second)
	lastTouch := time.Now()
	var entries []VersionEntry
	for time.Now().Before(deadline) {
		entries, err = LogVersions(ctx, db, "ws_audit", auditPath, 20)
		if err != nil {
			t.Fatalf("LogVersions: %v", err)
		}
		if len(entries) > 0 {
			break
		}
		if time.Since(lastTouch) > 300*time.Millisecond {
			now := time.Now()
			_ = os.Chtimes(full, now, now)
			lastTouch = now
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(entries) == 0 {
		t.Fatalf("memory.write on the CREW tier produced no version at %q — "+
			"the file is on disk at %s but the panel would render an empty history",
			auditPath, full)
	}
	if entries[0].Tier != string(TierCrew) {
		t.Errorf("tier = %q, want %q", entries[0].Tier, TierCrew)
	}
	if entries[0].Bytes != len(body) {
		t.Errorf("bytes = %d, want %d", entries[0].Bytes, len(body))
	}
	content, err := ReadVersion(ctx, db, "ws_audit", auditPath, entries[0].Sha256)
	if err != nil {
		t.Fatalf("ReadVersion (the panel's second call): %v", err)
	}
	if string(content) != body {
		t.Errorf("blob = %q, want %q", content, body)
	}
}
