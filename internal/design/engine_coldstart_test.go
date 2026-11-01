package design

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A loaded CI shard once took more than 5 s just to start node (#4496). Slow
// start-up alone must not be reported as an unavailable engine: the real
// engine runs behind a node that needs 6 s before it even begins.
func TestRunToleratesSlowColdStartWithinDefaultBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for a deliberately slow start")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	dir := t.TempDir()
	slow := "#!/bin/sh\nsleep 6\nexec " + node + " \"$@\"\n"
	if err = os.WriteFile(filepath.Join(dir, "node"), []byte(slow), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	started := time.Now()
	result, err := Run(t.Context(), Request{Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"slow","type":"rect","x":0,"y":0,"width":10,"height":10}}]`)})
	if err != nil {
		t.Fatalf("Run after %s: %v", time.Since(started).Round(time.Millisecond), err)
	}
	if len(result.Nodes) != 1 || len(result.State) == 0 {
		t.Fatalf("result = %+v", result)
	}
}
