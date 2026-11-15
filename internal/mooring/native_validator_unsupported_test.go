//go:build !linux

package mooring

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/craftengine"
)

// Outside Linux an operator's native engine override is refused before it can
// run, and a validator without an engine refuses every candidate.
func TestNativeCLIValidatorUnsupportedPlatformRefusesBeforeExecution(t *testing.T) {
	for _, app := range []string{"wordcraft", "gridcraft"} {
		t.Run(app, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "called")
			path := filepath.Join(t.TempDir(), "engine")
			raw := []byte(fmt.Sprintf("#!/bin/sh\ntouch %q\n", marker))
			if err := os.WriteFile(path, raw, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := craftengine.NewNative(app, path, fmt.Sprintf("%x", sha256.Sum256(raw))); !errors.Is(err, craftengine.ErrUnavailable) {
				t.Fatalf("native engine on an unsupported platform: %v", err)
			}
			v := NativeCLIValidator{Application: app}
			if err := v.Validate(t.Context(), []byte(`{"native":"valid candidate"}`)); !errors.Is(err, ErrNativeValidation) {
				t.Fatalf("validator without an engine did not fail closed: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("unsupported platform reached engine")
			}
		})
	}
}
