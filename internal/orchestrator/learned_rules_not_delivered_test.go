package orchestrator

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/provider"
)

// Sentinel tests for the memory-consolidation "learned rule" promotion.
//
// THE FINDING these tests pin down: approving a memory proposal writes a
// canonical learned-YYYY-MM-DD.md file that NOTHING ever delivers to the
// agent. The write path is real and well-tested; the read path does not
// exist. Three independent gaps stack, any one of which alone would be
// enough to make the rule undeliverable:
//
// GAP 0 — CLOSED by #1663. It used to be that the file never landed
//
//	inside the container at all: the runner's default output root was
//	the container-absolute "/crew/shared/.memory" written by a HOST
//	process, while /crew is a bind of host
//	{Storage.BasePath}/crews/{crewID}. The consolidator now resolves its
//	output through memory.HostCrewTopicsDir, the host twin of the
//	container path this file's tests use, so the learned file really is
//	where these tests place it. Positive coverage:
//	internal/consolidate/crew_memory_host_path_test.go and
//	internal/memory/crewpaths_test.go. The gaps below are what remains.
//
// GAP 1 — the boot prompt never reads it.
//
//	consolidate.ApproveProposal appends to
//	{OutputDir}/learned-YYYY-MM-DD.md (approve.go:179, mirrored by the
//	exported consolidate.CanonicalPathForProposal at approve.go:503),
//	where OutputDir is memory.HostCrewTopicsDir(basePath, crewID,
//	crewSlug) — the host side of
//	/crew/shared/.memory/{crewSlug}/topics.
//	Orchestrator.buildMemoryContext reads a CLOSED LIST of container
//	paths — /crew/agents/{slug}/.memory/{pins,BRIEF,AGENT}.md +
//	daily/* (memory.go:251-277), /crew/shared/.memory/{CREW.md,
//	daily/*,lessons.md} (memory.go:301-325), and
//	/crew/shared/.memory/{crewSlug}/topics/pins.md (memory.go:395).
//	That last one is the SIBLING of learned-*.md in the very same
//	directory: pins.md is injected, learned-*.md is not.
//
// GAP 2 — no memory tool can reach it either. The agent's only memory
//
//	surface is the sidecar MCP server crewship-memory
//	(mcp_memory_inject.go:43), which routes to memory.Dispatcher. Its
//	tier enum is closed and has no "learned" member, and
//	candidateFiles never enumerates the topics/ subtree. Pinned in
//	internal/memory/learned_tier_not_in_tool_surface_test.go.
//
// GAP 3 — the agent's CWD cannot discover it by file convention.
//
//	The agent CLI runs with WorkingDir = /output/{agentSlug}
//	(orchestrator_run.go:1113-1115, buildExecCommand at :1335).
//	/crew/shared/.memory/... is on a different mount entirely, so the
//	CLAUDE.md / AGENTS.md working-directory conventions cannot see it
//	(and adapter_claude.go runs Claude Code with --setting-sources "",
//	which disables CLAUDE.md auto-discovery outright).
//
// A fix has to close GAP 1 or GAP 2 deliberately — either read
// learned-*.md in buildCrewMemoryBlock/buildPinsBlock (a new prompt
// section, i.e. a product decision about what the agent receives), or
// add a "learned" tier to the dispatcher so the model can pull it on
// demand. When that happens these sentinels trip; flip them and delete
// the corresponding gap from this comment.

// recordingMemoryContainer is a mockContainer that answers `cat` from a
// file map AND records every path the orchestrator asked for. The
// recording is the load-bearing part: "the content is absent from the
// prompt" could be a budget artefact, but "the orchestrator never even
// issued a read for the file" is unambiguous.
type recordingMemoryContainer struct {
	*mockContainer
	mu      sync.Mutex
	catPath []string
}

func newRecordingMemoryContainer(files map[string]string) *recordingMemoryContainer {
	rc := &recordingMemoryContainer{mockContainer: &mockContainer{}}
	rc.mockContainer.execFn = func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		if len(cfg.Cmd) == 2 && cfg.Cmd[0] == "cat" {
			rc.mu.Lock()
			rc.catPath = append(rc.catPath, cfg.Cmd[1])
			rc.mu.Unlock()
			return &provider.ExecResult{
				ExecID: "cat",
				Reader: io.NopCloser(strings.NewReader(files[cfg.Cmd[1]])),
			}, nil
		}
		return &provider.ExecResult{ExecID: "noop", Reader: io.NopCloser(strings.NewReader(""))}, nil
	}
	return rc
}

