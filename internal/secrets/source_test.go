package secrets

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Dummy AES-256 test key (64 hex chars), assembled via Repeat so secret
// scanners don't flag the literal.
var sourceTestKey = strings.Repeat("0123456789abcdef", 4)

func clearManagedEnv(t *testing.T) {
	t.Helper()
	for _, m := range managed {
		t.Setenv(m.EnvVar, "")
		os.Unsetenv(m.EnvVar)
	}
}

// TestEncryptionKeySource_External: an operator-supplied ENCRYPTION_KEY (env)
// must be reported as "external" and must NOT trigger the colocated-key
// warning.
func TestEncryptionKeySource_External(t *testing.T) {
	clearManagedEnv(t)
	t.Setenv("ENCRYPTION_KEY", sourceTestKey)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	if err := LoadOrGenerate(context.Background(), t.TempDir(), logger); err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}

	if got := EncryptionKeySource(); got != SourceExternal {
		t.Fatalf("EncryptionKeySource() = %q, want %q", got, SourceExternal)
	}
	if strings.Contains(buf.String(), "auto-generated") {
		t.Fatalf("unexpected colocated-key warning for external key:\n%s", buf.String())
	}
}

// TestEncryptionKeySource_Generated: a first-run auto-generated key is
// colocated with the database — source must be "generated" and a WARN line
// must land in the startup log.
func TestEncryptionKeySource_Generated(t *testing.T) {
	clearManagedEnv(t)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	if err := LoadOrGenerate(context.Background(), t.TempDir(), logger); err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}

	if got := EncryptionKeySource(); got != SourceGenerated {
		t.Fatalf("EncryptionKeySource() = %q, want %q", got, SourceGenerated)
	}
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "auto-generated") {
		t.Fatalf("expected WARN about auto-generated colocated key, got:\n%s", out)
	}
}

// TestEncryptionKeySource_Persisted: a key loaded from <dataDir>/secrets.env
// on a later boot is still colocated with the database — "generated", and the
// warning fires on every boot, not just the first.
func TestEncryptionKeySource_Persisted(t *testing.T) {
	clearManagedEnv(t)
	dir := t.TempDir()

	// First boot generates + persists.
	if err := LoadOrGenerate(context.Background(), dir, slog.New(slog.NewTextHandler(os.Stderr, nil))); err != nil {
		t.Fatalf("LoadOrGenerate (first): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets.env")); err != nil {
		t.Fatalf("secrets.env not persisted: %v", err)
	}

	// Simulate a fresh process: env cleared, persisted file present.
	clearManagedEnv(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	if err := LoadOrGenerate(context.Background(), dir, logger); err != nil {
		t.Fatalf("LoadOrGenerate (second): %v", err)
	}

	if got := EncryptionKeySource(); got != SourceGenerated {
		t.Fatalf("EncryptionKeySource() = %q, want %q", got, SourceGenerated)
	}
	if !strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("expected colocated-key WARN on subsequent boots, got:\n%s", buf.String())
	}
}
