package main

import (
	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
	"os"
	"path/filepath"
	"testing"
)

func TestPageProjectDirectoryRoundTripAndRefusals(t *testing.T) {
	source := pageprofile.Source()
	dir := filepath.Join(t.TempDir(), "app")
	if err := unpackPageProject(source, dir); err != nil {
		t.Fatal(err)
	}
	if err := unpackPageProject(source, dir); err == nil {
		t.Fatal("overwrote existing directory")
	}
	packed, err := packPageProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := source.Digest()
	got, _ := packed.Digest()
	if got != want {
		t.Fatal("roundtrip changed source")
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("PRIVATE=value"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := packPageProject(dir); err == nil {
		t.Fatal("packed environment file")
	}
	os.Remove(filepath.Join(dir, ".env"))
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "outside")); err != nil {
		t.Fatal(err)
	}
	if _, err := packPageProject(dir); err == nil {
		t.Fatal("packed symlink")
	}
}
