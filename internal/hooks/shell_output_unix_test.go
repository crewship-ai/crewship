//go:build !windows

package hooks

import (
	"strings"
	"testing"
)

func TestShellHandlerDrainsBothStreamsBeyondCaptureLimit(t *testing.T) {
	result, err := shellHandler(t.Context(), Hook{HandlerConfig: map[string]any{"command": `i=0; while [ "$i" -lt 10000 ]; do printf x; printf y >&2; i=$((i+1)); done`}}, EventContext{})
	if err != nil || result.Outcome != OutcomePass {
		t.Fatalf("large-output script did not complete: %+v %v", result, err)
	}
	payload := result.Payload.(map[string]any)
	if payload["stdout"] != strings.Repeat("x", 4096)+"...[truncated]" || payload["stderr"] != strings.Repeat("y", 4096)+"...[truncated]" {
		t.Fatal("stream capture/presentation changed")
	}
}
