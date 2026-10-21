package mooring

import (
	"context"
	"errors"
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
