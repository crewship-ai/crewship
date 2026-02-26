package apple

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestDetect(t *testing.T) {
	_, err := exec.LookPath("container")
	if err != nil {
		t.Skipf("container CLI not available: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := Detect(ctx)
	if err != nil {
		t.Skipf("Apple Container runtime not available: %v", err)
	}
	if result.Version == "" {
		t.Fatal("expected non-empty version from Detect")
	}
	if result.HostIP == "" {
		t.Fatal("expected non-empty host IP from Detect")
	}
	t.Logf("detected Apple Containers version=%s host_ip=%s", result.Version, result.HostIP)
}
