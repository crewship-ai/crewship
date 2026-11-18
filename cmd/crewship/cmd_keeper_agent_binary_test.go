package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Real Cobra root/compiled binary boundary. The sidecar reply here is a
// transport fixture, not full Keeper/Vault/tool authentication acceptance.
func TestKeeperAgentBinaryIgnoresHumanConfiguration(t *testing.T) {
	binary := buildCrewshipBinary(t)
	var humanCalls, sidecarCalls atomic.Int32
	human := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { humanCalls.Add(1); w.WriteHeader(500) }))
	defer human.Close()
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sidecarCalls.Add(1)
		if r.URL.Path != "/keeper/request" || r.Header.Get("Authorization") != "Bearer agtv2.fixture-original" {
			t.Error("compiled CLI used the wrong authority or route")
		}
		io.WriteString(w, `{"request_id":"kr_fixture","decision":"ALLOW","risk_score":1}`)
	}))
	defer sidecar.Close()
	corrupt := filepath.Join(t.TempDir(), "corrupt-human-config.yaml")
	if err := os.WriteFile(corrupt, []byte("profiles: [not: valid: yaml\ntoken: human-token-do-not-use\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, config, token string
		extra               []string
		wantOK              bool
	}{
		{"corrupt config", corrupt, "agtv2.fixture-original", nil, true},
		{"directory config", t.TempDir(), "agtv2.fixture-original", nil, true},
		{"no run capability", corrupt, "", nil, false},
		{"server override", corrupt, "agtv2.fixture-original", []string{"--server", human.URL}, false},
		{"profile override", corrupt, "agtv2.fixture-original", []string{"--profile", "human"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{}, tc.extra...)
			args = append(args, "keeper", "request", "NATIVE_SECRET", "--intent", "fixture authentication")
			cmd := exec.CommandContext(t.Context(), binary, args...)
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "CREWSHIP_") && !strings.HasPrefix(entry, "HOME=") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "HOME="+t.TempDir(), "CREWSHIP_CONFIG="+tc.config,
				"CREWSHIP_AGENT_TOKEN="+tc.token, "CREWSHIP_SIDECAR_URL="+sidecar.URL,
				"CREWSHIP_SERVER="+human.URL, "CREWSHIP_TOKEN=human-token-do-not-use", "CREWSHIP_PROFILE=human")
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.wantOK || (tc.wantOK && !strings.Contains(string(out), `"decision":"ALLOW"`)) {
				t.Fatalf("compiled CLI result mismatch: err=%v output=%s", err, out)
			}
			if strings.Contains(string(out), "human-token-do-not-use") || strings.Contains(string(out), "failed to load config") {
				t.Fatal("agent command read or exposed the human configuration")
			}
		})
	}
	if humanCalls.Load() != 0 || sidecarCalls.Load() != 2 {
		t.Fatalf("human fallback or invalid-call forwarding: human=%d sidecar=%d", humanCalls.Load(), sidecarCalls.Load())
	}
}
