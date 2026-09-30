//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

func TestProjectInputVerifierExactSnapshotAndNoWritableAliases(t *testing.T) {
	base := t.TempDir()
	content := []byte("H1 project source canary")
	sum := sha256.Sum256(content)
	m, err := restrictedruntime.NewNativeInputManifest([]restrictedruntime.NativeInputFile{{VersionID: "version1", Name: "input.txt", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "version1")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	file := filepath.Join(dir, "input.txt")
	if err := os.WriteFile(file, content, 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	check := func() error { return verifyProjectInputsAt(base, *m, uint32(os.Getuid())) }
	if err := check(); err != nil {
		t.Fatal("exact source rejected", err)
	}
	if err := os.Chmod(file, 0600); err != nil {
		t.Fatal(err)
	}
	if check() == nil {
		t.Fatal("writable source accepted")
	}
	if err := os.Chmod(file, 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Link(file, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	if check() == nil {
		t.Fatal("hardlink alias accepted")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	if check() == nil {
		t.Fatal("symlink/extra file accepted")
	}
}
