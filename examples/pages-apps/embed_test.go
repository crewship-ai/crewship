package pagesdemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	run := func(t *testing.T) ([]byte, []byte, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, node, "--input-type=module")
		cmd.Stdin = strings.NewReader(source)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		started := time.Now()
		out, err := cmd.Output()
		// Cancellation is a failed test execution, not evidence that the
		// collector rejected invalid accounting. Keep the original timeout;
		// report its cause separately from an external kill or Node error.
		if ctx.Err() != nil {
			t.Fatalf("collector execution after %s: context=%v, process=%v, stderr=%q",
				time.Since(started), ctx.Err(), err, stderr.String())
		}
		return out, stderr.Bytes(), err
	}
	assertRejected := func(t *testing.T) {
		t.Helper()
		out, stderr, err := run(t)
		var exitErr *exec.ExitError
		const wantError = "Container resource sample failed; retaining the previous Page snapshot."
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || len(out) != 0 || strings.TrimSpace(string(stderr)) != wantError {
			t.Fatalf("accounting rejection must exit 1 with the collector diagnostic and no snapshot: process=%v, stdout=%q, stderr=%q", err, out, stderr)
		}
	}
	out, stderr, err := run(t)
	if err != nil || len(stderr) != 0 {
		t.Fatalf("valid accounting failed: process=%v, stdout=%q, stderr=%q", err, out, stderr)
	}
	var payload map[string]json.RawMessage
	if err = json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
	for panel, schema := range map[string]pages.PanelSchema{"services": "status.v1", "memory": "metric.v1", "fleet": "table.v1"} {
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
	for _, tc := range []struct{ name, file, value, valid string }{
		{"invalid-memory-limit", "memory.max", "bad", "4294967296"},
		{"zero-memory-limit", "memory.max", "0", "4294967296"},
		{"invalid-cpu-limit", "cpu.max", "bad 100000", "200000 100000"},
		{"zero-cpu-period", "cpu.max", "200000 0", "200000 100000"},
		{"missing-cpu-period", "cpu.max", "max", "200000 100000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.file)
			if err := os.WriteFile(path, []byte(tc.value), 0600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.WriteFile(path, []byte(tc.valid), 0600); err != nil {
					t.Error(err)
				}
			})
			assertRejected(t)
		})
	}
	if err = os.Remove(filepath.Join(dir, "cpu.stat")); err != nil {
		t.Fatal(err)
	}
	assertRejected(t)
}

func TestOperationsCollectorFleetTelemetry(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required for collector test")
	}
	dir := t.TempDir()
	for name, content := range map[string]string{"memory.current": "52428800", "memory.max": "4294967296", "cpu.stat": "usage_usec 100000\n", "cpu.max": "200000 100000"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"crews":[{"name":"Ops","slug":"ops","available":true,"containers":[{"name":"crew-ops","kind":"crew","status":"running","cpu_percent":1.26,"memory_mb":50}]},{"name":"Quality","slug":"quality","available":false,"containers":[]}]}`))
	}))
	defer server.Close()
	source := strings.ReplaceAll(string(Collector), "/sys/fs/cgroup/", filepath.ToSlash(dir)+"/")
	source = strings.Replace(source, "http://127.0.0.1:9119/crews/telemetry", server.URL, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=module")
	cmd.Stdin = strings.NewReader(source)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Fleet struct {
			Rows []struct {
				Crew   string  `json:"crew"`
				CPU    *string `json:"cpu"`
				Memory *string `json:"memory"`
			} `json:"rows"`
		} `json:"fleet"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Fleet.Rows) != 2 || payload.Fleet.Rows[0].Crew != "Ops" || payload.Fleet.Rows[0].CPU == nil || *payload.Fleet.Rows[0].CPU != "1.3%" || payload.Fleet.Rows[0].Memory == nil || *payload.Fleet.Rows[0].Memory != "50 MB" || payload.Fleet.Rows[1].CPU != nil {
		t.Fatalf("fleet telemetry = %+v", payload.Fleet.Rows)
	}
}
