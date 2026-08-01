package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests in this file cover the FTS5-backed memory.search path
// (#1651). Before it landed, the tool the model calls walked
// candidateFiles(tier) and matched lowercase substrings while the FTS5
// index the sidecar builds and rebuilds at boot sat unused — so the
// [MEMORY GAP] block woke an agent, told it to search, and handed it a
// grep.
//
// Every assertion below is written so it can only pass through the
// index: the queries are ones a substring scan CANNOT answer (terms in
// a different order than the file has them, or on two different lines
// of the same chunk). A regression that quietly drops back to the scan
// therefore reddens these rather than silently degrading recall.

// indexedDispatcher builds the dispatcher the sidecar builds in
// production: one bound to the FTS5 engines over the agent (and crew)
// memory dirs. Files already on disk are indexed before it returns.
func indexedDispatcher(t *testing.T, ac AgentContext) *Dispatcher {
	t.Helper()
	agentEng, err := New(ac.AgentMemoryDir, DefaultConfig())
	if err != nil {
		t.Fatalf("agent engine: %v", err)
	}
	t.Cleanup(func() { _ = agentEng.Close() })
	if err := agentEng.Reindex(); err != nil {
		t.Fatalf("agent reindex: %v", err)
	}
	var crewEng *Engine
	if ac.CrewMemoryDir != "" {
		crewEng, err = New(ac.CrewMemoryDir, DefaultConfig())
		if err != nil {
			t.Fatalf("crew engine: %v", err)
		}
		t.Cleanup(func() { _ = crewEng.Close() })
		if err := crewEng.Reindex(); err != nil {
			t.Fatalf("crew reindex: %v", err)
		}
	}
	return NewDispatcher(ac, WithSearchIndex(agentEng, crewEng))
}

type searchEnvelope struct {
	Hits []struct {
		Source  string  `json:"source"`
		Snippet string  `json:"snippet"`
		Line    int     `json:"line"`
		Score   float64 `json:"score"`
	} `json:"hits"`
	Quarantined []struct {
		Source string `json:"source"`
		SHA256 string `json:"quarantine_sha256"`
	} `json:"quarantined"`
	Query string `json:"query"`
}

