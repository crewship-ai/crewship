package orchestrator

import (
	"strings"
	"testing"
)

// firstOversizedArg backs the shared exec-path E2BIG guard: adapters that pass
// the prompt as an argv element (everything except the stdin-capable Claude
// adapter) would otherwise crash with a cryptic exit-255/$0.00 when a single
// argument crosses Linux's 128 KiB MAX_ARG_STRLEN. The guard turns that into a
// legible error for every arg-path adapter.
func TestFirstOversizedArg(t *testing.T) {
	t.Run("normal command is fine", func(t *testing.T) {
		cmd := []string{"gemini", "-p", "summarize this", "--output-format", "stream-json"}
		if over, n := firstOversizedArg(cmd); over {
			t.Errorf("normal command flagged oversized (len=%d)", n)
		}
	})

	t.Run("oversized positional prompt is caught", func(t *testing.T) {
		big := strings.Repeat("A", 200*1024) // 200 KiB, well over the 128 KiB limit
		cmd := []string{"gemini", "-p", big}
		over, n := firstOversizedArg(cmd)
		if !over {
			t.Fatal("oversized argument NOT flagged — execve would E2BIG")
		}
		if n != len(big) {
			t.Errorf("reported length = %d, want %d", n, len(big))
		}
	})

	t.Run("just under the guarded ceiling is allowed", func(t *testing.T) {
		// Below (limit - margin) must pass so normal large-ish prompts on
		// arg-path adapters aren't needlessly blocked.
		arg := strings.Repeat("B", maxArgStrLen-argSafetyMargin-1)
		if over, _ := firstOversizedArg([]string{"gemini", "-p", arg}); over {
			t.Error("argument just under the ceiling was flagged")
		}
	})
}
