package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var aiClientFixture struct {
	once sync.Once
	data []byte
	err  error
}

// Share immutable fixture bytes across reconnect/status tests and -count runs.
// Each scenario still gets isolated native-client and configuration paths.
func buildAIClientFixture(t *testing.T) []byte {
	t.Helper()
	aiClientFixture.once.Do(func() {
		dir, err := os.MkdirTemp("", "ai-client-fixture-")
		if err != nil {
			aiClientFixture.err = err
			return
		}
		defer os.RemoveAll(dir)
		name := "ai-client"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		path := filepath.Join(dir, name)
		build := exec.Command("go", "build", "-o", path, "./testdata/ai-reconnect-client")
		if output, err := build.CombinedOutput(); err != nil {
			aiClientFixture.err = fmt.Errorf("build client fixture: %w\n%s", err, output)
			return
		}
		aiClientFixture.data, aiClientFixture.err = os.ReadFile(path)
	})
	if aiClientFixture.err != nil {
		t.Fatal(aiClientFixture.err)
	}
	return aiClientFixture.data
}
