package hooks

import (
	"runtime"
	"strings"
	"testing"
)

func TestShellOutputPreservesPresentationWithBoundedRetention(t *testing.T) {
	for _, size := range []int{0, 255, 256, 257, 4095, 4096, 4097, 16384} {
		input := strings.Repeat("x", size)
		for _, chunk := range []int{1, 23, 4096, 8192} {
			var out shellOutput
			for pos := 0; pos < len(input); pos += chunk {
				end := pos + chunk
				if end > len(input) {
					end = len(input)
				}
				n, err := out.Write([]byte(input[pos:end]))
				if err != nil || n != end-pos {
					t.Fatalf("drain: %d %v", n, err)
				}
				if out.Len() > maxShellCapturedBytes {
					t.Fatal("output retained beyond cap")
				}
			}
			for _, limit := range []int{256, 4096} {
				if got, want := truncate(out.String(), limit), truncate(input, limit); got != want {
					t.Fatalf("size=%d chunk=%d limit=%d: presentation changed", size, chunk, limit)
				}
			}
		}
	}
}

func TestShellHandlerDrainsBothStreamsBeyondCaptureLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	result, err := shellHandler(t.Context(), Hook{HandlerConfig: map[string]any{"command": `i=0; while [ "$i" -lt 10000 ]; do printf x; printf y >&2; i=$((i+1)); done`}}, EventContext{})
	if err != nil || result.Outcome != OutcomePass {
		t.Fatalf("large-output script did not complete: %+v %v", result, err)
	}
	payload := result.Payload.(map[string]any)
	if payload["stdout"] != strings.Repeat("x", 4096)+"...[truncated]" || payload["stderr"] != strings.Repeat("y", 4096)+"...[truncated]" {
		t.Fatal("stream capture/presentation changed")
	}
}
