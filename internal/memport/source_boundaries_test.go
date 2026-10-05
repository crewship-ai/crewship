package memport

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/crewship-ai/crewship/internal/memory"
)

type failingSource struct {
	fs.FS
	target   string
	failRead bool
}

func (f failingSource) Open(name string) (fs.File, error) {
	if name != f.target {
		return f.FS.Open(name)
	}
	if !f.failRead {
		return nil, fs.ErrPermission
	}
	file, err := f.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return failingSourceFile{File: file}, nil
}

type failingSourceFile struct{ fs.File }

func (f failingSourceFile) Read([]byte) (int, error) { return 0, fs.ErrPermission }

func TestSourceReadFailuresAreReportedWithoutInventingMemory(t *testing.T) {
	for _, tc := range []struct {
		format Format
		name   string
	}{
		{FormatCrewship, "AGENT.md"}, {FormatOKF, "note.md"}, {FormatNanoClaw, "groups/work/CLAUDE.md"}, {FormatOpenClaw, "MEMORY.md"},
	} {
		for _, readFailure := range []bool{false, true} {
			t.Run(string(tc.format)+map[bool]string{false: "/open", true: "/read"}[readFailure], func(t *testing.T) {
				base := fstest.MapFS{tc.name: &fstest.MapFile{Data: []byte("memory content")}}
				source := failingSource{FS: base, target: tc.name, failRead: readFailure}
				plan, err := ReadSource(source, tc.format, Options{})
				if err != nil {
					t.Fatal(err)
				}
				if len(plan.Docs) != 0 || len(plan.Skipped) != 1 || plan.Skipped[0].Source != tc.name || !strings.Contains(plan.Skipped[0].Reason, "unreadable") {
					t.Fatalf("read failure hidden: %+v", plan)
				}
				if sniffHasFrontmatter(source, tc.name) {
					t.Fatal("unreadable file classified as frontmatter")
				}
			})
		}
	}
	broken := failingSource{FS: fstest.MapFS{}, target: "."}
	if _, err := Detect(broken); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("walk failure disguised as unknown format: %v", err)
	}
	if _, err := ReadSource(broken, FormatCrewship, Options{}); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("walk failure hidden: %v", err)
	}
	if _, err := ReadSource(fstest.MapFS{}, Format("unknown"), Options{}); err == nil {
		t.Fatal("unknown format accepted")
	}
}

func TestFrontmatterIncompleteHeadersPreserveOriginalDocument(t *testing.T) {
	for _, raw := range []string{"ordinary markdown", "---not a delimiter\nbody", "---\ntitle: unfinished", "---\ntitle: unfinished\n", "---"} {
		fm, body, err := parseFrontmatter([]byte(raw))
		if err != nil || fm.Title != "" || string(body) != raw {
			t.Fatalf("incomplete header consumed content: %q %+v %q %v", raw, fm, body, err)
		}
	}
	for _, raw := range []string{"---\ntitle: Example\n---", "\ufeff---\r\ntitle: Example\r\n---\r\nbody"} {
		fm, _, err := parseFrontmatter([]byte(raw))
		if err != nil || fm.Title != "Example" {
			t.Fatalf("valid BOM/CRLF/header-only document rejected: %+v %v", fm, err)
		}
	}
	plan, err := ReadSource(fstest.MapFS{"note.md": &fstest.MapFile{Data: []byte("---\ntags: [unterminated\n---\nRemember this.")}}, FormatOKF, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Skipped) != 1 || !strings.Contains(string(docFor(t, plan, "AGENT.md").Body), "Remember this.") {
		t.Fatalf("malformed metadata lost the body or warning: %+v", plan)
	}
}

func TestForeignTiersAndScopeRemainExplicit(t *testing.T) {
	for _, tc := range []struct {
		kind, path string
		tier       memory.Tier
		scope      Scope
	}{
		{"crew", "CREW.md", memory.TierCrew, ScopeCrew}, {"workspace", "CREW.md", memory.TierWorkspace, ScopeCrew},
		{"pins", "pins.md", memory.TierPins, ScopeAgent}, {"learned", "learned.md", memory.TierLearned, ScopeAgent},
		{"foreign-table", "AGENT.md", memory.TierAgent, ScopeAgent},
	} {
		plan, err := ReadSource(fstest.MapFS{"note.md": &fstest.MapFile{Data: []byte("---\ntype: " + tc.kind + "\n---\nContent\n")}}, FormatOKF, Options{})
		if err != nil {
			t.Fatal(err)
		}
		doc := docFor(t, plan, tc.path)
		if doc.Tier != tc.tier || doc.Scope != tc.scope {
			t.Fatalf("foreign tier mapped to wrong destination: %+v", doc)
		}
	}
	if got := scopeForCrewshipPath("AGENT.md", ""); got != ScopeAgent {
		t.Fatalf("default scope=%q", got)
	}
	plan, err := ReadSource(fstest.MapFS{"groups/global/CLAUDE.md": &fstest.MapFile{Data: []byte("Shared")}}, FormatNanoClaw, Options{})
	if err != nil || len(plan.Docs) != 1 || plan.Docs[0].Scope != ScopeCrew {
		t.Fatalf("global-only memory lost: %+v %v", plan, err)
	}
	if _, err := ReadSource(fstest.MapFS{"groups/work/CLAUDE.md": &fstest.MapFile{Data: []byte("Work")}}, FormatNanoClaw, Options{Group: "missing"}); err == nil {
		t.Fatal("missing group silently selected another conversation")
	}
}

func TestSecureMemoryReadsRejectTraversalAndMissingEntries(t *testing.T) {
	source := SecureDirFS(t.TempDir())
	for _, name := range []string{"../other-crew/AGENT.md", "/etc/passwd", "daily/../AGENT.md", "missing.md"} {
		if file, err := source.Open(name); err == nil {
			file.Close()
			t.Fatalf("Open accepted %q", name)
		}
		if _, err := fs.ReadDir(source, name); err == nil {
			t.Fatalf("ReadDir accepted %q", name)
		}
		if _, err := fs.ReadFile(source, name); err == nil {
			t.Fatalf("ReadFile accepted %q", name)
		}
	}
}

func TestSecureMemoryDirectoryReadsKeepLexicalOrder(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"z.md", "a.md", "m.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := fs.ReadDir(SecureDirFS(root), ".")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if strings.Join(names, ",") != "a.md,m.md,z.md" {
		t.Fatalf("non-deterministic export order: %v", names)
	}
}
