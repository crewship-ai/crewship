package docker

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestDetectDebug(t *testing.T) {
	fmt.Println("DOCKER_HOST:", os.Getenv("DOCKER_HOST"))

	for _, c := range candidateSockets() {
		_, err := os.Stat(c.path)
		exists := err == nil
		fmt.Printf("  socket: %-60s runtime: %-10s exists: %v\n", c.path, c.runtime, exists)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := Detect(ctx)
	if err != nil {
		fmt.Println("Detect error:", err)
		return
	}
	fmt.Printf("Result: runtime=%s socket=%s version=%s\n", result.Runtime, result.Socket, result.Version)
}
