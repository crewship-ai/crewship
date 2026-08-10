package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// Renderer — pure function over the decoded payload, no HTTP.
// ---------------------------------------------------------------------------

func chainFixture() chainGraph {
	return chainGraph{
		Anchor:     "ENG-7",
		AnchorNode: "issue:m1",
		MaxDepth:   4,
		MaxNodes:   200,
		Nodes: []chainNode{
			{ID: "issue:m1", Kind: "issue", Ref: "m1", Key: "ENG-7", Label: "Ship the thing", Status: "PLANNING", Anchor: true,
				Partial: true, PartialReason: "inbox items raised while this issue was worked cannot be linked to it: inbox_items carries no mission/issue column."},
			{ID: "routine:p1", Kind: "routine", Ref: "p1", Key: "deploy", Label: "Deploy", Status: "active", Depth: 1},
			{ID: "run:run-1", Kind: "run", Ref: "run-1", Key: "deploy", Label: "deploy", Status: "failed", Depth: 1},
			{ID: "run:run-2", Kind: "run", Ref: "run-2", Key: "deploy", Label: "deploy", Status: "failed", Depth: 2},
		},
		Edges: []chainEdge{
			{From: "issue:m1", To: "routine:p1", Kind: "triggers"},
			{From: "issue:m1", To: "run:run-1", Kind: "triggers"},
			{From: "routine:p1", To: "run:run-1", Kind: "runs"},
			{From: "run:run-1", To: "run:run-2", Kind: "triggers"},
		},
		Gaps: []chainGap{
			{From: "inbox", To: "issue", Reason: "inbox_items has no mission column."},
			{From: "escalation", To: "run", Reason: "escalations has no run column."},
		},
	}
}

// Every edge in the response must produce exactly one line. A tree that
// quietly drops the second edge into a node is a tree that disagrees with the
// JSON the same command prints under --format json.
func TestRenderChainTree_EveryEdgeGetsExactlyOneLine(t *testing.T) {
	out := strings.Join(renderChainTree(chainFixture(), false), "\n")

	// run:run-1 has two parents (the issue and the routine). One of those
	// edges expands it; the other must still appear, as a cross-link.
	if !strings.Contains(out, "shown above") {
		t.Errorf("the second edge into run-1 produced no line — the tree disagrees with the graph:\n%s", out)
	}
	for _, ref := range []string{"ENG-7", "deploy", "run-1", "run-2"} {
		if !strings.Contains(out, ref) {
			t.Errorf("output is missing %q:\n%s", ref, out)
		}
	}
	// Two runs of the same routine share a pipeline_slug, so the handle
	// printed for a run must be its id — otherwise both render identically
	// and the tree is unreadable exactly where fan-out matters.
	if strings.Count(out, "run-1") < 2 {
		t.Errorf("run-1 should appear both where it expands and in its cross-link:\n%s", out)
	}
	if got := strings.Count(out, "[runs"); got != 1 {
		t.Errorf("the runs edge appears %d times, want exactly 1:\n%s", got, out)
	}
}