func runSearch(t *testing.T, d *Dispatcher, args string) searchEnvelope {
	t.Helper()
	res, err := d.Dispatch(context.Background(), ToolCall{
		Name: "memory.search",
		Args: json.RawMessage(args),
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if res.IsError {
		t.Fatalf("memory.search returned an error result: %s", res.Content)
	}
	var env searchEnvelope
	if err := json.Unmarshal([]byte(res.Content), &env); err != nil {
		t.Fatalf("decode envelope: %v\nbody=%s", err, res.Content)
	}
	return env
}

func writeMemFile(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDispatch_Search_RanksThroughFTSIndex is the core #1651 assertion.
// The query terms appear in AGENT.md in the opposite order and on two
// different lines, so `strings.Contains(line, needle)` can never match
// it — only the FTS5 index can. A hit here proves memory.search now
// reaches the index the sidecar has been maintaining all along.
func TestDispatch_Search_RanksThroughFTSIndex(t *testing.T) {
	ac := testAgentCtx(t)
	writeMemFile(t, ac.AgentMemoryDir, "AGENT.md",
		"# notes\nthe rollback happened on tuesday\nharbour deploy was reverted\n")
	d := indexedDispatcher(t, ac)

	// Substring-hostile on purpose: "deploy rollback" appears nowhere
	// as a literal, and the two words live on different lines.
	env := runSearch(t, d, `{"q":"deploy rollback"}`)
	if len(env.Hits) == 0 {
		t.Fatalf("expected an FTS hit for a query no substring scan can answer; envelope=%+v", env)
	}
	if env.Hits[0].Source != "AGENT.md" {
		t.Errorf("hit source = %q, want AGENT.md", env.Hits[0].Source)
	}
	if env.Hits[0].Score <= 0 {
		t.Errorf("indexed hits must carry a rank score, got %v", env.Hits[0].Score)
	}
}

// TestDispatch_Search_IndexedPathDoesNotLeakAbsolutePaths keeps the
// pre-#1651 contract on the new path: hits carry the tier label, never
// the container's bind-mount path.
func TestDispatch_Search_IndexedPathDoesNotLeakAbsolutePaths(t *testing.T) {
	ac := testAgentCtx(t)
	writeMemFile(t, ac.AgentMemoryDir, "daily/2026-07-30.md", "shipped the harbour rollback\n")
	d := indexedDispatcher(t, ac)

	res, err := d.Dispatch(context.Background(), ToolCall{
		Name: "memory.search",
		Args: json.RawMessage(`{"q":"harbour"}`),
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if strings.Contains(res.Content, ac.AgentMemoryDir) {
		t.Errorf("search content leaked the absolute memory dir:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, `"source": "daily/2026-07-30.md"`) {
		t.Errorf("expected the tier-relative source label; got:\n%s", res.Content)
	}
}

// TestDispatch_Search_IndexedPathScopesToTier pins that the `tier`
// argument still narrows the result set once the index is answering.
// The index is per-directory and has no tier concept of its own, so
// this filter is ours to get right.
func TestDispatch_Search_IndexedPathScopesToTier(t *testing.T) {
	ac := testAgentCtx(t)
	writeMemFile(t, ac.AgentMemoryDir, "AGENT.md", "harbour deploy notes\n")
	writeMemFile(t, ac.AgentMemoryDir, "pins.md", "harbour deploy pin\n")
	d := indexedDispatcher(t, ac)

	env := runSearch(t, d, `{"q":"harbour"}`)
	if len(env.Hits) < 2 {
		t.Fatalf("control failed: unscoped search should see both files, got %+v", env.Hits)
	}

	env = runSearch(t, d, `{"q":"harbour","tier":"pins"}`)
	if len(env.Hits) == 0 {
		t.Fatalf("tier-scoped search returned nothing")
	}
	for _, h := range env.Hits {
		if h.Source != "pins.md" {
			t.Errorf("tier=pins returned %q", h.Source)
		}
	}
}

// TestDispatch_Search_IndexedPathClampsLimit pins the maxSearchLimit
// clamp on the indexed path — an agent asking for 100 hits must not be
// able to dump the corpus back into its own context window.
func TestDispatch_Search_IndexedPathClampsLimit(t *testing.T) {
	ac := testAgentCtx(t)
	for _, day := range []string{
		"2026-07-01", "2026-07-02", "2026-07-03", "2026-07-04", "2026-07-05",
		"2026-07-06", "2026-07-07", "2026-07-08", "2026-07-09", "2026-07-10",
		"2026-07-11", "2026-07-12", "2026-07-13", "2026-07-14", "2026-07-15",
		"2026-07-16", "2026-07-17", "2026-07-18", "2026-07-19", "2026-07-20",
		"2026-07-21", "2026-07-22", "2026-07-23", "2026-07-24", "2026-07-25",
	} {
		writeMemFile(t, ac.AgentMemoryDir, "daily/"+day+".md", "harbour standup notes\n")
	}
	d := indexedDispatcher(t, ac)

	env := runSearch(t, d, `{"q":"harbour","limit":100}`)
	if len(env.Hits) > maxSearchLimit {
		t.Errorf("limit=100 must clamp to %d, got %d hits", maxSearchLimit, len(env.Hits))
	}
	if len(env.Hits) != maxSearchLimit {
		t.Errorf("25 matching files should fill the clamp; got %d hits", len(env.Hits))
	}

	env = runSearch(t, d, `{"q":"harbour","limit":3}`)
	if len(env.Hits) != 3 {
		t.Errorf("explicit limit=3 not honoured: got %d hits", len(env.Hits))
	}
}

// TestDispatch_Search_IndexedPathQuarantinesPoisonedFile is the
// fail-closed contract carried across to the index. A file whose body
// trips the injection scanner contributes no snippet to `hits` even
// when the FTS index ranks it first — it is quarantined and reported
// separately, exactly as the substring path did.
func TestDispatch_Search_IndexedPathQuarantinesPoisonedFile(t *testing.T) {
	ac := testAgentCtx(t)
	writeMemFile(t, ac.AgentMemoryDir, "AGENT.md", poisonBody)
	d := indexedDispatcher(t, ac)

	env := runSearch(t, d, `{"q":"exfiltrate"}`)
	if len(env.Hits) != 0 {
		t.Errorf("poisoned file contributed %d hit(s) through the index: %+v", len(env.Hits), env.Hits)
	}
	if len(env.Quarantined) != 1 || env.Quarantined[0].Source != "AGENT.md" {
		t.Fatalf("expected one quarantine note for AGENT.md, got %+v", env.Quarantined)
	}
	if env.Quarantined[0].SHA256 == "" {
		t.Errorf("quarantine note should carry the stored sha")
	}
}

// TestDispatch_Search_IndexedPathReachesCrewTier proves the crew engine
// is consulted too — CREW.md lives in a different directory with its
// own index, so forgetting it would silently drop a whole tier from
// the tool the wake prompt points at.
func TestDispatch_Search_IndexedPathReachesCrewTier(t *testing.T) {
	ac := testAgentCtx(t)
	writeMemFile(t, ac.CrewMemoryDir, "CREW.md", "the crew agreed on tuesday to ship harbour\n")
	d := indexedDispatcher(t, ac)

	env := runSearch(t, d, `{"q":"harbour tuesday"}`)
	if len(env.Hits) == 0 || env.Hits[0].Source != "CREW.md" {
		t.Fatalf("expected a CREW.md hit from the crew index, got %+v", env.Hits)
	}

	env = runSearch(t, d, `{"q":"harbour tuesday","tier":"AGENT"}`)
	if len(env.Hits) != 0 {
		t.Errorf("tier=AGENT must not return crew hits: %+v", env.Hits)
	}
}

// TestDispatch_Search_IndexedPathOrdersByRank pins that the envelope is
// ranked best-first, including across the two engines. Each engine
// ranks its own corpus independently, so an unmerged concatenation
// would put every agent-tier hit — however weak — ahead of the crew's
// best one, and the limit would then truncate the good hit away.
func TestDispatch_Search_IndexedPathOrdersByRank(t *testing.T) {
	ac := testAgentCtx(t)
	for i, day := range []string{"2026-07-01", "2026-07-02", "2026-07-03", "2026-07-04"} {
		writeMemFile(t, ac.AgentMemoryDir, "daily/"+day+".md",
			strings.Repeat("standup notes about harbour\n", i+1))
	}
	writeMemFile(t, ac.CrewMemoryDir, "CREW.md", "harbour is the crew's shipping decision\n")
	d := indexedDispatcher(t, ac)

	env := runSearch(t, d, `{"q":"harbour"}`)
	if len(env.Hits) < 3 {
		t.Fatalf("control failed: expected several hits, got %+v", env.Hits)
	}
	for i := 1; i < len(env.Hits); i++ {
		if env.Hits[i].Score > env.Hits[i-1].Score {
			t.Fatalf("hits are not ranked best-first: hit %d (%s, %v) outscores hit %d (%s, %v)",
				i, env.Hits[i].Source, env.Hits[i].Score,
				i-1, env.Hits[i-1].Source, env.Hits[i-1].Score)
		}
	}
	// The crew's single hit is rank 1 in its own engine, so it must not
	// sink below the agent tier's lower-ranked chunks.
	crewPos := -1
	for i, h := range env.Hits {
		if h.Source == "CREW.md" {
			crewPos = i
			break
		}
	}
	if crewPos < 0 {
		t.Fatalf("crew hit missing from a merged search: %+v", env.Hits)
	}
	if crewPos > 1 {
		t.Errorf("crew rank-1 hit landed at position %d — the engines were concatenated, not merged", crewPos)
	}
}

// TestDispatch_Search_IndexedPathIgnoresUnknownFiles keeps the search
// surface to the declared tiers. The indexer walks every .md under the
// memory root, which is a wider set than candidateFiles ever exposed —
// a stray note dropped in by a tool must not become searchable just
// because the index happens to hold it.
func TestDispatch_Search_IndexedPathIgnoresUnknownFiles(t *testing.T) {
	ac := testAgentCtx(t)
	writeMemFile(t, ac.AgentMemoryDir, "scratch/notes.md", "harbour scratch\n")
	writeMemFile(t, ac.AgentMemoryDir, "AGENT.md", "harbour agent\n")
	d := indexedDispatcher(t, ac)

	env := runSearch(t, d, `{"q":"harbour"}`)
	if len(env.Hits) == 0 {
		t.Fatal("control failed: AGENT.md should hit")
	}
	for _, h := range env.Hits {
		if h.Source != "AGENT.md" {
			t.Errorf("search reached a non-tier file: %q", h.Source)
		}
	}
}

// TestDispatch_Write_RefreshesSearchIndex is the freshness half of the
// wiring. Nothing else in the container reindexes after a tool write —
// memory.StartWatcher has no production caller and the sidecar's own
// post-write reindex only runs on the legacy HTTP route — so a
// dispatcher that ranked off the index without maintaining it would
// make an agent's own notes unsearchable for the rest of the session.
func TestDispatch_Write_RefreshesSearchIndex(t *testing.T) {
	ac := testAgentCtx(t)
	d := indexedDispatcher(t, ac)

	res, err := d.Dispatch(context.Background(), ToolCall{
		Name: "memory.write",
		Args: json.RawMessage(`{"tier":"AGENT","content":"the rollback happened on tuesday\nharbour deploy was reverted\n","mode":"replace"}`),
	})
	if err != nil {
		t.Fatalf("dispatch write: %v", err)
	}
	if res.IsError {
		t.Fatalf("write failed: %s", res.Content)
	}

	env := runSearch(t, d, `{"q":"deploy rollback"}`)
	if len(env.Hits) == 0 {
		t.Fatalf("content written this session is not searchable — the index was not refreshed; envelope=%+v", env)
	}
}

// TestDispatch_AppendDaily_RefreshesSearchIndex covers the same
// freshness property through the wrapper the wake prompt tells the
// agent to use for session notes.
func TestDispatch_AppendDaily_RefreshesSearchIndex(t *testing.T) {
	ac := testAgentCtx(t)
	d := indexedDispatcher(t, ac)

	res, err := d.Dispatch(context.Background(), ToolCall{
		Name: "memory.append_daily",
		Args: json.RawMessage(`{"entry":"harbour rollback finished"}`),
	})
	if err != nil {
		t.Fatalf("dispatch append_daily: %v", err)
	}
	if res.IsError {
		t.Fatalf("append_daily failed: %s", res.Content)
	}

	env := runSearch(t, d, `{"q":"rollback harbour"}`)
	if len(env.Hits) == 0 {
		t.Fatalf("today's appended log is not searchable; envelope=%+v", env)
	}
	if !strings.HasPrefix(env.Hits[0].Source, "daily/") {
		t.Errorf("hit source = %q, want a daily/ label", env.Hits[0].Source)
	}
}

// TestDispatch_Search_FallsBackToScanWithoutIndex pins the degraded
// path: a dispatcher built without an engine (LocalDispatcher, tests,
// any caller that could not open SQLite) must still answer searches
// from disk rather than returning nothing.
func TestDispatch_Search_FallsBackToScanWithoutIndex(t *testing.T) {
	ac := testAgentCtx(t)
	writeMemFile(t, ac.AgentMemoryDir, "AGENT.md", "harbour deploy notes\n")
	d := NewDispatcher(ac)

	env := runSearch(t, d, `{"q":"harbour"}`)
	if len(env.Hits) != 1 || env.Hits[0].Source != "AGENT.md" {
		t.Fatalf("substring fallback broken: %+v", env.Hits)
	}
	if env.Hits[0].Line != 1 {
		t.Errorf("fallback hit line = %d, want 1", env.Hits[0].Line)
	}
}

// TestAdvertisedTools_AreDispatchable pins the catalogue itself: every
// name the memory MCP server advertises has a schema and a dispatcher
// arm. An advertised tool that falls through to the unknown-tool arm
// would be the same class of defect #1651 is about, one layer down.
func TestAdvertisedTools_AreDispatchable(t *testing.T) {
	schemas := ToolSchemas()
	advertised := AdvertisedTools()
	if len(advertised) == 0 {
		t.Fatal("AdvertisedTools() is empty")
	}
	d := NewDispatcher(testAgentCtx(t))
	for _, name := range advertised {
		if _, ok := schemas[name]; !ok {
			t.Errorf("advertised tool %q has no schema", name)
			continue
		}
		res, err := d.Dispatch(context.Background(), ToolCall{Name: name, Args: json.RawMessage(`{}`)})
		if err != nil {
			t.Errorf("dispatch %q: %v", name, err)
			continue
		}
		if strings.Contains(res.Content, "unknown tool") {
			t.Errorf("advertised tool %q is not routed by Dispatch: %s", name, res.Content)
		}
	}
}
