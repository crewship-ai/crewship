package backup

import (
	"archive/tar"
	"bytes"
	"io"
	"strings"
	"testing"
)

func tarWith(t *testing.T, entries []tar.Header, bodies map[string]string) io.ReadCloser {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := range entries {
		h := entries[i]
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(bodies[h.Name]))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(bodies[h.Name])); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return io.NopCloser(bytes.NewReader(buf.Bytes()))
}

func readTar(t *testing.T, r io.Reader) (names []string, bodies map[string]string) {
	t.Helper()
	bodies = map[string]string{}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return names, bodies
		}
		if err != nil {
			t.Fatalf("read filtered tar: %v", err)
		}
		names = append(names, h.Name)
		b, _ := io.ReadAll(tr)
		bodies[h.Name] = string(b)
	}
}

// The crew section carries directories owned by the agent and files
// owned by the sidecar. No one identity can chmod/utime both, so tar
// exits 2 partway through. Dropping the directory entries removes the
// conflict (#1746).
func TestFilesOnlyTarDropsDirectories(t *testing.T) {
	src := tarWith(t, []tar.Header{
		{Name: "agents/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "agents/alex/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "agents/alex/.memory/", Typeflag: tar.TypeDir, Mode: 0o2775},
		{Name: "agents/alex/.memory/AGENT.md", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "shared/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "shared/.memory/CREW.md", Typeflag: tar.TypeReg, Mode: 0o664},
	}, map[string]string{
		"agents/alex/.memory/AGENT.md": "knowledge\n",
		"shared/.memory/CREW.md":       "shared\n",
	})

	names, bodies := readTar(t, filesOnlyTar(src))

	for _, n := range names {
		if strings.HasSuffix(n, "/") {
			t.Errorf("directory entry survived: %q", n)
		}
	}
	if len(names) != 2 {
		t.Fatalf("entries = %v, want the two files", names)
	}
	if bodies["agents/alex/.memory/AGENT.md"] != "knowledge\n" {
		t.Errorf("content lost: %q", bodies["agents/alex/.memory/AGENT.md"])
	}
	if bodies["shared/.memory/CREW.md"] != "shared\n" {
		t.Errorf("content lost: %q", bodies["shared/.memory/CREW.md"])
	}
}

// An empty section must stay empty rather than becoming a malformed
// stream the extractor cannot read.
func TestFilesOnlyTarHandlesDirectoryOnlyInput(t *testing.T) {
	src := tarWith(t, []tar.Header{
		{Name: "agents/", Typeflag: tar.TypeDir, Mode: 0o755},
	}, nil)
	names, _ := readTar(t, filesOnlyTar(src))
	if len(names) != 0 {
		t.Errorf("entries = %v, want none", names)
	}
}

// A read error must surface rather than truncating the section into a
// silently short restore.
func TestFilesOnlyTarPropagatesReadErrors(t *testing.T) {
	src := io.NopCloser(strings.NewReader("this is not a tar archive at all"))
	_, err := io.ReadAll(filesOnlyTar(src))
	if err == nil {
		t.Fatal("a corrupt source produced no error — a short restore would look complete")
	}
}

// The preflight refuses a section when a directory it writes into is
// unwritable. "Writes into" has to mean the directories tar actually
// opens — the immediate parent of each file, plus directories the
// archive itself creates and their ancestors. Counting every ancestor
// of every file refused restores over directories nothing touches
// (#1746): the crew memory section writes into `agents/<slug>/.memory`,
// which is group-writable by design, while `agents/` above it is not.
func TestSectionWriteDirsCountsOnlyWhatTarOpens(t *testing.T) {
	src := tarWith(t, []tar.Header{
		{Name: "agents/alex/.memory/AGENT.md", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "shared/.memory/CREW.md", Typeflag: tar.TypeReg, Mode: 0o664},
	}, map[string]string{
		"agents/alex/.memory/AGENT.md": "a\n",
		"shared/.memory/CREW.md":       "b\n",
	})

	dirs, paths, err := sectionWriteDirs(src)
	if err != nil {
		t.Fatalf("sectionWriteDirs: %v", err)
	}
	for _, want := range []string{"agents/alex/.memory", "shared/.memory"} {
		if !dirs[want] {
			t.Errorf("immediate parent %q missing from writesInto: %v", want, dirs)
		}
	}
	for _, notWanted := range []string{"agents", "agents/alex", "shared"} {
		if dirs[notWanted] {
			t.Errorf("%q counted as written-into; tar never opens it when the child exists", notWanted)
		}
	}
	if !paths["agents/alex/.memory/AGENT.md"] {
		t.Errorf("file path missing from writesPaths: %v", paths)
	}
}

// A directory the ARCHIVE creates is different: tar makes it, so every
// ancestor above it may have to be made too.
func TestSectionWriteDirsKeepsAncestorsOfDirectoryEntries(t *testing.T) {
	src := tarWith(t, []tar.Header{
		{Name: "a/b/c/", Typeflag: tar.TypeDir, Mode: 0o755},
	}, nil)

	dirs, _, err := sectionWriteDirs(src)
	if err != nil {
		t.Fatalf("sectionWriteDirs: %v", err)
	}
	for _, want := range []string{"a/b/c", "a/b", "a"} {
		if !dirs[want] {
			t.Errorf("%q missing: a directory entry may need its ancestors created: %v", want, dirs)
		}
	}
}