// An automation is the origin of a composed chain, so the line that stands
// for it has to carry what a reader needs to recognise the rule: the event
// that armed it, its name, and whether it is still live. The rule's id is the
// handle, because event_type is shared by every rule watching that event.
func TestRenderChainTree_AutomationLineNamesTheEventAndTheRule(t *testing.T) {
	g := chainGraph{
		Anchor:     "aut_1",
		AnchorNode: "automation:aut_1",
		MaxDepth:   4,
		MaxNodes:   200,
		Nodes: []chainNode{
			{ID: "automation:aut_1", Kind: "automation", Ref: "aut_1", Key: "run.failed",
				Label: "Triage on failure", Status: "enabled", Anchor: true},
			{ID: "routine:p1", Kind: "routine", Ref: "p1", Key: "triage", Label: "Triage", Status: "active", Depth: 1},
			{ID: "run:run-1", Kind: "run", Ref: "run-1", Key: "triage", Label: "triage", Status: "completed", Depth: 1, ChainDepth: 2},
		},
		Edges: []chainEdge{
			{From: "automation:aut_1", To: "routine:p1", Kind: "triggers"},
			{From: "automation:aut_1", To: "run:run-1", Kind: "triggers"},
		},
	}

	out := strings.Join(renderChainTree(g, false), "\n")

	if !strings.Contains(out, "automation/run.failed") {
		t.Errorf("the automation line does not name the event that arms it:\n%s", out)
	}
	if !strings.Contains(out, "Triage on failure") {
		t.Errorf("the automation line does not name the rule:\n%s", out)
	}
	if !strings.Contains(out, "aut_1") {
		t.Errorf("the automation line does not carry the rule id as its handle:\n%s", out)
	}
	if !strings.Contains(out, "enabled") {
		t.Errorf("the automation line does not say whether the rule is live:\n%s", out)
	}
	// A composed run must announce itself: "depth 2" is the reader's cue that
	// they are looking at a chain a rule built, not a run someone started.
	if !strings.Contains(out, "composed depth 2") {
		t.Errorf("a run with chain_depth 2 did not render as composed:\n%s", out)
	}
}

// Every ordinary run carries chain_depth 0, and printing "composed depth 0" on
// all of them would bury the few that are genuinely composed.
func TestRenderChainTree_UncomposedRunSaysNothingAboutDepth(t *testing.T) {
	out := strings.Join(renderChainTree(chainFixture(), false), "\n")

	if strings.Contains(out, "composed depth") {
		t.Errorf("a hand-started chain advertised a composition depth:\n%s", out)
	}
}

// The anchor is usually in the MIDDLE of its chain. Anchored on a run, the
// routine and issue above it must still be rendered — a children-only walk
// would strand both and quietly show half the chain.
func TestRenderChainTree_ShowsCausesAboveARunAnchor(t *testing.T) {
	g := chainFixture()
	g.AnchorNode = "run:run-1"

	out := strings.Join(renderChainTree(g, false), "\n")

	if !strings.Contains(out, "ENG-7") {
		t.Errorf("the issue that triggered this run is missing from a run-anchored chain:\n%s", out)
	}
	if !strings.Contains(out, "[<- ") {
		t.Errorf("no inbound edge was marked with its real direction:\n%s", out)
	}
	if strings.Contains(out, "Not reachable from the anchor") {
		t.Errorf("nodes were stranded rather than walked:\n%s", out)
	}
}

// A cycle in the data must terminate in the renderer too, not just in the
// walker — and the edge that closes it must still be visible.
func TestRenderChainTree_CycleTerminatesAndShowsTheBackEdge(t *testing.T) {
	g := chainGraph{
		AnchorNode: "assignment:a1", MaxDepth: 4, MaxNodes: 200,
		Nodes: []chainNode{
			{ID: "assignment:a1", Kind: "assignment", Ref: "a1", Label: "one", Anchor: true},
			{ID: "assignment:a2", Kind: "assignment", Ref: "a2", Label: "two", Depth: 1},
		},
		Edges: []chainEdge{
			{From: "assignment:a1", To: "assignment:a2", Kind: "triggers"},
			{From: "assignment:a2", To: "assignment:a1", Kind: "triggers"},
		},
	}

	out := strings.Join(renderChainTree(g, false), "\n")

	if strings.Count(out, "[triggers ->]")+strings.Count(out, "[<- triggers]") != 2 {
		t.Errorf("want both cycle edges rendered exactly once:\n%s", out)
	}
	if !strings.Contains(out, "shown above") {
		t.Errorf("the back-edge that closes the cycle produced no line:\n%s", out)
	}
}

