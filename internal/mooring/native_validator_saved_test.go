//go:build linux

package mooring

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Only bytes the caller vouches for as its own engine's computed output are validated without
// recalculation; everything else (a browser's save, a foreign document) is recalculated as before.
func TestNativeCLIValidatorSkipsRecalculationOnlyForTrustedBytes(t *testing.T) {
	args := filepath.Join(t.TempDir(), "args")
	v := nativeCLIProbe(t, "gridcraft", fmt.Sprintf(`echo "$@" >> '%s'; cp "$2" "$3"; echo "wrote $3 ($(wc -c < "$3" | tr -d ' ') bytes)" >&2`, args))
	own, other := []byte(`{"sheets":[1]}`), []byte(`{"sheets":[2]}`)
	v.Trusted = func(raw []byte) bool { return string(raw) == string(own) }
	for _, raw := range [][]byte{own, other} {
		if err := v.Validate(t.Context(), raw); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	b, _ := os.ReadFile(args)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], " --saved-values") || strings.Contains(lines[1], "--saved-values") {
		t.Fatalf("engine runs: %q", lines)
	}
	// Word has no such option: never passed.
	w := nativeCLIProbe(t, "wordcraft", fmt.Sprintf(`echo "$@" >> '%s'; cp "$2" "$3"; echo "wrote $3" >&2`, args))
	w.Trusted = func([]byte) bool { return true }
	_ = os.Remove(args)
	_ = w.Validate(t.Context(), own)
	if b, _ := os.ReadFile(args); strings.Contains(string(b), "--saved-values") {
		t.Fatalf("wordcraft: %s", b)
	}
}
