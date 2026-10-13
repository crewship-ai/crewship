//go:build linux

package docker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostCaptureQualificationTrustedPath(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "crewship-helper-trust-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	helper := filepath.Join(root, "helper")
	if err := os.WriteFile(helper, []byte("fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	if !trustedHostHelperPath(helper) {
		t.Fatal("private owned executable rejected")
	}
	for _, mode := range []os.FileMode{0644, 0775, 0777} {
		if err := os.Chmod(helper, mode); err != nil {
			t.Fatal(err)
		}
		if trustedHostHelperPath(helper) {
			t.Fatalf("unsafe helper mode %o admitted", mode)
		}
	}
	if err := os.Chmod(helper, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0775); err != nil {
		t.Fatal(err)
	}
	if trustedHostHelperPath(helper) {
		t.Fatal("group writable ancestor admitted")
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(helper, alias); err != nil {
		t.Fatal(err)
	}
	if trustedHostHelperPath(alias) {
		t.Fatal("uncanonical symlink admitted")
	}
	for _, path := range []string{"relative", helper + "/../helper", root, filepath.Join(root, "missing")} {
		if trustedHostHelperPath(path) {
			t.Fatalf("invalid helper path %q admitted", path)
		}
	}
}