// Truncation must be stated, and must name the flag that would widen it.
// "Fewer nodes than expected" is not a message.
func TestRenderChainTree_TruncationIsStatedAndActionable(t *testing.T) {
	for _, tc := range []struct{ by, wantFlag string }{
		{"nodes", "--limit"},
		{"depth", "--depth"},
	} {
		g := chainFixture()
		g.Truncated = true
		g.TruncatedBy = tc.by

		out := strings.Join(renderChainTree(g, false), "\n")

		if !strings.Contains(out, "NOT the whole chain") {
			t.Errorf("truncated_by=%s: the output does not say the chain is incomplete:\n%s", tc.by, out)
		}
		if !strings.Contains(out, tc.wantFlag) {
			t.Errorf("truncated_by=%s: the output does not name %s:\n%s", tc.by, tc.wantFlag, out)
		}
	}
}

// A complete chain must not carry the truncation warning, or the warning
// stops meaning anything.
func TestRenderChainTree_CompleteChainSaysNothingAboutTruncation(t *testing.T) {
	out := strings.Join(renderChainTree(chainFixture(), false), "\n")
	if strings.Contains(out, "Truncated") {
		t.Errorf("a complete chain was labelled truncated:\n%s", out)
	}
}

// The partial reason is the whole point of the "we cannot see this" contract.
// It must reach the terminal, once per distinct reason.
func TestRenderChainTree_PartialNodesExplainThemselves(t *testing.T) {
	out := strings.Join(renderChainTree(chainFixture(), false), "\n")

	if !strings.Contains(out, "(partial)") {
		t.Errorf("the partial issue node is not marked:\n%s", out)
	}
	if !strings.Contains(out, "Not walkable") || !strings.Contains(out, "inbox_items") {
		t.Errorf("the reason the issue is partial never reaches the reader:\n%s", out)
	}
	// --gaps is opt-in; without it the full data-model gap list stays out of
	// the way.
	if strings.Contains(out, "Known gaps in the data model") {
		t.Errorf("the gaps block printed without --gaps:\n%s", out)
	}

	withGaps := strings.Join(renderChainTree(chainFixture(), true), "\n")
	if !strings.Contains(withGaps, "Known gaps in the data model") {
		t.Errorf("--gaps did not print the gap list:\n%s", withGaps)
	}
	if !strings.Contains(withGaps, "escalations has no run column") {
		t.Errorf("--gaps did not print the escalation gap:\n%s", withGaps)
	}
}

// Labels are user- and agent-written (issue titles, assignment prompts, inbox
// subjects) and go straight to a terminal.
func TestRenderChainTree_StripsControlBytesFromLabels(t *testing.T) {
	g := chainFixture()
	g.Nodes[0].Label = "pwn\x1b[2Jed\nsecond line"

	out := strings.Join(renderChainTree(g, false), "\n")

	if strings.Contains(out, "\x1b[2J") {
		t.Errorf("an escape sequence from an issue title reached the terminal:\n%q", out)
	}
}

// ---------------------------------------------------------------------------
// RunE against a stub server.
// ---------------------------------------------------------------------------

func TestChainCmd_CallsTheRouteAndRendersIt(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/chains/ENG-7", clitest.JSONResponse(200, chainFixture()))
	covSetupCli10(t, s.URL())
	chainCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return chainCmd.RunE(chainCmd, []string{"ENG-7"})
	})
	if err != nil {
		t.Fatalf("RunE: %v (out: %s)", err, out)
	}
	if !strings.Contains(out, "ENG-7") || !strings.Contains(out, "run-2") {
		t.Errorf("human output does not render the chain:\n%s", out)
	}
}

// --format json must emit the payload, not the ANSI-coloured tree.
func TestChainCmd_FormatJSONIsMachineReadable(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/chains/ENG-7", clitest.JSONResponse(200, chainFixture()))
	covSetupCli10(t, s.URL())
	chainCmd.SetContext(context.Background())
	flagFormat = "json"

	out, err := captureStdoutCovCli10(t, func() error {
		return chainCmd.RunE(chainCmd, []string{"ENG-7"})
	})
	if err != nil {
		t.Fatalf("RunE: %v (out: %s)", err, out)
	}
	var decoded chainGraph
	if jerr := json.Unmarshal([]byte(out), &decoded); jerr != nil {
		t.Fatalf("--format json output does not parse: %v\n%s", jerr, out)
	}
	if len(decoded.Edges) != 4 || decoded.AnchorNode != "issue:m1" {
		t.Errorf("decoded payload lost data: %+v", decoded)
	}
	// gaps must survive the round trip — a machine consumer needs them as
	// much as a human does.
	if len(decoded.Gaps) != 2 {
		t.Errorf("gaps = %+v, want both known gaps", decoded.Gaps)
	}
}

