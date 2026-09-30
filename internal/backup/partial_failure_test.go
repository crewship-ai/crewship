package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// refuseWorkspace is a daemon that refuses to copy /workspace — the dev3
// shape: a stale container whose bind-mount source directory was deleted —
// and serves every other section.
type refuseWorkspace struct{ *fakeDockerOps }

func (r refuseWorkspace) CopyFrom(ctx context.Context, id, src string) (io.ReadCloser, error) {
	if src == ContainerWorkspacePath {
		return nil, errors.New("Error response from daemon: error while mounting volume: no such file or directory")
	}
	return r.fakeDockerOps.CopyFrom(ctx, id, src)
}

func TestCollectCrew_ARefusedSectionKeepsTheRestOfTheCrew(t *testing.T) {
	ops := refuseWorkspace{&fakeDockerOps{exists: true, otherPaths: map[string]map[string][]byte{
		ContainerCrewPath: {"agents/a/.memory/note.md": []byte("remember")},
	}}}
	var buf bytes.Buffer
	w, err := NewTarZstWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := CollectCrew(context.Background(), ops, w, CrewTarget{Slug: "lab", ContainerID: "c1"}, ScopeLevelQuick)
	if err != nil {
		t.Fatalf("a refused section failed the whole crew: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if len(capture.FailedSections) != 1 || !strings.HasPrefix(capture.FailedSections[0], ContainerWorkspacePath+": ") {
		t.Fatalf("FailedSections = %q", capture.FailedSections)
	}
	if capture.CrewMemoryFiles != 1 || capture.WorkspaceFiles != 0 {
		t.Fatalf("capture = %+v; want the memory kept and no workspace files", capture)
	}
	if ops.unpauseCalls != 1 {
		t.Fatalf("unpause calls = %d; the container must be unpaused", ops.unpauseCalls)
	}

	// The manifest says so, and the bundle is marked incomplete.
	tgt := &WorkspaceTarget{ID: "ws", Slug: "w", CrewTargets: []CrewTarget{{Slug: "lab"}}}
	contents := buildContents(tgt, ScopeLevelQuick, map[string]CrewCapture{"lab": capture})
	items := buildIncomplete(tgt, contents, nil)
	if len(items) != 1 || items[0].Kind != IncompleteCrewSectionFailed || contents.Crews[0].WorkspaceIncluded {
		t.Fatalf("incomplete = %+v, crew = %+v", items, contents.Crews[0])
	}
}

func TestCollectCrew_ACancelledCopyStillFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ops := refuseWorkspace{&fakeDockerOps{exists: true}}
	var buf bytes.Buffer
	w, _ := NewTarZstWriter(&buf)
	if _, err := CollectCrew(ctx, ops, w, CrewTarget{Slug: "lab", ContainerID: "c1"}, ScopeLevelQuick); err == nil {
		t.Fatal("a cancelled backup must not be recorded as a skipped section")
	}
}

func TestCopyTree_UnreadableFilesAreSkippedAndNamed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads mode-0000 files")
	}
	src, dst := t.TempDir(), t.TempDir()
	mustWrite := func(rel, body string, mode os.FileMode) {
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("ok.txt", "kept", 0o644)
	mustWrite("runs/secret/.gitignore", "container-owned", 0o000)
	locked := filepath.Join(src, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite("locked/inner.txt", "x", 0o644)
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	sc, err := newStagingCipher()
	if err != nil {
		t.Fatal(err)
	}
	entries, _, unreadable, err := copyTree(context.Background(), src, dst, nil, sc)
	if err != nil {
		t.Fatalf("an unreadable file failed the store copy: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "ok.txt" {
		t.Fatalf("entries = %+v", entries)
	}
	got := strings.Join(unreadable, ",")
	if !strings.Contains(got, "runs/secret/.gitignore") || !strings.Contains(got, "locked") {
		t.Fatalf("unreadable = %q", unreadable)
	}
}
