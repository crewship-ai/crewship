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
