package backup

// Coverage tests for restorer.go — ExtractPayload section routing +
// rejection rules, the Open* accessors, RestoreCrew's two copy
// strategies and error aggregation, splitFirst and storageOrDefault.

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// payloadEntry describes one entry for buildPayloadTarZst.
type payloadEntry struct {
	name     string
	body     []byte
	typeflag byte   // 0 → TypeReg
	linkname string // for symlink/link entries
}

func buildPayloadTarZst(t *testing.T, entries []payloadEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw, err := NewTarZstWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		tf := e.typeflag
		if tf == 0 {
			tf = tar.TypeReg
		}
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     0o644,
			ModTime:  time.Unix(0, 0),
			Typeflag: tf,
			Linkname: e.linkname,
		}
		if tf == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.tw.WriteHeader(hdr); err != nil {
			t.Fatalf("header %s: %v", e.name, err)
		}
		if tf == tar.TypeReg && len(e.body) > 0 {
			if _, err := tw.tw.Write(e.body); err != nil {
				t.Fatalf("body %s: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// readPlainTar decodes a plain (uncompressed) tar into name → body.
func readPlainTar(t *testing.T, r io.Reader) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		body, _ := io.ReadAll(tr)
		out[hdr.Name] = string(body)
	}
	return out
}

func TestExtractPayload_FullLayout(t *testing.T) {
	ctx := context.Background()
	dumpJSON := []byte(`{"workspace_id":"ws1","tables":{"workspaces":[{"id":"ws1","slug":"alpha-ws"}]}}`)
	payload := buildPayloadTarZst(t, []payloadEntry{
		{name: "db/dump.json", body: dumpJSON},
		{name: "devcontainer/alpha/devcontainer.json", body: []byte(`{"image":"x"}`)},
		{name: "devcontainer/alpha/mise.toml", body: []byte("[tools]")},
		{name: "devcontainer/alpha/extras.txt", body: []byte("ignored file kind")},
		{name: "devcontainer/short", body: []byte("no slug/file split — skipped")},
		{name: "workspace/alpha/main.go", body: []byte("package main")},
		{name: "workspace/no-slash-entry", body: []byte("skipped: slug only")},
		{name: "volumes/alpha/home/.bashrc", body: []byte("export X=1")},
		{name: "volumes/alpha/tools/run.sh", body: []byte("#!/bin/sh")},
		{name: "volumes/alpha", body: []byte("skipped: missing vol")},
		{name: "memory/alpha/MEMORY.md", body: []byte("# notes")},
		{name: "system/alpha/var-lib/redis/dump.rdb", body: []byte("RDB")},
		{name: "system/alpha", body: []byte("skipped: missing kind")},
		{name: "future/unknown.bin", body: []byte("discarded")},
		// Parent-relative symlink in a LAX section must be tolerated.
		{name: "workspace/alpha/link", typeflag: tar.TypeSymlink, linkname: "../shared/target"},
	})

	p, err := ExtractPayload(ctx, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("ExtractPayload: %v", err)
	}
	defer func() { _ = p.Close() }()

	if p.DBDump == nil || p.DBDump.WorkspaceID != "ws1" {
		t.Fatalf("DBDump = %+v", p.DBDump)
	}
	if string(p.DevcontainerBySlug["alpha"]) != `{"image":"x"}` {
		t.Errorf("devcontainer = %q", p.DevcontainerBySlug["alpha"])
	}
	if string(p.MiseBySlug["alpha"]) != "[tools]" {
		t.Errorf("mise = %q", p.MiseBySlug["alpha"])
	}
	if !p.HasWorkspace("alpha") || p.HasWorkspace("ghost") {
		t.Errorf("HasWorkspace alpha=%v ghost=%v", p.HasWorkspace("alpha"), p.HasWorkspace("ghost"))
	}

	// Workspace section: names stripped to be container-root relative.
	r, ok, err := p.OpenWorkspace(ctx, "alpha")
	if err != nil || !ok {
		t.Fatalf("OpenWorkspace = (%v, %v)", ok, err)
	}
	ws := readPlainTar(t, r)
	_ = r.Close()
	if ws["main.go"] != "package main" {
		t.Errorf("workspace entries = %v", ws)
	}
	if _, leak := ws["alpha/main.go"]; leak {
		t.Errorf("slug prefix must be stripped inside the sink: %v", ws)
	}

	// Volume sections.
	for vol, wantName := range map[string]string{"home": ".bashrc", "tools": "run.sh"} {
		r, ok, err := p.OpenVolume(ctx, "alpha", vol)
		if err != nil || !ok {
			t.Fatalf("OpenVolume(%s) = (%v, %v)", vol, ok, err)
		}
		entries := readPlainTar(t, r)
		_ = r.Close()
		if _, found := entries[wantName]; !found {
			t.Errorf("volume %s entries = %v, want %s", vol, entries, wantName)
		}
	}
	// Missing volume / missing slug return (nil, false, nil).
	if r, ok, err := p.OpenVolume(ctx, "alpha", "ghost-vol"); r != nil || ok || err != nil {
		t.Errorf("missing vol = (%v, %v, %v)", r, ok, err)
	}
	if r, ok, err := p.OpenVolume(ctx, "ghost", "home"); r != nil || ok || err != nil {
		t.Errorf("missing slug = (%v, %v, %v)", r, ok, err)
	}

	// Memory section.
	r, ok, err = p.OpenMemory(ctx, "alpha")
	if err != nil || !ok {
		t.Fatalf("OpenMemory = (%v, %v)", ok, err)
	}
	mem := readPlainTar(t, r)
	_ = r.Close()
	if mem["MEMORY.md"] != "# notes" {
		t.Errorf("memory entries = %v", mem)
	}
	if _, ok, err := p.OpenMemory(ctx, "ghost"); ok || err != nil {
		t.Errorf("missing memory should be (false, nil)")
	}

	// System section.
	r, ok, err = p.OpenSystem(ctx, "alpha", "var-lib")
	if err != nil || !ok {
		t.Fatalf("OpenSystem = (%v, %v)", ok, err)
	}
	sys := readPlainTar(t, r)
	_ = r.Close()
	if sys["redis/dump.rdb"] != "RDB" {
		t.Errorf("system entries = %v", sys)
	}
	if _, ok, err := p.OpenSystem(ctx, "alpha", "ghost-kind"); ok || err != nil {
		t.Errorf("missing kind should be (false, nil)")
	}
	if _, ok, err := p.OpenSystem(ctx, "ghost", "var-lib"); ok || err != nil {
		t.Errorf("missing slug should be (false, nil)")
	}
	if _, ok, err := p.OpenWorkspace(ctx, "ghost"); ok || err != nil {
		t.Errorf("missing workspace should be (false, nil)")
	}

	// Close removes the temp dir and is idempotent.
	tempDir := p.tempDir
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Errorf("temp dir %s survived Close", tempDir)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	var nilPayload *ExtractedPayload
	if err := nilPayload.Close(); err != nil {
		t.Errorf("nil Close: %v", err)
	}
}

func TestExtractPayload_OpenErrorsAfterTempFileLoss(t *testing.T) {
	ctx := context.Background()
	payload := buildPayloadTarZst(t, []payloadEntry{
		{name: "workspace/alpha/f", body: []byte("x")},
		{name: "volumes/alpha/home/g", body: []byte("y")},
		{name: "memory/alpha/m", body: []byte("z")},
		{name: "system/alpha/var-lib/s", body: []byte("w")},
	})
	p, err := ExtractPayload(ctx, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	// Yank the backing files so every Open* hits its error branch with
	// found=true.
	for _, path := range []string{
		p.workspacePathBySlug["alpha"],
		p.volumePathsBySlug["alpha"]["home"],
		p.memoryPathBySlug["alpha"],
		p.systemPathsBySlug["alpha"]["var-lib"],
	} {
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove %s: %v", path, err)
		}
	}
	if _, ok, err := p.OpenWorkspace(ctx, "alpha"); !ok || err == nil || !strings.Contains(err.Error(), "open workspace section") {
		t.Errorf("OpenWorkspace = (%v, %v)", ok, err)
	}
	if _, ok, err := p.OpenVolume(ctx, "alpha", "home"); !ok || err == nil || !strings.Contains(err.Error(), "open volume section") {
		t.Errorf("OpenVolume = (%v, %v)", ok, err)
	}
	if _, ok, err := p.OpenMemory(ctx, "alpha"); !ok || err == nil || !strings.Contains(err.Error(), "open memory section") {
		t.Errorf("OpenMemory = (%v, %v)", ok, err)
	}
	if _, ok, err := p.OpenSystem(ctx, "alpha", "var-lib"); !ok || err == nil || !strings.Contains(err.Error(), "open system section") {
		t.Errorf("OpenSystem = (%v, %v)", ok, err)
	}
}

func TestExtractPayload_RejectsTamperedEntries(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		entries []payloadEntry
		wantSub string
	}{
		{
			"parent reference in name",
			[]payloadEntry{{name: "workspace/../../../etc/shadow", body: []byte("x")}},
			"contains parent reference",
		},
		// NUL-in-linkname is exercised separately below: archive/tar
		// refuses to ENCODE such an entry, so the malicious header has
		// to be forged byte-by-byte.
		{
			"absolute symlink in strict section",
			[]payloadEntry{{name: "memory/a/l", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"}},
			"link target is absolute",
		},
		{
			"parent-escaping symlink in strict section",
			[]payloadEntry{{name: "system/a/var-lib/l", typeflag: tar.TypeLink, linkname: "../../escape"}},
			"escapes via parent reference",
		},
		{
			"corrupt stream",
			nil, // replaced below with raw garbage
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var src io.Reader
			if tc.entries == nil {
				src = strings.NewReader("not a zstd stream at all, definitely not")
			} else {
				src = bytes.NewReader(buildPayloadTarZst(t, tc.entries))
			}
			p, err := ExtractPayload(ctx, src)
			if err == nil {
				_ = p.Close()
				t.Fatal("expected error")
			}
			if tc.wantSub != "" && !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %v, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestExtractPayload_BadDumpJSON(t *testing.T) {
	payload := buildPayloadTarZst(t, []payloadEntry{
		{name: "db/dump.json", body: []byte("{definitely broken")},
	})
	p, err := ExtractPayload(context.Background(), bytes.NewReader(payload))
	if err == nil {
		_ = p.Close()
		t.Fatal("expected unmarshal failure")
	}
	if !strings.Contains(err.Error(), "unmarshal dump") {
		t.Errorf("err = %v", err)
	}
}

func TestExtractPayload_SectionRootDirEntries(t *testing.T) {
	// The slug-root directory entry itself (e.g. "workspace/alpha/")
	// repacks as "." inside the sink; memory entries without a slug
	// separator are skipped entirely.
	payload := buildPayloadTarZst(t, []payloadEntry{
		{name: "workspace/alpha/", typeflag: tar.TypeDir},
		{name: "workspace/alpha/f.txt", body: []byte("x")},
		{name: "memory/sluglessentry", body: []byte("skipped")},
	})
	p, err := ExtractPayload(context.Background(), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	r, ok, err := p.OpenWorkspace(context.Background(), "alpha")
	if err != nil || !ok {
		t.Fatalf("OpenWorkspace = (%v, %v)", ok, err)
	}
	entries := readPlainTar(t, r)
	_ = r.Close()
	if _, hasDot := entries["."]; !hasDot {
		t.Errorf("slug-root dir must repack as '.': %v", entries)
	}
	if entries["f.txt"] != "x" {
		t.Errorf("entries = %v", entries)
	}
	if len(p.memoryPathBySlug) != 0 {
		t.Errorf("slugless memory entry must not create a sink: %v", p.memoryPathBySlug)
	}
}

// Note: the "link target contains NUL" branch in ExtractPayload is
// defence-in-depth that cannot be reached through Go's archive/tar
// reader — it parses the linkname field as a NUL-terminated cstring
// and rejects NUL bytes inside PAX linkpath records, so the value
// never arrives with an embedded NUL. Left uncovered on purpose.

func TestSymlinkSectionIsStrict(t *testing.T) {
	cases := map[string]bool{
		"workspace/a/x": false,
		"volumes/a/h/x": false,
		"memory/a/x":    true,
		"system/a/x":    true,
		"unknown/x":     true,
	}
	for name, want := range cases {
		if got := symlinkSectionIsStrict(name); got != want {
			t.Errorf("symlinkSectionIsStrict(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSplitFirst(t *testing.T) {
	if h, tl, ok := splitFirst("a/b/c"); h != "a" || tl != "b/c" || !ok {
		t.Errorf("splitFirst(a/b/c) = (%q, %q, %v)", h, tl, ok)
	}
	if h, tl, ok := splitFirst("solo"); h != "solo" || tl != "" || ok {
		t.Errorf("splitFirst(solo) = (%q, %q, %v)", h, tl, ok)
	}
	if h, tl, ok := splitFirst("/lead"); h != "" || tl != "lead" || !ok {
		t.Errorf("splitFirst(/lead) = (%q, %q, %v)", h, tl, ok)
	}
}

func TestStorageOrDefault(t *testing.T) {
	p := &ExtractedPayload{}
	if _, ok := p.storageOrDefault().(LocalStorageOps); !ok {
		t.Errorf("empty payload should fall back to package default, got %T", p.storageOrDefault())
	}
	stub := stubStorageOpsForDefaultDir{homePath: "/x"}
	p2 := &ExtractedPayload{storage: stub}
	if p2.storageOrDefault() != StorageOps(stub) {
		t.Errorf("captured storage must win")
	}
}

// restoreRecOps records which copy strategy each section travelled
// through, with optional per-method failures.
type restoreRecOps struct {
	copyToDst     []string
	copyVolumeDst []string
	copySystemDst []string

	copyToErr     error
	copyVolumeErr error
	copySystemErr error
}

func (r *restoreRecOps) Pause(context.Context, string) error   { return nil }
func (r *restoreRecOps) Unpause(context.Context, string) error { return nil }
func (r *restoreRecOps) CopyFrom(context.Context, string, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (r *restoreRecOps) CopyTo(_ context.Context, _ string, dst string, content io.Reader) error {
	_, _ = io.Copy(io.Discard, content)
	if r.copyToErr != nil {
		return r.copyToErr
	}
	r.copyToDst = append(r.copyToDst, dst)
	return nil
}
func (r *restoreRecOps) CopyToVolume(_ context.Context, _ string, dst string, content io.Reader) error {
	_, _ = io.Copy(io.Discard, content)
	if r.copyVolumeErr != nil {
		return r.copyVolumeErr
	}
	r.copyVolumeDst = append(r.copyVolumeDst, dst)
	return nil
}
func (r *restoreRecOps) CopyToSystem(_ context.Context, _ string, dst string, content io.Reader) error {
	_, _ = io.Copy(io.Discard, content)
	if r.copySystemErr != nil {
		return r.copySystemErr
	}
	r.copySystemDst = append(r.copySystemDst, dst)
	return nil
}
func (r *restoreRecOps) ContainerExists(context.Context, string) (bool, error) { return true, nil }
func (r *restoreRecOps) Exec(context.Context, string, []string) (int, []byte, error) {
	return 0, nil, nil
}

func fullSectionPayload(t *testing.T) *ExtractedPayload {
	t.Helper()
	payload := buildPayloadTarZst(t, []payloadEntry{
		{name: "workspace/alpha/f", body: []byte("w")},
		{name: "volumes/alpha/home/g", body: []byte("h")},
		{name: "volumes/alpha/tools/i", body: []byte("t")},
		{name: "memory/alpha/m", body: []byte("m")},
		{name: "system/alpha/var-lib/s", body: []byte("s")},
	})
	p, err := ExtractPayload(context.Background(), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestRestoreCrew_RoutesSectionsByStrategy(t *testing.T) {
	ctx := context.Background()

	t.Run("nil payload", func(t *testing.T) {
		err := RestoreCrew(ctx, &restoreRecOps{}, "c1", "alpha", nil)
		if err == nil || !strings.Contains(err.Error(), "nil payload") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("strategy routing", func(t *testing.T) {
		p := fullSectionPayload(t)
		ops := &restoreRecOps{}
		if err := RestoreCrew(ctx, ops, "c1", "alpha", p); err != nil {
			t.Fatalf("RestoreCrew: %v", err)
		}
		// Root-parent destinations go through the archive API.
		wantCopyTo := []string{ContainerWorkspacePath, ContainerMemoryPath}
		if strings.Join(ops.copyToDst, ",") != strings.Join(wantCopyTo, ",") {
			t.Errorf("CopyTo dsts = %v, want %v", ops.copyToDst, wantCopyTo)
		}
		// Named volumes go through exec-tar as the agent user.
		wantVol := []string{ContainerHomePath, ContainerToolsPath}
		if strings.Join(ops.copyVolumeDst, ",") != strings.Join(wantVol, ",") {
			t.Errorf("CopyToVolume dsts = %v, want %v", ops.copyVolumeDst, wantVol)
		}
		// /var/lib goes through the uid-0 path.
		if strings.Join(ops.copySystemDst, ",") != ContainerVarLibPath {
			t.Errorf("CopyToSystem dsts = %v, want [%s]", ops.copySystemDst, ContainerVarLibPath)
		}
	})

	t.Run("missing sections are silent skips", func(t *testing.T) {
		// Payload with no sections at all for this slug.
		p, err := ExtractPayload(ctx, bytes.NewReader(buildPayloadTarZst(t, []payloadEntry{
			{name: "workspace/other/f", body: []byte("x")},
		})))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = p.Close() }()
		ops := &restoreRecOps{}
		if err := RestoreCrew(ctx, ops, "c1", "alpha", p); err != nil {
			t.Fatalf("RestoreCrew: %v", err)
		}
		if len(ops.copyToDst)+len(ops.copyVolumeDst)+len(ops.copySystemDst) != 0 {
			t.Errorf("no copies expected, got %v %v %v", ops.copyToDst, ops.copyVolumeDst, ops.copySystemDst)
		}
	})

	t.Run("per-section failures aggregate", func(t *testing.T) {
		p := fullSectionPayload(t)
		ops := &restoreRecOps{
			copyToErr:     io.ErrUnexpectedEOF,
			copyVolumeErr: io.ErrClosedPipe,
			copySystemErr: io.ErrShortWrite,
		}
		err := RestoreCrew(ctx, ops, "c1", "alpha", p)
		if err == nil {
			t.Fatal("expected aggregated error")
		}
		for _, section := range []string{"workspace", "home", "tools", "memory", "var-lib"} {
			if !strings.Contains(err.Error(), section+":") {
				t.Errorf("error %v missing section %q", err, section)
			}
		}
		if !strings.Contains(err.Error(), "partial") {
			t.Errorf("error should flag partial restore: %v", err)
		}
	})

	t.Run("open failure feeds the same aggregation", func(t *testing.T) {
		p := fullSectionPayload(t)
		// Destroy only the home-volume temp file; other sections restore.
		if err := os.Remove(p.volumePathsBySlug["alpha"]["home"]); err != nil {
			t.Fatal(err)
		}
		ops := &restoreRecOps{}
		err := RestoreCrew(ctx, ops, "c1", "alpha", p)
		if err == nil || !strings.Contains(err.Error(), "home:") {
			t.Fatalf("err = %v", err)
		}
		// The remaining sections still landed.
		if len(ops.copyToDst) != 2 || len(ops.copySystemDst) != 1 {
			t.Errorf("other sections must still restore: %v %v", ops.copyToDst, ops.copySystemDst)
		}
	})
}

func TestSectionEntries_FromManifest(t *testing.T) {
	m := &Manifest{
		Contents: Contents{Crews: []CrewSummary{
			{Slug: "alpha", WorkspaceIncluded: true, MemoryIncluded: true, VolumesIncluded: []string{"home", "tools"}},
			{Slug: "beta"},
		}},
	}
	got := SectionEntries(m)
	want := []string{"workspace/alpha", "volumes/alpha/home", "volumes/alpha/tools", "memory/alpha"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("SectionEntries = %v, want %v", got, want)
	}
}
