package pagesdemo

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pages"
)

func TestOperationsCollectorPayloads(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		// SKIP-WAIVER(#2472): collector execution needs Node; the Node-equipped CI job exercises the examples.
		t.Skip("Node required for collector test")
	}
	dir := t.TempDir()
	for name, content := range map[string]string{"memory.current": "52428800", "memory.max": "4294967296", "cpu.stat": "usage_usec 100000\n", "cpu.max": "200000 100000"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Substitute only the cgroup mount in this fixture; execute the embedded source.
	source := strings.ReplaceAll(string(Collector), "/sys/fs/cgroup/", filepath.ToSlash(dir)+"/")
	run := func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, node, "--input-type=module")
		cmd.Stdin = strings.NewReader(source)
		return cmd.Output()
	}
	out, err := run()
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err = json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
	for panel, schema := range map[string]pages.PanelSchema{"services": "status.v1", "memory": "metric.v1"} {
		if _, err := pages.ValidatePayload(schema, payload[panel]); err != nil {
			t.Fatal(err)
		}
	}
	var memory struct {
		Value   int   `json:"value"`
		Samples []int `json:"sparkline"`
	}
	if err = json.Unmarshal(payload["memory"], &memory); err != nil || memory.Value != 50 || len(memory.Samples) != 8 {
		t.Fatalf("invalid measurement: %+v %v", memory, err)
	}
	if err = os.Remove(filepath.Join(dir, "cpu.stat")); err != nil {
		t.Fatal(err)
	}
	if out, err = run(); err == nil || len(out) != 0 {
		t.Fatalf("missing accounting published a healthy snapshot: %s %v", out, err)
	}
}