// ---------------------------------------------------------------------------
// `crewship chain list` — the index.
// ---------------------------------------------------------------------------

func chainListFixture() chainList {
	return chainList{
		Chains: []chainSummary{
			{
				Origin: "prn_01j4", StartedByKind: "automation", StartedByID: "aut_01hx",
				StartedByKey: "run.failed", StartedBy: "Triage on failure", TriggeredVia: "automation",
				RoutineID: "p1", RoutineSlug: "triage", Runs: 3, MaxChainDepth: 2,
				FailedRuns: 1, Failed: true,
				FirstActivity: "2026-08-07T12:00:00.000000000Z", LastActivity: "2026-08-07T12:09:00.000000000Z",
			},
			{
				Origin: "prn_01hy", StartedByKind: "user", StartedByID: "usr_1",
				StartedBy: "Pavel", TriggeredVia: "manual",
				RoutineID: "p2", RoutineSlug: "deploy", Runs: 1,
				FirstActivity: "2026-08-07T11:00:00.000000000Z", LastActivity: "2026-08-07T11:02:00.000000000Z",
			},
		},
		Count: 2, Limit: 50, Offset: 0,
	}
}

// The row has to answer "what ran, why, how big, did it break" without the
// reader opening anything. A list that only prints ids is a list of ids.
func TestRenderChainList_RowNamesTheCauseTheSizeAndTheOutcome(t *testing.T) {
	out := strings.Join(renderChainList(chainListFixture()), "\n")

	for _, want := range []string{"Triage on failure", "triage", "prn_01j4", "Pavel", "deploy"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("a chain with a failed run is not marked:\n%s", out)
	}
	// The composed chain must announce its depth; the single hand-started run
	// must not, or "depth" stops distinguishing anything.
	if !strings.Contains(out, "2") || strings.Count(out, "FAILED") != 1 {
		t.Errorf("depth/outcome columns do not separate the two rows:\n%s", out)
	}
}

// An empty index is not "the system is fine": it means either nothing has run
// or everything predates chain recording, and the two must not read alike.
func TestRenderChainList_EmptyIndexSaysWhichKindOfEmpty(t *testing.T) {
	empty := strings.Join(renderChainList(chainList{Limit: 50}), "\n")
	if !strings.Contains(empty, "No chains") {
		t.Errorf("an empty index printed nothing a reader can act on:\n%s", empty)
	}
	if strings.Contains(empty, "predate") {
		t.Errorf("an empty index blamed pre-migration rows that do not exist:\n%s", empty)
	}

	old := chainList{Limit: 50, HasUnrecordedRuns: true}
	withOld := strings.Join(renderChainList(old), "\n")
	if !strings.Contains(withOld, "predate") {
		t.Errorf("runs recorded before chain_origin existed are not declared:\n%s", withOld)
	}
}

// A capped page must say so and name how to get the next one.
func TestRenderChainList_TruncatedPageIsStatedAndActionable(t *testing.T) {
	l := chainListFixture()
	l.Limit = 2
	l.HasMore = true

	out := strings.Join(renderChainList(l), "\n")

	if !strings.Contains(out, "--offset 2") {
		t.Errorf("a capped page does not name the offset that continues it:\n%s", out)
	}
}

// Labels come from user- and agent-written rows (rule names, issue titles) and
// are printed straight to a terminal.
func TestRenderChainList_StripsControlBytesFromLabels(t *testing.T) {
	l := chainListFixture()
	l.Chains[0].StartedBy = "pwn\x1b[2Jed"

	out := strings.Join(renderChainList(l), "\n")

	if strings.Contains(out, "\x1b[2J") {
		t.Errorf("an escape sequence from a rule name reached the terminal:\n%q", out)
	}
}

