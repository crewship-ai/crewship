package devcontainer

import (
	"fmt"
	"strings"
	"testing"
)

// The build-log tail is bounded on BOTH lines and bytes (#829) so a verbose
// build — or a few pathologically long lines — never balloons the durable
// failure payload.
func TestBoundedLog_LineCap(t *testing.T) {
	b := newBoundedLog(3, 1<<20)
	for i := 0; i < 10; i++ {
		b.add(fmt.Sprintf("line-%d", i))
	}
	if got, want := b.tail(), "line-7\nline-8\nline-9"; got != want {
		t.Errorf("line cap: got %q, want %q", got, want)
	}
}

func TestBoundedLog_ByteCapCutsAtLineBoundary(t *testing.T) {
	// joined = 32 bytes ("aaaaaaaaaa\nbbbbbbbbbb\ncccccccccc"); cap 20.
	b := newBoundedLog(100, 20)
	b.add("aaaaaaaaaa")
	b.add("bbbbbbbbbb")
	b.add("cccccccccc")

	got := b.tail()
	if len(got) > 20 {
		t.Errorf("byte cap exceeded: %d bytes %q", len(got), got)
	}
	if strings.Contains(got, "aaaa") {
		t.Errorf("leading over-cap line must be dropped: %q", got)
	}
	if !strings.HasSuffix(got, "cccccccccc") {
		t.Errorf("must retain the trailing (failing-step) output: %q", got)
	}
	// No partial leading line: every retained line is whole.
	for _, line := range strings.Split(got, "\n") {
		if line != "" && len(line) != 10 {
			t.Errorf("partial line retained: %q", line)
		}
	}
}
