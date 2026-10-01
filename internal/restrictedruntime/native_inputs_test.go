//go:build linux

package restrictedruntime

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"
)

func TestNativeInputManifestRejectsSourceMutationAndScopeAlias(t *testing.T) {
	sum := sha256.Sum256([]byte("H1 private project canary"))
	files := []NativeInputFile{{VersionID: "version-one", Name: "src/input.txt", Size: 25, SHA256: hex.EncodeToString(sum[:])}}
	m, err := NewNativeInputManifest(files)
	if err != nil {
		t.Fatal(err)
	}
	p := Plan{Workspace: "ws", Principal: "h1", Agent: "agent", Scope: "scope-h1", Attempt: "attempt-h1", Revision: "r1", NativeSandbox: "protected", NativeInputs: m}
	p.Mounts = []Mount{{Resource: NativeInputResource(p), Target: NativeInputTarget, ReadOnly: true}}
	if !validNativeInputPlan(p) {
		t.Fatal("exact read-only input rejected")
	}
	other := p
	other.Principal, other.Scope, other.Attempt = "h2", "scope-h2", "attempt-h2"
	if validNativeInputPlan(other) || NativeInputResource(other) == NativeInputResource(p) {
		t.Fatal("another human reused snapshot catalog identity")
	}
	p.Mounts[0].ReadOnly = false
	if validNativeInputPlan(p) {
		t.Fatal("writable source accepted")
	}
	p.Mounts[0].ReadOnly = true
	m.Files[0].SHA256 = hex.EncodeToString(make([]byte, 32))
	if validNativeInputPlan(p) {
		t.Fatal("changed content provenance kept manifest authority")
	}
	for _, name := range []string{"../secret", "/host/file", "a/../../b", "a\\b", ".", "a\nheader"} {
		files[0].Name = name
		if _, err := NewNativeInputManifest(files); err == nil {
			t.Fatalf("unsafe source path accepted: %q", name)
		}
	}
}

func TestNativeInputArchivePreservesBinaryAndDeniesUnboundBytes(t *testing.T) {
	canary := []byte{0, 255, 1, 'H', '1'}
	h := sha256.Sum256(canary)
	m, err := NewNativeInputManifest([]NativeInputFile{{VersionID: "v1", Name: "nested/input.bin", Size: int64(len(canary)), SHA256: hex.EncodeToString(h[:])}})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := nativeInputArchive(m, []NativeInputData{{VersionID: "v1", Content: canary}})
	if err != nil {
		t.Fatal(err)
	}
	r := tar.NewReader(bytes.NewReader(archive))
	files := 0
	for {
		hdr, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Uid != 1001 || hdr.Gid != 1001 || hdr.Linkname != "" || (hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir) || hdr.Mode&0222 != 0 {
			t.Fatal("snapshot archive widened owner, writable mode or link authority")
		}
		if hdr.Typeflag == tar.TypeReg {
			b, err := io.ReadAll(r)
			if err != nil || hdr.Name != "v1/nested/input.bin" || !bytes.Equal(b, canary) {
				t.Fatal("binary snapshot changed")
			}
			files++
		}
	}
	if files != 1 {
		t.Fatal("snapshot imported extra files")
	}
	if _, err := nativeInputArchive(m, []NativeInputData{{VersionID: "v1", Content: []byte("H2 canary")}}); err == nil {
		t.Fatal("uncaptured bytes materialized")
	}
	if _, err := nativeInputArchive(m, []NativeInputData{{VersionID: "foreign-v", Content: canary}}); err == nil {
		t.Fatal("foreign version materialized")
	}
}
