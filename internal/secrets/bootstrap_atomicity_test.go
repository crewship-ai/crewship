package secrets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapValidationFailureDoesNotPublishPartialSecrets(t *testing.T) {
	for _, persisted := range []bool{false, true} {
		name := "environment"
		if persisted {
			name = "persisted"
		}
		t.Run(name, func(t *testing.T) {
			scrubEnv(t)
			dir := t.TempDir()
			if persisted {
				value, err := generateHex(32)
				if err != nil {
					t.Fatal(err)
				}
				if err := writeFile(t.Context(), SecretsFilePath(dir), map[string]string{"ENCRYPTION_KEY": value, "NEXTAUTH_SECRET": "invalid"}); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv("NEXTAUTH_SECRET", "invalid")
			}
			before := make(map[string]string)
			for _, m := range managed {
				before[m.EnvVar] = os.Getenv(m.EnvVar)
			}
			if err := LoadOrGenerate(t.Context(), dir, silentLogger()); err == nil {
				t.Fatal("invalid later secret accepted")
			}
			for _, m := range managed {
				if os.Getenv(m.EnvVar) != before[m.EnvVar] {
					t.Errorf("validation failure published %s before the complete secret set was accepted", m.EnvVar)
				}
			}
			if !persisted {
				if _, err := os.Stat(filepath.Join(dir, secretsFileName)); !os.IsNotExist(err) {
					t.Fatalf("invalid environment created secrets file: %v", err)
				}
			}
		})
	}
}

func TestPersistedNULSecretIsRefusedBeforeExport(t *testing.T) {
	scrubEnv(t)
	dir := t.TempDir()
	key, err := generateHex(32)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"ENCRYPTION_KEY": key, "NEXTAUTH_SECRET": strings.Repeat("x", 32) + "\x00"}
	if err := writeFile(t.Context(), SecretsFilePath(dir), values); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(SecretsFilePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := LoadOrGenerate(t.Context(), dir, silentLogger()); err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("unexportable persisted secret accepted: %v", err)
	}
	for _, m := range managed {
		if os.Getenv(m.EnvVar) != "" {
			t.Errorf("unexportable secret set partially published %s", m.EnvVar)
		}
	}
	after, err := os.ReadFile(SecretsFilePath(dir))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("invalid persisted set was rewritten")
	}
}

func TestCancelledBootstrapDoesNotPublishGeneratedValues(t *testing.T) {
	scrubEnv(t)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	logger := slog.New(cancelOnGeneration{Handler: slog.NewTextHandler(io.Discard, nil), cancel: cancel})
	if err := LoadOrGenerate(ctx, dir, logger); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled bootstrap: %v", err)
	}
	for _, m := range managed {
		if os.Getenv(m.EnvVar) != "" {
			t.Errorf("cancelled bootstrap published %s", m.EnvVar)
		}
	}
	if _, err := os.Stat(SecretsFilePath(dir)); !os.IsNotExist(err) {
		t.Fatalf("cancelled bootstrap persisted partial set: %v", err)
	}
}

type cancelOnGeneration struct {
	slog.Handler
	cancel context.CancelFunc
}

func (h cancelOnGeneration) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "first-run secret generated" {
		h.cancel()
	}
	return h.Handler.Handle(ctx, record)
}

func TestUnknownSecretSourceDoesNotClaimExternalProtection(t *testing.T) {
	if source := Source("UNMANAGED_TEST_KEY"); source != SourceUnknown {
		t.Fatalf("unmanaged key source = %q", source)
	}
}