func (rc *recordingMemoryContainer) paths() []string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	out := make([]string, len(rc.catPath))
	copy(out, rc.catPath)
	return out
}

// TestLearnedRules_NotReadIntoBootPrompt is the GAP 1 sentinel. Both
// pins.md and the approved learned-*.md sit in the same container
// directory; the prompt assembler reads one and ignores the other.
//
// The learned-*.md filename is not hardcoded here — it comes from
// consolidate.CanonicalPathForProposal, the same helper the approve
// path and the HITL diff endpoint use — so if the writer ever renames
// or relocates the canonical file this test follows it instead of
// silently testing a stale path.
func TestLearnedRules_NotReadIntoBootPrompt(t *testing.T) {
	const crewSlug = "alpha-crew"
	now := time.Now().UTC()

	// Exactly what the approve path would produce for this crew: a
	// proposal under {crewMemory}/.proposed/ merges into the canonical
	// learned-*.md one directory up.
	proposalPath := path.Join("/crew/shared/.memory", crewSlug, "topics", ".proposed", "proposal-run1.md")
	learnedPath := filepath.ToSlash(consolidate.CanonicalPathForProposal(proposalPath, now))

	wantDir := path.Join("/crew/shared/.memory", crewSlug, "topics")
	if got := path.Dir(learnedPath); got != wantDir {
		t.Fatalf("canonical learned dir moved: got %q want %q — the approve path changed; re-derive the container path this sentinel checks", got, wantDir)
	}
	if !strings.HasPrefix(path.Base(learnedPath), "learned-") {
		t.Fatalf("canonical file is no longer learned-*: %q", path.Base(learnedPath))
	}

	pinsPath := path.Join(wantDir, "pins.md")

	mc := newRecordingMemoryContainer(map[string]string{
		// Control: this sibling IS wired into the prompt.
		pinsPath: "- **j_42** — pins-canary-token\n",
		// Subject: an approved learned rule, human-signed off.
		learnedPath: "## Rule: learned-canary-token\n**Confidence:** high\n",
	})

	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		ContainerID:   "c1",
		AgentSlug:     "agent-1",
		AgentID:       "a1",
		CrewID:        "crew1",
		CrewSlug:      crewSlug,
		WorkspaceID:   "ws1",
		MemoryEnabled: true,
	}

	block := o.buildMemoryContext(context.Background(), req, 0)

	// Control assertion: the mechanism works for pins.md. If this
	// fails the test is measuring nothing and must be repaired before
	// its negative assertions mean anything.
	if !strings.Contains(block, "pins-canary-token") {
		t.Fatalf("control failed: pins.md content missing from prompt — buildPinsBlock no longer reads %s; fix the control before trusting the assertions below.\nblock=%q", pinsPath, block)
	}

	if strings.Contains(block, "learned-canary-token") {
		t.Fatalf(`SENTINEL TRIPPED (GAP 1 closed): approved learned rules now reach the boot prompt.
Update the doc comment at the top of this file and flip this assertion to a positive one.
block=%q`, block)
	}

	// The stronger form: no read was even attempted.
	for _, p := range mc.paths() {
		if strings.HasPrefix(path.Base(p), "learned-") {
			t.Fatalf(`SENTINEL TRIPPED (GAP 1 closed): the orchestrator issued a read for %q.
Approved learned rules are now being fetched from the container; flip this sentinel.`, p)
		}
	}

	// And the read set is the closed list documented above — pins.md
	// from the topics dir, nothing else from it.
	var fromTopicsDir []string
	for _, p := range mc.paths() {
		if path.Dir(p) == wantDir {
			fromTopicsDir = append(fromTopicsDir, path.Base(p))
		}
	}
	if len(fromTopicsDir) != 1 || fromTopicsDir[0] != "pins.md" {
		t.Errorf("read set for %s changed: got %v, want exactly [pins.md] — a new consumer of the consolidator's output dir appeared; re-check whether learned-*.md is now covered", wantDir, fromTopicsDir)
	}
}

