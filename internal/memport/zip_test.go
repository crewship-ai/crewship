package memport

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
)

func readZip(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		body, _ := io.ReadAll(rc)
		_ = rc.Close()
		out[f.Name] = string(body)
	}
	return out
}

// The download must BE an OKF bundle — same frontmatter, same manifest —
// so a browser download and a `crewship memory export` are the same
// artifact. Anything less and the format quietly forks in two.
func TestWriteOKFZipIsTheSameBundle(t *testing.T) {
	docs := []Doc{
		{Tier: memory.TierAgent, Scope: ScopeAgent, RelPath: "AGENT.md", Title: "Long-term", Body: []byte("knowledge\n")},
		{Tier: memory.TierAgent, Scope: ScopeAgent, RelPath: "daily/2026-08-01.md", Body: []byte("today\n")},
	}
	var buf bytes.Buffer
	if err := WriteOKFZip(&buf, docs, "agent alex"); err != nil {
		t.Fatalf("WriteOKFZip: %v", err)
	}
	files := readZip(t, buf.Bytes())

	if _, ok := files[manifestName]; !ok {
		t.Fatalf("no manifest; entries = %v", files)
	}
	if !strings.Contains(files[manifestName], "scope: agent alex") {
		t.Errorf("manifest does not record the scope:\n%s", files[manifestName])
	}
	agent := files["AGENT.md"]
	for _, want := range []string{"type: agent", "crewship_path: AGENT.md", "title: Long-term", "knowledge"} {
		if !strings.Contains(agent, want) {
			t.Errorf("AGENT.md missing %q:\n%s", want, agent)
		}
	}
	if !strings.Contains(files["daily/2026-08-01.md"], "today") {
		t.Errorf("nested document lost: %q", files["daily/2026-08-01.md"])
	}
}

// A bundle a browser downloads is one somebody may re-import. A path
// that does not satisfy Doc.RelPath's invariant must not be written
// into it at all.
func TestWriteOKFZipRefusesHostilePaths(t *testing.T) {
	for _, bad := range []string{"../escape.md", "/etc/passwd", "a//b.md"} {
		var buf bytes.Buffer
		err := WriteOKFZip(&buf, []Doc{{Tier: memory.TierAgent, RelPath: bad, Body: []byte("x")}}, "")
		if err == nil {
			t.Errorf("WriteOKFZip accepted %q", bad)
		}
	}
}

// Two downloads of unchanged memory must be the same bytes, or a bundle
// kept in git shows a diff on every fetch.
func TestWriteOKFZipIsDeterministic(t *testing.T) {
	docs := []Doc{{Tier: memory.TierCrew, Scope: ScopeCrew, RelPath: "CREW.md", Body: []byte("ship on thursdays\n")}}
	var a, b bytes.Buffer
	if err := WriteOKFZip(&a, docs, "crew eng"); err != nil {
		t.Fatal(err)
	}
	if err := WriteOKFZip(&b, docs, "crew eng"); err != nil {
		t.Fatal(err)
	}
	fa, fb := readZip(t, a.Bytes()), readZip(t, b.Bytes())
	for name, body := range fa {
		if fb[name] != body {
			t.Errorf("%s differs between two exports of the same memory", name)
		}
	}
}
