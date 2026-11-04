package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Exercise the process boundary: scripts must receive the same validation code
// in the process status and error envelope, before admission or authentication.
func TestAcceptance_CraftVersionInvalidInput(t *testing.T) {
	binary := buildCrewshipBinary(t)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected request", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	for _, token := range []string{"", "synthetic-craft-validation-token"} {
		name := "without_credentials"
		if token != "" {
			name = "with_credentials"
		}
		t.Run(name, func(t *testing.T) {
			config := filepath.Join(t.TempDir(), "cli.yaml")
			if err := os.WriteFile(config, []byte("server: "+server.URL+"\nworkspace: ccraftvalidationtest00001\ntoken: "+token+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, version := range []string{"0", "-1", "01", "+1", "1.0", "1e3", " 1", "1 ", "", "9223372036854775808", "18446744073709551615"} {
				t.Run(version, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
					defer cancel()
					cmd := exec.CommandContext(ctx, binary, "--format", "json", "craft", "version", "@does-not-exist", "--", version)
					cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+config, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
					var stdout, stderr bytes.Buffer
					cmd.Stdout, cmd.Stderr = &stdout, &stderr
					err := cmd.Run()
					if err == nil {
						t.Fatal("invalid version unexpectedly succeeded")
					}
					if got := exitCodeOf(t, err); got != cli.ExitValidation {
						t.Errorf("process exit = %d, want validation %d; stderr: %s", got, cli.ExitValidation, stderr.String())
					}
					var envelope cli.ErrorEnvelope
					if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
						t.Fatalf("error envelope: %v; stderr: %s", err, stderr.String())
					}
					if envelope.Error.ExitCode != cli.ExitValidation || !strings.Contains(envelope.Error.Message, "version must be a positive integer") {
						t.Errorf("unexpected validation envelope: %+v", envelope)
					}
					if stdout.Len() != 0 {
						t.Errorf("failure wrote success output: %s", stdout.String())
					}
					if got := requests.Load(); got != 0 {
						t.Fatalf("invalid syntax reached HTTP transport: %d requests", got)
					}
				})
			}
		})
	}
}
