package memport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/crewship-ai/crewship/internal/memory"
)

// A live .memory tree exports to an OKF bundle and imports back to
// byte-identical content. Round-trip is the property that makes the
// bundle a backup an operator can actually rely on; without it the
// export is a lossy screenshot.
func TestExportImportRoundTrip(t *testing.T) {
	src := fstest.MapFS{
		"AGENT.md":            &fstest.MapFile{Data: []byte("The deploy key rotates monthly.\n")},
		"CREW.md":             &fstest.MapFile{Data: []byte("Ship on Thursdays.\n")},
		"pins.md":             &fstest.MapFile{Data: []byte("- never force-push main\n")},
		"PERSONA.md":          &fstest.MapFile{Data: []byte("Be terse.\n")},
		"daily/2026-08-01.md": &fstest.MapFile{Data: []byte("Shipped the parser.\n")},
		"peers/pavel.md":      &fstest.MapFile{Data: []byte("Prefers Czech.\n")},
	}

	plan, err := ReadSource(src, FormatCrewship, Options{})
	if err != nil {
		t.Fatalf("ReadSource() error = %v", err)
	}

	dir := t.TempDir()
	if err := ExportOKF(dir, plan.Docs); err != nil {
		t.Fatalf("ExportOKF() error = %v", err)
	}

	back, err := ReadSource(os.DirFS(dir), FormatCrewship, Options{})
	if err != nil {
		t.Fatalf("re-read error = %v", err)
	}

	if len(back.Docs) != len(plan.Docs) {
		t.Fatalf("round trip changed the document count: %d -> %d (%v)",
			len(plan.Docs), len(back.Docs), relPaths(back))
	}
	got := map[string]Doc{}
	for _, d := range back.Docs {
		got[d.RelPath] = d
	}
	for _, want := range plan.Docs {
		g, ok := got[want.RelPath]
		if !ok {
			t.Errorf("%s missing after round trip; have %v", want.RelPath, relPaths(back))
			continue
		}
		if strings.TrimSpace(string(g.Body)) != strings.TrimSpace(string(want.Body)) {
			t.Errorf("%s body changed:\n want %q\n  got %q", want.RelPath, want.Body, g.Body)
		}
		if g.Tier != want.Tier {
			t.Errorf("%s tier %q -> %q", want.RelPath, want.Tier, g.Tier)
		}
	}
}

