package main

// `crewship run list -o yaml` and the metadata blob, driven through the BUILT
// binary.
//
// A run's `metadata` is an opaque JSON document. Decoded into a raw byte
// slice and handed to yaml.v3 it renders as a sequence of integers — one line
// per byte — so a listing of a dozen runs answered with several hundred
// numbers and no readable field. JSON and ndjson were unaffected, which is why
// it went unnoticed: the format an operator reaches for when they want to READ
// the output is the one that broke.
//
// `run get -o yaml` goes through the same decode (AutoDetail) and had the same
// output.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const runsWithMetadataJSON = `{"data":[
	{"id":"crn00000000000000000one","agent_id":"cag1","workspace_id":"ws1",
	 "trigger_type":"MANUAL","status":"COMPLETED","created_at":"2026-08-12T10:00:00Z",
	 "agent_slug":"amy","metadata":{"tokens_in":1240,"tokens_out":880,"model":"claude-opus-5"}}
]}`

const oneRunWithMetadataJSON = `{"id":"crn00000000000000000one","agent_id":"cag1","workspace_id":"ws1",
	"trigger_type":"MANUAL","status":"COMPLETED","created_at":"2026-08-12T10:00:00Z",
	"agent_slug":"amy","metadata":{"tokens_in":1240,"tokens_out":880,"model":"claude-opus-5"}}`

func runYAMLConfig(t *testing.T) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(cfgPath,
		[]byte("token: test-token\nworkspace: c00000000000000000run\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func runsStub(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/runs":
			_, _ = w.Write([]byte(runsWithMetadataJSON))
		case "/api/v1/runs/crn00000000000000000one":
			_, _ = w.Write([]byte(oneRunWithMetadataJSON))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestAcceptance_RunYAML_MetadataIsAMappingNotBytes(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv := runsStub(t)
	defer srv.Close()

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"run list", []string{"run", "list", "--format", "yaml"}},
		{"run get", []string{"run", "get", "crn00000000000000000one", "--format", "yaml"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runCrewship(t, bin, runYAMLConfig(t), srv.URL, append(tc.args, "--no-color")...)
			if err != nil {
				t.Fatalf("run: %v\n%s", err, out)
			}
			// 123 is the byte value of "{". A document rendered as its bytes
			// opens with it, every time.
			if strings.Contains(out, "- 123") {
				t.Fatalf("metadata rendered as a byte sequence:\n%s", out)
			}
			for _, want := range []string{"tokens_in: 1240", "tokens_out: 880", "model: claude-opus-5"} {
				if !strings.Contains(out, want) {
					t.Errorf("output does not contain %q:\n%s", want, out)
				}
			}
			// One document, not one line per byte: a run with three metadata
			// keys cannot need fifty lines.
			if n := strings.Count(out, "\n"); n > 40 {
				t.Errorf("output is %d lines for one run — the blob is still being "+
					"spelled out:\n%s", n, out)
			}
		})
	}
}
