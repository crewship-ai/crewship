package backup

import (
	"strings"
	"testing"
)

func TestCollectAuthKeys_RequiresEnv(t *testing.T) {
	t.Setenv(NextAuthSecretEnv, "")
	_, err := CollectAuthKeys()
	if err == nil {
		t.Fatal("missing env must error")
	}
	t.Setenv(NextAuthSecretEnv, "s3cr3t")
	keys, err := CollectAuthKeys()
	if err != nil {
		t.Fatalf("present env: %v", err)
	}
	if keys.NextAuthSecret != "s3cr3t" {
		t.Errorf("secret not captured: %q", keys.NextAuthSecret)
	}
}

func TestRotateAuthKeys_GeneratesHexSecret(t *testing.T) {
	var written string
	new, err := RotateAuthKeys(func(s string) error {
		written = s
		return nil
	})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if new != written {
		t.Errorf("writer received different secret than returned")
	}
	// 64 bytes hex-encoded → 128 chars.
	if len(new) != 128 {
		t.Errorf("secret length: got %d want 128", len(new))
	}
	// Every char must be lowercase hex.
	if strings.Trim(new, "0123456789abcdef") != "" {
		t.Errorf("secret contains non-hex: %q", new)
	}
}

func TestRotateAuthKeys_RequiresWriter(t *testing.T) {
	if _, err := RotateAuthKeys(nil); err == nil {
		t.Error("nil writer must error")
	}
}