// Exporting the same memory twice must produce identical bytes, or a
// bundle kept in git shows a diff on every run and stops being
// reviewable.
func TestExportIsDeterministic(t *testing.T) {
	docs := []Doc{
		{Tier: memory.TierAgent, RelPath: "AGENT.md", Title: "Long-term", Tags: []string{"ops", "deploy"},
			Body: []byte("a\n")},
		{Tier: memory.TierCrew, RelPath: "CREW.md", Body: []byte("b\n")},
	}
	first, second := t.TempDir(), t.TempDir()
	if err := ExportOKF(first, docs); err != nil {
		t.Fatalf("ExportOKF() error = %v", err)
	}
	if err := ExportOKF(second, docs); err != nil {
		t.Fatalf("ExportOKF() error = %v", err)
	}
	for _, name := range []string{"AGENT.md", "CREW.md", "okf.yaml"} {
		a, err := os.ReadFile(filepath.Join(first, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		b, err := os.ReadFile(filepath.Join(second, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(a) != string(b) {
			t.Errorf("%s differs between two exports of the same input:\n%s\n---\n%s", name, a, b)
		}
	}
}

// Apply is the only writer, and it goes through memory.WriteFile so the
// caps, the scrubber and the atomic replace all still apply. This test
// pins the outcome an operator sees; the chokepoint itself is tested in
// internal/memory.
func TestApplyWritesThroughMemoryWriter(t *testing.T) {
	root := t.TempDir()
	docs := []Doc{
		{Tier: memory.TierAgent, RelPath: "AGENT.md", Body: []byte("knowledge\n")},
		{Tier: memory.TierAgent, RelPath: "daily/2026-08-01.md", Body: []byte("today\n")},
	}

	res, err := Apply(context.Background(), root, docs, memory.WriteConfig{})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(res.Written) != 2 {
		t.Fatalf("Written = %v, want 2 files", res.Written)
	}
	for _, d := range docs {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(d.RelPath)))
		if err != nil {
			t.Fatalf("read back %s: %v", d.RelPath, err)
		}
		if string(b) != string(d.Body) {
			t.Errorf("%s = %q, want %q", d.RelPath, b, d.Body)
		}
	}
}

// A rejected write (over the cap) is a reported outcome, not a silent
// drop and not a half-applied import.
func TestApplyReportsRejections(t *testing.T) {
	root := t.TempDir()
	docs := []Doc{
		{Tier: memory.TierAgent, RelPath: "AGENT.md", Body: []byte(strings.Repeat("x", 100))},
	}
	res, err := Apply(context.Background(), root, docs, memory.WriteConfig{MaxBytes: 10})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(res.Written) != 0 {
		t.Errorf("Written = %v, want none", res.Written)
	}
	if len(res.Rejected) != 1 || res.Rejected[0].RelPath != "AGENT.md" {
		t.Fatalf("Rejected = %+v, want AGENT.md", res.Rejected)
	}
	if res.Rejected[0].Kind != "cap" {
		t.Errorf("rejection kind = %q, want %q", res.Rejected[0].Kind, "cap")
	}
	if _, err := os.Stat(filepath.Join(root, "AGENT.md")); !os.IsNotExist(err) {
		t.Error("rejected content reached disk")
	}
}

// A crafted RelPath must not escape the target .memory directory. The
// source of an import is a tarball a stranger produced.
func TestApplyRefusesEscape(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"../escape.md", "daily/../../escape.md", "/etc/passwd"} {
		t.Run(bad, func(t *testing.T) {
			_, err := Apply(context.Background(), root,
				[]Doc{{Tier: memory.TierAgent, RelPath: bad, Body: []byte("x")}},
				memory.WriteConfig{})
			if err == nil {
				t.Fatalf("Apply() accepted %q", bad)
			}
			if _, serr := os.Stat(filepath.Join(filepath.Dir(root), "escape.md")); serr == nil {
				t.Fatal("write landed outside the target directory")
			}
		})
	}
}

// An import runs under the same ceilings an agent's own writes do.
// Without this an operator could put a 40 KB AGENT.md into memory that
// the agent itself would be refused, and the next prompt build would
// carry it.
func TestApplyEnforcesCanonicalCaps(t *testing.T) {
	root := t.TempDir()
	cap, ok := memory.CapForPath("AGENT.md")
	if !ok {
		t.Fatal("AGENT.md has no canonical cap")
	}
	docs := []Doc{{
		Tier:    memory.TierAgent,
		RelPath: "AGENT.md",
		Body:    []byte(strings.Repeat("x", cap+1)),
	}}

	res, err := Apply(context.Background(), root, docs, memory.WriteConfig{})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(res.Rejected) != 1 || res.Rejected[0].Kind != "cap" {
		t.Fatalf("Rejected = %+v, want one cap rejection", res.Rejected)
	}
	if _, err := os.Stat(filepath.Join(root, "AGENT.md")); !os.IsNotExist(err) {
		t.Error("over-cap content reached disk")
	}
}

// Paths with no specific rule — the consolidator's per-crew
// <slug>/topics/ tree is the real example — still round-trip, but under
// the widest ceiling any memory file has rather than none at all.
func TestApplyCapsUnrecognisedPaths(t *testing.T) {
	root := t.TempDir()
	rel := "alpha-crew/topics/learned-deploys.md"

	small, err := Apply(context.Background(), root,
		[]Doc{{Tier: memory.TierLearned, RelPath: rel, Body: []byte("a note\n")}},
		memory.WriteConfig{})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(small.Written) != 1 {
		t.Fatalf("Written = %v, want the topics file to round-trip", small.Written)
	}

	big, err := Apply(context.Background(), root,
		[]Doc{{Tier: memory.TierLearned, RelPath: rel, Body: []byte(strings.Repeat("x", defaultImportCap+1))}},
		memory.WriteConfig{})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(big.Rejected) != 1 || big.Rejected[0].Kind != "cap" {
		t.Fatalf("Rejected = %+v, want a cap rejection on an unbounded path", big.Rejected)
	}
}

// An explicit cap from the caller still wins — that is how the CLI's
// escape hatch and these tests pin a smaller ceiling.
func TestApplyExplicitCapOverridesCanonical(t *testing.T) {
	root := t.TempDir()
	res, err := Apply(context.Background(), root,
		[]Doc{{Tier: memory.TierAgent, RelPath: "AGENT.md", Body: []byte("0123456789abc")}},
		memory.WriteConfig{MaxBytes: 5})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(res.Rejected) != 1 {
		t.Fatalf("Rejected = %+v, want the caller's tighter cap to apply", res.Rejected)
	}
}
