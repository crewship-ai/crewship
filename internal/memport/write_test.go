package memport

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/crewship-ai/crewship/internal/memory"
)

// expectRefused pins the contract Apply promises for a document it will
// not write: no hard error, nothing written, and the refusal REPORTED.
//
// The tests here used to join those with &&, which also passes when the
// document vanishes with no report, or when Apply returns a hard error
// and abandons the rest of the batch — two outcomes the design forbids.
func expectRefused(t *testing.T, res ApplyResult, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: Apply() hard error = %v, want a per-document refusal", what, err)
	}
	if len(res.Written) != 0 {
		t.Fatalf("%s: written = %v, want none", what, res.Written)
	}
	if len(res.Failed) != 1 {
		t.Fatalf("%s: failed = %+v, want exactly one reported refusal", what, res.Failed)
	}
	if res.Failed[0].Reason == "" {
		t.Errorf("%s: refusal carries no reason", what)
	}
}

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
			expectRefused(t, res, err, bad)
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
			expectRefused(t, res, err, owned)
			if _, serr := os.Stat(filepath.Join(root, filepath.FromSlash(owned))); serr == nil {
				t.Errorf("%s was written", owned)
			}
		})
	}
}

// The crew's pinned notes live under <slug>/topics/pins.md and must
// round-trip into the crew tree they came from.
func TestApplyAcceptsCrewTopicsPins(t *testing.T) {
	root := t.TempDir()
	res, err := Apply(context.Background(), root,
		[]Doc{{Tier: memory.TierPins, Scope: ScopeCrew, RelPath: "engineering/topics/pins.md",
			Body: []byte("- never force-push\n")}},
		memory.WriteConfig{})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(res.Written) != 1 {
		t.Fatalf("Written = %v (failed: %+v), want the crew pins file", res.Written, res.Failed)
	}
}

// The same shape aimed at an AGENT tree is a directory the product never
// creates there. Accepting it on the strength of the spelling let an
// import invent a directory inside one agent's memory, which the FTS
// indexer then walks and serves back into that agent's context.
func TestApplyRefusesCrewTopicsShapeOutsideCrewScope(t *testing.T) {
	for _, sc := range []Scope{ScopeAgent, ""} {
		t.Run(string(sc), func(t *testing.T) {
			root := t.TempDir()
			res, err := Apply(context.Background(), root,
				[]Doc{{Tier: memory.TierPins, Scope: sc, RelPath: "engineering/topics/pins.md",
					Body: []byte("x")}},
				memory.WriteConfig{})
			expectRefused(t, res, err, "crew-topics shape at scope "+string(sc))
			if _, serr := os.Stat(filepath.Join(root, "engineering")); serr == nil {
				t.Error("the invented directory was created anyway")
			}
		})
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

// Re-exporting into the same directory is the workflow this feature is
// sold on: a bundle kept in git only shows a useful diff if you can
// refresh it in place. Refusing a non-empty destination broke that.
func TestExportOKFRefreshesInPlace(t *testing.T) {
	dir := t.TempDir()
	first := []Doc{
		{Tier: memory.TierAgent, Scope: ScopeAgent, RelPath: "AGENT.md", Body: []byte("v1\n")},
		{Tier: memory.TierAgent, Scope: ScopeAgent, RelPath: "daily/2026-08-01.md", Body: []byte("monday\n")},
	}
	if err := ExportOKF(dir, first); err != nil {
		t.Fatalf("first export: %v", err)
	}
	// Something the operator keeps alongside the bundle.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	second := []Doc{{Tier: memory.TierAgent, Scope: ScopeAgent, RelPath: "AGENT.md", Body: []byte("v2\n")}}
	if err := ExportOKF(dir, second); err != nil {
		t.Fatalf("re-export into the same directory: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "AGENT.md"))
	if err != nil || !strings.Contains(string(got), "v2") {
		t.Errorf("AGENT.md not refreshed: %q (%v)", got, err)
	}
	// A document dropped from memory since the last export must not sit
	// in the directory looking current.
	if _, err := os.Stat(filepath.Join(dir, "daily", "2026-08-01.md")); !os.IsNotExist(err) {
		t.Error("a stale document from the previous bundle survived the re-export")
	}
	// Anything this function did not write is not its business.
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Error("the export removed a file it never wrote")
	}
}

// With no previous manifest there is nothing to prune, and a directory
// this function has never written to must be left alone.
func TestExportOKFLeavesForeignDirectoriesAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ExportOKF(dir, []Doc{{Tier: memory.TierAgent, RelPath: "AGENT.md", Body: []byte("x\n")}}); err != nil {
		t.Fatalf("ExportOKF into a directory with unrelated files: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Error("an unrelated file was removed")
	}
}

// A failure to LOOK is not a refusal to write. EnsureDirNoFollow
// returns errors for stat and mkdir problems too, and reporting those
// as "path does not stay inside the memory directory" answered a
// security-shaped question with a filesystem-shaped cause (#1741).
func TestConfinementReasonSeparatesRefusalFromFailure(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantPart string
	}{
		{"symlinked directory", errors.New("refusing symlinked memory directory: daily"), "symlink"},
		{"escape", errors.New("directory escapes memory root: x"), "does not stay inside"},
		{"not a directory", errors.New("memory path component is not a directory: peers"), "does not stay inside"},
		{"permission denied", fmt.Errorf("create memory directory peers: %w", fs.ErrPermission), "could not prepare"},
		{"stat failed", errors.New("stat memory directory peers: input/output error"), "could not prepare"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := confinementReason(tt.err)
			if !strings.Contains(got, tt.wantPart) {
				t.Errorf("confinementReason(%v) = %q, want it to mention %q", tt.err, got, tt.wantPart)
			}
			if strings.Contains(got, "/") {
				t.Errorf("reason leaks a path: %q", got)
			}
		})
	}
}

// The cause must reach the caller of Apply so the server can log it,
// and must never be the thing the operator is shown.
func TestApplyCarriesTheCauseForTheLog(t *testing.T) {
	root := t.TempDir()
	// A file where a directory needs to be: the write cannot succeed.
	if err := os.WriteFile(filepath.Join(root, "daily"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(context.Background(), root,
		[]Doc{{Tier: memory.TierAgent, Scope: ScopeAgent, RelPath: "daily/2026-08-01.md", Body: []byte("x")}},
		memory.WriteConfig{})
	if err != nil {
		t.Fatalf("Apply() hard error = %v", err)
	}
	if len(res.Failed) != 1 {
		t.Fatalf("Failed = %+v, want one", res.Failed)
	}
	if res.Failed[0].Cause == nil {
		t.Error("the underlying cause was discarded — the failure is undiagnosable from either side")
	}
	if strings.Contains(res.Failed[0].Reason, root) {
		t.Errorf("the operator-facing reason leaks the host path: %q", res.Failed[0].Reason)
	}
}
