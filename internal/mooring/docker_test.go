package mooring

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDockerRunnerRejectsMutableOrInjectedImageBeforeExecution(t *testing.T) {
	for _, image := range []string{"", "engine:latest", "engine@sha256:abc", "--privileged", "engine@sha256:" + strings.Repeat("a", 64) + " --network host"} {
		if _, err := NewDockerRunner(image); !errors.Is(err, ErrInvalid) {
			t.Fatalf("image %q: %v", image, err)
		}
		if _, err := (DockerRunner{Image: image}).Run(context.Background(), nil, Command{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("direct construction %q: %v", image, err)
		}
	}
}

func TestWorkerOutputBoundCannotBeBypassedByReaderFrom(t *testing.T) {
	output := &boundedOutput{remaining: 4}
	// LimitReader has no WriteTo: io.Copy tries the destination's ReaderFrom.
	// Embedding bytes.Buffer would accidentally promote an unbounded ReadFrom.
	_, err := io.Copy(output, io.LimitReader(strings.NewReader("123456"), 6))
	if err == nil || len(output.Bytes()) > 4 {
		t.Fatalf("output cap bypassed: bytes=%d err=%v", len(output.Bytes()), err)
	}
}
