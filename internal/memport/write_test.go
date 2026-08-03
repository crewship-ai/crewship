package memport

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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

	// Re-read the way the CLI does: detect the format rather than
	// assuming one. A bundle is OKF — it carries headers a live tree
	// does not — and Detect is what has to know that.
	format, err := Detect(os.DirFS(dir))
	if err != nil {
		t.Fatalf("Detect() on our own bundle: %v", err)
	}
	if format != FormatOKF {
		t.Fatalf("Detect() = %q, want %q for an exported bundle", format, FormatOKF)
	}
	back, err := ReadSource(os.DirFS(dir), format, Options{})
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
// payload is operator-supplied and, upstream of that, comes from a
// directory somebody else produced.
//
// A refusal is per-document, not a hard error: one bad entry in a
// hundred must not decide the fate of the other ninety-nine.
func TestApplyRefusesEscape(t *testing.T) {
	for _, bad := range []string{"../escape.md", "daily/../../escape.md", "/etc/passwd"} {
		t.Run(bad, func(t *testing.T) {
			root := t.TempDir()
			res, err := Apply(context.Background(), root,
				[]Doc{{Tier: memory.TierAgent, RelPath: bad, Body: []byte("x")}},
				memory.WriteConfig{})
			if err != nil {
				t.Fatalf("Apply() hard error = %v, want a per-document refusal", err)
			}
			if len(res.Written) != 0 {
				t.Fatalf("Apply() accepted %q", bad)
			}
			if len(res.Failed) != 1 {
				t.Fatalf("Failed = %+v, want the refusal reported", res.Failed)
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

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
}

// Lexical confinement is not confinement. An agent owns its own
// .memory, so it can replace a subdirectory with a link to somewhere
// else — another crew's shared tree, say — and every path stays
// textually inside the root while the bytes land outside it.
func TestApplyRefusesSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	symlinkOrSkip(t, elsewhere, filepath.Join(root, "daily"))

	res, err := Apply(context.Background(), root,
		[]Doc{{Tier: memory.TierAgent, RelPath: "daily/2026-08-01.md", Body: []byte("stolen")}},
		memory.WriteConfig{})
	if err == nil && len(res.Failed) == 0 {
		t.Fatal("Apply wrote through a symlinked parent directory")
	}
	if _, serr := os.Stat(filepath.Join(elsewhere, "2026-08-01.md")); serr == nil {
		t.Fatal("content landed outside the memory root")
	}
}

// The same swap one level down: the file itself is the link.
func TestApplyRefusesSymlinkedLeaf(t *testing.T) {
	root := t.TempDir()
	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "victim.md")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, victim, filepath.Join(root, "AGENT.md"))

	res, err := Apply(context.Background(), root,
		[]Doc{{Tier: memory.TierAgent, RelPath: "AGENT.md", Body: []byte("overwrite")}},
		memory.WriteConfig{})
	if err == nil && len(res.Failed) == 0 {
		t.Fatal("Apply wrote through a symlinked leaf")
	}
	got, rerr := os.ReadFile(victim)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != "original" {
		t.Fatalf("symlink target was overwritten: %q", got)
	}
}

// Every other write door into memory refuses a path it does not
// recognise. An importer that accepts anything is a way to put files
// into the tree — .quarantine entries, nested daily forgeries — that no
// other surface in the product would have taken.
func TestApplyRefusesUnknownPaths(t *testing.T) {
	for _, bad := range []string{
		"daily/2026-08-01/notes.md", // nested forgery the sidecar rejects
		".quarantine/abc123.md",     // deliberately isolated content
		"secrets.md",
		"topics/../AGENT.md",
	} {
		t.Run(bad, func(t *testing.T) {
			root := t.TempDir()
			res, err := Apply(context.Background(), root,
				[]Doc{{Tier: memory.TierAgent, RelPath: bad, Body: []byte("x")}},
				memory.WriteConfig{})
			if err == nil && len(res.Failed) == 0 && len(res.Written) > 0 {
				t.Fatalf("Apply accepted unrecognised path %q", bad)
			}
		})
	}
}

// The consolidator owns lessons/learned files: they carry a YAML schema
// and their own locking. Replacing one with freeform markdown from an
// import destroys the store and every later WriteLesson fails on parse.
func TestApplyRefusesConsolidatorOwnedFiles(t *testing.T) {
	for _, owned := range []string{"lessons.md", "learned.md", "eng/topics/learned-ops.md"} {
		t.Run(owned, func(t *testing.T) {
			root := t.TempDir()
			res, err := Apply(context.Background(), root,
				[]Doc{{Tier: memory.TierLearned, RelPath: owned, Body: []byte("freeform\n")}},
				memory.WriteConfig{})
			if err == nil && len(res.Failed) == 0 && len(res.Written) > 0 {
				t.Fatalf("Apply overwrote consolidator-owned %q", owned)
			}
			if _, serr := os.Stat(filepath.Join(root, filepath.FromSlash(owned))); serr == nil {
				t.Errorf("%s was written", owned)
			}
		})
	}
}

// The crew's pinned notes live under <slug>/topics/pins.md and must
// round-trip; they are the one nested path the importer accepts.
func TestApplyAcceptsCrewTopicsPins(t *testing.T) {
	root := t.TempDir()
	res, err := Apply(context.Background(), root,
		[]Doc{{Tier: memory.TierPins, RelPath: "engineering/topics/pins.md", Body: []byte("- never force-push\n")}},
		memory.WriteConfig{})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(res.Written) != 1 {
		t.Fatalf("Written = %v (failed: %+v), want the crew pins file", res.Written, res.Failed)
	}
}

// A write that fails partway must not be reported as "nothing
// happened": the documents already replaced are gone, and the operator
// needs to know which.
func TestApplyReportsPerDocumentFailureWithoutAborting(t *testing.T) {
	root := t.TempDir()
	// Make PERSONA.md a directory so its write fails while its
	// neighbours succeed.
	if err := os.MkdirAll(filepath.Join(root, "PERSONA.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	docs := []Doc{
		{Tier: memory.TierAgent, RelPath: "AGENT.md", Body: []byte("first\n")},
		{Tier: memory.TierAgent, RelPath: "PERSONA.md", Body: []byte("second\n")},
		{Tier: memory.TierAgent, RelPath: "pins.md", Body: []byte("third\n")},
	}
	res, err := Apply(context.Background(), root, docs, memory.WriteConfig{})
	if err != nil {
		t.Fatalf("Apply() returned a hard error instead of per-document failures: %v", err)
	}
	if len(res.Failed) != 1 || res.Failed[0].RelPath != "PERSONA.md" {
		t.Fatalf("Failed = %+v, want PERSONA.md", res.Failed)
	}
	// The documents after the failure still ran.
	if len(res.Written) != 2 {
		t.Errorf("Written = %v, want AGENT.md and pins.md", res.Written)
	}
}
