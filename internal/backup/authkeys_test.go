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

// TestCollectAuthKeys_EdgeCases locks down two behaviours the restore
// flow depends on: (1) whitespace-only env counts as unset, (2) a
// real secret that HAPPENS to have leading/trailing whitespace is
// preserved byte-for-byte. Mutating (2) would silently invalidate
// every JWE session on the target after a cross-instance restore.
func TestCollectAuthKeys_EdgeCases(t *testing.T) {
	t.Run("whitespace-only env is rejected", func(t *testing.T) {
		t.Setenv(NextAuthSecretEnv, "   \t\n  ")
		if _, err := CollectAuthKeys(); err == nil {
			t.Error("whitespace-only env must be rejected")
		}
	})
	t.Run("preserves leading/trailing whitespace in secret", func(t *testing.T) {
		raw := "  s3cr3t-with-padding  "
		t.Setenv(NextAuthSecretEnv, raw)
		keys, err := CollectAuthKeys()
		if err != nil {
			t.Fatalf("present env: %v", err)
		}
		if keys.NextAuthSecret != raw {
			t.Errorf("secret bytes altered: got %q want %q", keys.NextAuthSecret, raw)
		}
	})
	t.Run("preserves trailing newline", func(t *testing.T) {
		raw := "real-secret\n"
		t.Setenv(NextAuthSecretEnv, raw)
		keys, err := CollectAuthKeys()
		if err != nil {
			t.Fatalf("present env: %v", err)
		}
		if keys.NextAuthSecret != raw {
			t.Errorf("trailing newline stripped: got %q want %q", keys.NextAuthSecret, raw)
		}
	})
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
