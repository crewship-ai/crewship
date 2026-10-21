package journal

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAuditBootPublishesOneStablePrivateKeyring(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "synthetic-boot-root")
	dir := t.TempDir()
	const boots = 8
	var group sync.WaitGroup
	ids := make(chan string, boots)
	errors := make(chan error, boots)
	for i := 0; i < boots; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			keys, _, err := EnsureSigningKeys(dir, "")
			if err != nil {
				errors <- err
				return
			}
			ids <- keys.ActiveID()
		}()
	}
	group.Wait()
	close(errors)
	close(ids)
	for err := range errors {
		t.Error(err)
	}
	var active string
	for id := range ids {
		if active != "" && active != id {
			t.Error("concurrent boots selected different active keys")
		}
		active = id
	}
	if active == "" || active == LegacySigningKeyID || active == "bootstrap-v2" {
		t.Fatal("normal boot did not select persisted independent current key")
	}
	path := filepath.Join(dir, SigningKeysFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private file mode: %v", err)
	}
	keys, _, err := EnsureSigningKeys(dir, "")
	if err != nil || keys.ActiveID() != active {
		t.Fatalf("restart identity changed: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("restart replaced existing keyring")
	}
	legacy, err := keys.verification(ActiveSigningIdentity(LegacySigningKeys(ChainKeyFromEnv())))
	if err != nil || !bytes.Equal(legacy, ChainKeyFromEnv()) {
		t.Fatal("legacy verify root not preserved")
	}
}

func TestAuditBootDoesNotRepairInvalidOrExplicitKeyring(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		dir := t.TempDir()
		path := filepath.Join(dir, SigningKeysFile)
		bad := []byte("invalid synthetic fixture")
		if err := os.WriteFile(path, bad, 0600); err != nil {
			t.Fatal(err)
		}
		configured := ""
		if explicit {
			configured = path
		}
		if _, _, err := EnsureSigningKeys(dir, configured); err == nil {
			t.Error("invalid existing keyring accepted")
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(bad, after) {
			t.Fatal("invalid existing keyring overwritten")
		}
	}
}
