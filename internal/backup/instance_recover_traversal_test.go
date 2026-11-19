package backup

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExtractInstancePayload_RefusesTraversalEntries pins the guards that
// code-scanning alert #1073 (go/zipslip, tar.go Next) does not credit: an
// instance payload entry name reaches the filesystem only through
// safepath.JoinRel (instance/files/<store>/<rel>) or safepath.JoinUnder
// (instance/crews/<id>.tar.zst, instance/environments/<ws>.tar.zst). A
// tampered bundle carrying "../" entries must be refused before any byte is
// written, and nothing may land outside the recovery data directory.
// Replacing either guard with filepath.Join makes this test fail.
func TestExtractInstancePayload_RefusesTraversalEntries(t *testing.T) {
	for _, name := range []string{
		instanceFilesPrefix + StoreOutput + "/../../../escaped.txt",
		instanceFilesPrefix + StoreOutput + "/a/../../../../escaped.txt",
		instanceFilesPrefix + StoreOutput + "/..",
		instanceCrewsPrefix + "../../escaped" + instanceCrewsSuffix,
		instanceCrewsPrefix + "x/../../escaped" + instanceCrewsSuffix,
		instanceEnvPrefix + "../../escaped" + instanceCrewsSuffix,
		instanceEnvPrefix + "../escaped" + instanceCrewsSuffix,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dataDir := filepath.Join(root, "data", "recovered")
			if err := os.MkdirAll(dataDir, 0o700); err != nil {
				t.Fatal(err)
			}
			bundle := filepath.Join(root, "bundle.tar.zst")
			m := writeInstanceBundleWithEntry(t, bundle, name)

			_, err := extractInstancePayload(t.Context(), RecoverOptions{BundlePath: bundle, DataDir: dataDir}, m, dataDir)
			if err == nil || !strings.Contains(err.Error(), "refuse unsafe payload path") {
				t.Fatalf("extract %q: want unsafe payload refusal, got %v", name, err)
			}
			_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() && strings.HasPrefix(d.Name(), "escaped") {
					t.Errorf("traversal entry %q was written to %s", name, p)
				}
				return nil
			})
		})
	}
}

// writeInstanceBundleWithEntry writes an unencrypted instance bundle whose
// payload holds one regular-file entry called name.
func writeInstanceBundleWithEntry(t *testing.T, path, name string) *Manifest {
	t.Helper()
	var payload bytes.Buffer
	tw, err := NewTarZstWriter(&payload)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("planted")
	if err := tw.tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg, ModTime: nonZeroTime()}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	m := &Manifest{
		Scope:             ScopeInstance,
		CompatibleTargets: []Target{TargetAnyInstance},
		CreatedAt:         nonZeroTime(),
		CreatedBy:         Actor{UserID: "u_test"},
		Encryption:        Encryption{Enabled: false},
		Checksums:         Checksums{PayloadSHA256: "sha256:deadbeef"},
		Contents: Contents{Instance: &InstanceContents{Stores: map[string]StoreSummary{
			StoreOutput: {Configured: true},
		}}},
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := WriteBundleStream(f, m, bytes.NewReader(payload.Bytes()), int64(payload.Len())); err != nil {
		t.Fatal(err)
	}
	return m
}
