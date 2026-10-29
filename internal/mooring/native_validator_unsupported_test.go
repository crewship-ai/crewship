//go:build !linux

package mooring

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeCLIValidatorUnsupportedPlatformRefusesBeforeExecution(t *testing.T) {
	for _, app := range []string{"wordcraft", "gridcraft"} {
		t.Run(app, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "called")
			v := nativeCLIProbe(t, app, fmt.Sprintf("touch %q", marker))
			if err := v.Validate(t.Context(), []byte(`{"native":"valid candidate"}`)); !errors.Is(err, ErrNativeValidation) {
				t.Fatalf("unsupported platform did not fail closed: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("unsupported platform reached engine")
			}
		})
	}
}