// TestLearnedRules_MemoryInstructionsPointAtTheWrongTopicsDir documents
// the one place the prompt gestures at the consolidator's output at
// all — and shows the pointer is a path segment short.
//
// renderMemoryInstructions (memory.go:535) tells the agent that
// "/crew/shared/.memory/topics/*.md" holds domain knowledge. The
// consolidator writes to /crew/shared/.memory/{crewSlug}/topics/. The
// shorter path is real — orchestrator_run.go:1176 mkdir -p's it on
// every memory-enabled crew run — so the agent that follows the
// instruction lands in a directory that never receives a learned rule.
// That is why "the agent can just read the file itself" does not hold:
// nothing tells it the crew-slug-qualified path.
func TestLearnedRules_MemoryInstructionsPointAtTheWrongTopicsDir(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	instr := renderMemoryInstructions(today)

	const advertised = "/crew/shared/.memory/topics/*.md"
	if !strings.Contains(instr, advertised) {
		t.Fatalf("control failed: instructions no longer advertise %q — re-derive this sentinel from the current text.\ninstr=%q", advertised, instr)
	}

	// The instructions never mention the canonical file or the
	// crew-slug-qualified directory it lives in.
	if strings.Contains(instr, "learned-") {
		t.Errorf(`SENTINEL TRIPPED: memory instructions now mention learned-*; the agent is being pointed at consolidated rules.
Update the doc comment at the top of this file.
instr=%q`, instr)
	}
	if strings.Contains(instr, "{crew_slug}/topics") || strings.Contains(instr, "{crewSlug}/topics") {
		t.Errorf(`SENTINEL TRIPPED: memory instructions now name the crew-slug-qualified topics dir (where the consolidator actually writes).
Update the doc comment at the top of this file.
instr=%q`, instr)
	}
}

// TestLearnedRules_AgentCWDCannotDiscoverThem is the GAP 3 sentinel. It
// is a source-level check for the same reason
// internal/memory/hybrid_dead_code_test.go is: the runtime value of
// workDir is only observable by standing up a container, but the
// invariant we care about ("the agent's CWD is /output/{slug}, not
// anywhere under /crew/shared/.memory") is exactly a property of
// preparePreflightDirs' source.
//
// If the CWD ever moves under the crew memory tree, a CLI's own
// file-convention discovery could start picking learned-*.md up — a
// real behaviour change that deserves to be noticed here.
func TestLearnedRules_AgentCWDCannotDiscoverThem(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(".", "orchestrator_run.go"))
	if err != nil {
		t.Fatalf("reading orchestrator_run.go: %v", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "orchestrator_run.go", src, parser.AllErrors)
	if err != nil {
		t.Fatalf("parsing orchestrator_run.go: %v", err)
	}

	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name.Name != "preparePreflightDirs" {
			continue
		}
		body = fn.Body
		break
	}
	if body == nil {
		t.Fatalf("Orchestrator.preparePreflightDirs not found in orchestrator_run.go — renamed? update this sentinel.")
	}

	start := fset.Position(body.Pos()).Offset
	end := fset.Position(body.End()).Offset
	if start < 0 || end > len(src) || start >= end {
		t.Fatalf("preparePreflightDirs body offsets out of range: [%d,%d) in src len %d", start, end, len(src))
	}
	bodyText := string(src[start:end])

	for _, want := range []string{
		`outputDir := path.Join("/output", req.AgentSlug)`,
		`workDir := outputDir`,
	} {
		if !strings.Contains(bodyText, want) {
			t.Errorf(`agent CWD wiring changed: %q no longer present in preparePreflightDirs.
Re-check GAP 3: if the CWD now sits under /crew/shared/.memory, a CLI's own working-directory
file discovery (CLAUDE.md / AGENTS.md conventions) may reach learned-*.md and this sentinel
must be replaced with a positive delivery test.`, want)
		}
	}

	if strings.Contains(bodyText, "learned-") {
		t.Errorf(`SENTINEL TRIPPED: preparePreflightDirs now references learned-* — the preflight is staging
consolidated rules into the agent's working directory. Update the doc comment at the top of this file.`)
	}
}
