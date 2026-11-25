package backup_test

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestForceGenerationWrongIdentity(t *testing.T) {
	f := newInstanceFixture(t)
	bundle := f.create(t, true)
	target := filepath.Join(t.TempDir(), "data")
	opts := backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: target}
	if _, err := backup.RecoverInstance(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	paths := []string{"crewship.db", "secrets.env"}
	originals := map[string]os.FileInfo{}
	for _, p := range paths {
		info, err := os.Stat(filepath.Join(target, p))
		if err != nil {
			t.Fatal(err)
		}
		originals[p] = info
	}
	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	opts.Force = true
	opts.Identities = []age.Identity{other}
	if _, err = backup.RecoverInstance(t.Context(), opts); err == nil {
		t.Fatal("accepted the wrong recovery identity")
	}
	for _, p := range paths {
		info, err := os.Stat(filepath.Join(target, p))
		if err != nil || !os.SameFile(originals[p], info) {
			t.Errorf("original %s changed after rejected recovery: %v", p, err)
		}
	}
}

type forceEntry struct {
	info     os.FileInfo
	uid, gid int
	hash     [sha256.Size]byte
}

func forceGenerationSnapshot(t *testing.T, root string) map[string]forceEntry {
	t.Helper()
	tree := make(map[string]forceEntry)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		entry := forceEntry{info: info, uid: header.Uid, gid: header.Gid}
		if info.Mode().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry.hash = sha256.Sum256(body)
		}
		tree[rel] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func assertOriginalGeneration(t *testing.T, dir string, before map[string]forceEntry) {
	t.Helper()
	after := forceGenerationSnapshot(t, dir)
	if len(after) != len(before) {
		t.Errorf("original entry count = %d, want %d", len(after), len(before))
	}
	for rel, old := range before {
		got, ok := after[rel]
		if !ok || !os.SameFile(old.info, got.info) || old.hash != got.hash ||
			old.info.Mode() != got.info.Mode() || old.uid != got.uid || old.gid != got.gid ||
			!old.info.ModTime().Equal(got.info.ModTime()) {
			t.Errorf("original entry changed: %s", rel)
		}
	}
}

// Change a late-checked manifest value but keep the encrypted payload and its
// checksum valid, so a refusal exercises rollback after extraction.
func wrongSnapshotSizeBundle(t *testing.T, path string) string {
	t.Helper()
	in, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	m, r, err := backup.ReadBundle(in)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	m.Contents.Instance.DatabaseBytes++
	out, err := os.Create(filepath.Join(t.TempDir(), "wrong-size.tar.zst"))
	if err != nil {
		t.Fatal(err)
	}
	if err := backup.WriteBundleStream(out, m, bytes.NewReader(sealed), int64(len(sealed))); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Name()
}

func TestForceGenerationCorruptionAndSwitch(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("force publication requires Linux")
	}
	f := newInstanceFixture(t)
	bundle := f.create(t, true)
	parent := t.TempDir()
	target := filepath.Join(parent, "data")
	opts := backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: target}
	if _, err := backup.RecoverInstance(t.Context(), opts); err != nil {
		t.Fatal(err)
	}

	// Make the old store distinguishable from its replacement.
	skill := filepath.Join("skills", "custom", "SKILL.md")
	if err := os.WriteFile(filepath.Join(target, skill), []byte("target version"), 0600); err != nil {
		t.Fatal(err)
	}
	operatorDir := filepath.Join(target, "operator")
	if err := os.Mkdir(operatorDir, 0750); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("operator", "site.conf")
	local := filepath.Join(target, rel)
	if err := os.WriteFile(local, []byte("site config"), 0600); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(local, 1001, 1002); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(local, 0640); err != nil {
		t.Fatal(err)
	}
	before := forceGenerationSnapshot(t, target)

	opts.Force = true
	opts.BundlePath = wrongSnapshotSizeBundle(t, bundle.Path)
	report, err := backup.RecoverInstance(t.Context(), opts)
	if err == nil || report == nil || report.Result != backup.RestoreResultFailed || report.PreviousDataDir != "" {
		t.Fatalf("corrupt snapshot: report %+v, error %v", report, err)
	}
	assertOriginalGeneration(t, target, before)
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 {
		t.Fatalf("after refusal, target parent has %d entries: %v", len(entries), err)
	}

	opts.BundlePath = bundle.Path
	report, err = backup.RecoverInstance(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != backup.RestoreResultOK || report.DataDir != target || report.PreviousDataDir == "" {
		t.Fatalf("unexpected recovery report %+v", report)
	}
	assertOriginalGeneration(t, report.PreviousDataDir, before)
	active := forceGenerationSnapshot(t, target)
	if os.SameFile(before["."].info, active["."].info) {
		t.Error("target directory was not switched")
	}
	if active[skill].hash != sha256.Sum256([]byte("a skill")) {
		t.Error("bundled skill was not restored")
	}
	for _, name := range []string{".", "operator", rel} {
		old := before[name]
		got, ok := active[name]
		if !ok || got.hash != old.hash || got.info.Mode() != old.info.Mode() || got.uid != old.uid || got.gid != old.gid {
			t.Errorf("recovered copy lost contents, permissions or ownership of %s", name)
		}
	}
	if os.SameFile(before[rel].info, active[rel].info) {
		t.Error("unrelated file is shared between generations")
	}
}
