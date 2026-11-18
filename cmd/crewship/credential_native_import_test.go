package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// An imported synthetic private key must remain usable by standard OpenSSH.
func TestCredentialImportOpenSSH(t *testing.T) {
	key := filepath.Join(t.TempDir(), "synthetic")
	if err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", key).Run(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("ssh-keygen", "-y", "-f", key).Run(); err != nil {
		t.Fatal("original key rejected")
	}
	after, err := readCredentialValue(bytes.NewReader(before), "synthetic stdin/file")
	if err != nil {
		t.Fatal(err)
	}
	imported := filepath.Join(t.TempDir(), "imported")
	if err := os.WriteFile(imported, []byte(after), 0600); err != nil {
		t.Fatal(err)
	}
	parseErr := exec.Command("ssh-keygen", "-y", "-f", imported).Run()
	t.Logf("synthetic only: before=%d bytes after=%d bytes original_parse=OK imported_parse_error=%v", len(before), len(after), parseErr)
	if !bytes.Equal(before, []byte(after)) {
		t.Errorf("credential import changed key bytes")
	}
	if parseErr != nil {
		t.Errorf("standard OpenSSH rejected imported synthetic key: %v", parseErr)
	}
}

func TestCredentialImportRejectsUnsupportedBinary(t *testing.T) {
	for _, value := range [][]byte{{0xff, 'x'}, {'x', 0, 'y'}} {
		if _, err := readCredentialValue(bytes.NewReader(value), "fixture"); err == nil {
			t.Fatal("binary import silently accepted")
		}
	}
}