func TestChainListCmd_CallsTheRouteAndRendersIt(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/chains", clitest.JSONResponse(200, chainListFixture()))
	covSetupCli10(t, s.URL())
	chainListCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return chainListCmd.RunE(chainListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v (out: %s)", err, out)
	}
	if !strings.Contains(out, "prn_01j4") || !strings.Contains(out, "Triage on failure") {
		t.Errorf("human output does not render the index:\n%s", out)
	}
}

func TestChainListCmd_FormatJSONIsMachineReadable(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/chains", clitest.JSONResponse(200, chainListFixture()))
	covSetupCli10(t, s.URL())
	chainListCmd.SetContext(context.Background())
	flagFormat = "json"

	out, err := captureStdoutCovCli10(t, func() error {
		return chainListCmd.RunE(chainListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v (out: %s)", err, out)
	}
	var decoded chainList
	if jerr := json.Unmarshal([]byte(out), &decoded); jerr != nil {
		t.Fatalf("--format json output does not parse: %v\n%s", jerr, out)
	}
	if len(decoded.Chains) != 2 || decoded.Chains[0].Origin != "prn_01j4" {
		t.Errorf("decoded payload lost data: %+v", decoded)
	}
}

// The page bounds must reach the wire, or the flags are decoration.
func TestChainListCmd_LimitAndOffsetReachTheRequest(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/chains", clitest.JSONResponse(200, chainListFixture()))
	covSetupCli10(t, s.URL())
	chainListCmd.SetContext(context.Background())
	setFlagCovCli10(t, chainListCmd, "limit", "25")
	setFlagCovCli10(t, chainListCmd, "offset", "50")

	if _, err := captureStdoutCovCli10(t, func() error {
		return chainListCmd.RunE(chainListCmd, nil)
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	calls := s.Calls()
	if len(calls) == 0 {
		t.Fatal("no request reached the server")
	}
	q := calls[len(calls)-1].Query
	if !strings.Contains(q, "limit=25") || !strings.Contains(q, "offset=50") {
		t.Errorf("request query = %q, want limit=25 and offset=50", q)
	}
}

// `chain list` is a subcommand, so it shadows an anchor literally named
// "list". Dispatch must send each form to the command that can answer it, and
// the `--` escape has to keep the shadowed anchor reachable.
func TestChainCmd_ListSubcommandDoesNotStealOtherAnchors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want *cobra.Command
	}{
		{[]string{"chain", "list"}, chainListCmd},
		{[]string{"chain", "ENG-7"}, chainCmd},
		{[]string{"chain", "--", "list"}, chainCmd},
		{[]string{"why", "list"}, chainListCmd},
	} {
		got, _, err := rootCmd.Find(tc.args)
		if err != nil {
			t.Errorf("Find(%v): %v", tc.args, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Find(%v) resolved to %q, want %q", tc.args, got.Name(), tc.want.Name())
		}
	}
}

// The bounds must reach the wire, or the flags are decoration.
func TestChainCmd_DepthAndLimitReachTheRequest(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/chains/ENG-7", clitest.JSONResponse(200, chainFixture()))
	covSetupCli10(t, s.URL())
	chainCmd.SetContext(context.Background())
	setFlagCovCli10(t, chainCmd, "depth", "7")
	setFlagCovCli10(t, chainCmd, "limit", "42")

	if _, err := captureStdoutCovCli10(t, func() error {
		return chainCmd.RunE(chainCmd, []string{"ENG-7"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	calls := s.Calls()
	if len(calls) == 0 {
		t.Fatal("no request reached the server")
	}
	q := calls[len(calls)-1].Query
	if !strings.Contains(q, "depth=7") || !strings.Contains(q, "limit=42") {
		t.Errorf("request query = %q, want depth=7 and limit=42", q)
	}
}
