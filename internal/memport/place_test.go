package memport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
)

type recordingPlacer struct {
	staging string
	rels    []string
	err     error
	bodies  map[string]string
}

func (p *recordingPlacer) Place(_ context.Context, stagingDir string, rels []string) error {
	p.staging = stagingDir
	p.rels = append([]string{}, rels...)
	p.bodies = map[string]string{}
	for _, r := range rels {
		b, err := os.ReadFile(filepath.Join(stagingDir, filepath.FromSlash(r)))
		if err == nil {
			p.bodies[r] = string(b)
		}
	}
	return p.err
}

// Policy still runs where it always ran: the documents reach the placer
// already validated, capped and scrubbed, written by memory.WriteFile.
func TestApplyViaStagesValidatedDocuments(t *testing.T) {
	p := &recordingPlacer{}
	docs := []Doc{
		{Tier: memory.TierAgent, Scope: ScopeAgent, RelPath: "AGENT.md", Body: []byte("knowledge\n")},
		{Tier: memory.TierAgent, Scope: ScopeAgent, RelPath: "daily/2026-08-01.md", Body: []byte("today\n")},
	}
	res, err := ApplyVia(context.Background(), p, docs, memory.WriteConfig{})
	if err != nil {
		t.Fatalf("ApplyVia() error = %v", err)
	}
	if len(res.Written) != 2 {
		t.Fatalf("Written = %v (failed %+v), want both", res.Written, res.Failed)
	}
	sort.Strings(p.rels)
	if len(p.rels) != 2 || p.rels[0] != "AGENT.md" || p.rels[1] != "daily/2026-08-01.md" {
		t.Errorf("placer saw %v", p.rels)
	}
	if p.bodies["AGENT.md"] != "knowledge\n" {
		t.Errorf("staged body = %q", p.bodies["AGENT.md"])
	}
	// The staging directory is temporary and must not survive the call.
	if _, err := os.Stat(p.staging); !os.IsNotExist(err) {
		t.Error("the staging directory outlived ApplyVia")
	}
}

// A document the policy refuses never reaches the placer.
func TestApplyViaDoesNotStageRefusedDocuments(t *testing.T) {
	p := &recordingPlacer{}
	res, err := ApplyVia(context.Background(), p,
		[]Doc{{Tier: memory.TierLearned, Scope: ScopeCrew, RelPath: "lessons.md", Body: []byte("x")}},
		memory.WriteConfig{})
	if err != nil {
		t.Fatalf("ApplyVia() error = %v", err)
	}
	if len(res.Failed) != 1 {
		t.Fatalf("Failed = %+v, want the consolidator-owned file refused", res.Failed)
	}
	if len(p.rels) != 0 {
		t.Errorf("a refused document was handed to the placer: %v", p.rels)
	}
}

// If placement fails the documents are in a temp directory about to be
// deleted. Reporting them written because Apply succeeded would be a
// lie of exactly the shape this feature keeps having to remove.
func TestApplyViaReportsPlacementFailureAsNotWritten(t *testing.T) {
	boom := errors.New("container is not running")
	p := &recordingPlacer{err: boom}
	res, err := ApplyVia(context.Background(), p,
		[]Doc{{Tier: memory.TierAgent, Scope: ScopeAgent, RelPath: "AGENT.md", Body: []byte("x")}},
		memory.WriteConfig{})
	if err != nil {
		t.Fatalf("ApplyVia() hard error = %v, want a per-document failure", err)
	}
	if len(res.Written) != 0 {
		t.Errorf("Written = %v, want none — placement failed", res.Written)
	}
	if len(res.Failed) != 1 {
		t.Fatalf("Failed = %+v, want the document reported", res.Failed)
	}
	if !errors.Is(res.Failed[0].Cause, boom) {
		t.Errorf("cause = %v, want the placement error for the log", res.Failed[0].Cause)
	}
	if got := res.Failed[0].Reason; got == "" || filepath.IsAbs(got) {
		t.Errorf("reason = %q", got)
	}
}

func TestApplyViaRequiresAPlacer(t *testing.T) {
	if _, err := ApplyVia(context.Background(), nil, []Doc{{RelPath: "AGENT.md"}}, memory.WriteConfig{}); err == nil {
		t.Fatal("ApplyVia() with no placer returned nil error")
	}
}

type actionableErr struct{ msg string }

func (e actionableErr) Error() string           { return "internal: " + e.msg }
func (e actionableErr) OperatorMessage() string { return e.msg }

type actionablePlacer struct{ msg string }

func (p actionablePlacer) Place(context.Context, string, []string) error {
	return actionableErr{msg: p.msg}
}

// When the placer knows what the operator should DO, that has to reach
// them. Leaving it in the server log — which is where it first landed —
// tells the person who can fix it nothing.
func TestApplyViaSurfacesAnActionablePlacementFailure(t *testing.T) {
	p := actionablePlacer{msg: `crew "engineering" has no running container — start it and import again`}
	res, err := ApplyVia(context.Background(), p,
		[]Doc{{Tier: memory.TierAgent, Scope: ScopeAgent, RelPath: "AGENT.md", Body: []byte("x")}},
		memory.WriteConfig{})
	if err != nil {
		t.Fatalf("ApplyVia() hard error = %v", err)
	}
	if len(res.Failed) != 1 {
		t.Fatalf("Failed = %+v", res.Failed)
	}
	if res.Failed[0].Reason != p.msg {
		t.Errorf("Reason = %q, want the placer's operator message", res.Failed[0].Reason)
	}
	if res.Failed[0].Cause == nil {
		t.Error("the cause must still reach the log")
	}
}

// A transport error carries container ids and host paths, so it keeps
// the generic text.
func TestApplyViaKeepsOpaqueFailuresOpaque(t *testing.T) {
	p := &recordingPlacer{err: errors.New("Error response from daemon: container abc123 is not running")}
	res, err := ApplyVia(context.Background(), p,
		[]Doc{{Tier: memory.TierAgent, Scope: ScopeAgent, RelPath: "AGENT.md", Body: []byte("x")}},
		memory.WriteConfig{})
	if err != nil {
		t.Fatalf("ApplyVia() hard error = %v", err)
	}
	if strings.Contains(res.Failed[0].Reason, "abc123") {
		t.Errorf("a daemon error leaked to the operator: %q", res.Failed[0].Reason)
	}
}
