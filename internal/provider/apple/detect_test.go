package apple

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"
)

func TestDetectDebug(t *testing.T) {
	path, err := exec.LookPath("container")
	fmt.Printf("  container CLI: %s (err: %v)\n", path, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := Detect(ctx)
	if err != nil {
		fmt.Println("Detect error:", err)
		t.Skipf("Apple Container runtime not available: %v", err)
		return
	}
	fmt.Printf("Result: version=%s host_ip=%s\n", result.Version, result.HostIP)
}
