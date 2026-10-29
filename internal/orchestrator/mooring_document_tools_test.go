package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/provider"
)

func TestMooringDocumentToolsKeepOriginalPromptOffArgvAndRemoveGenericTools(t *testing.T) {
	req := covRunReq()
	req.UserMessage = "synthetic private original"
	req.CrewMCPConfigJSON, req.AgentMCPConfigJSON = "synthetic-crew", "synthetic-agent"
	private, err := req.WithMooringDocumentTools()
	if err != nil {
		t.Fatal(err)
	}
	args := (claudeCodeAdapter{}).BuildCommand(private)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, req.UserMessage) || !claudePromptViaStdin(private) || private.CrewMCPConfigJSON != "" || private.AgentMCPConfigJSON != "" || !private.strictContentPrivacy {
		t.Fatal("private tool selection leaked original or generic configuration")
	}
	value := func(key string) string {
		for i, arg := range args {
			if arg == key && i+1 < len(args) {
				return args[i+1]
			}
		}
		return "absent"
	}
	if value("--tools") != "" || value("--setting-sources") != "" || !strings.Contains(joined, "--strict-mcp-config") || !strings.Contains(joined, "--no-session-persistence") {
		t.Fatal("private CLI isolation flags missing")
	}
	ordinary := (claudeCodeAdapter{}).BuildCommand(req)
	if !strings.Contains(strings.Join(ordinary, " "), req.UserMessage) || claudePromptViaStdin(req) {
		t.Fatal("ordinary prompt behavior changed")
	}
	for _, mutate := range []func(*AgentRunRequest){func(r *AgentRunRequest) { r.CLIAdapter = "CODEX" }, func(r *AgentRunRequest) { r.SkipSidecar = true }, func(r *AgentRunRequest) { r.RunID = "" }, func(r *AgentRunRequest) { r.WorkspaceID = "" }} {
		bad := req
		mutate(&bad)
		if _, err := bad.WithMooringDocumentTools(); err == nil {
			t.Fatal("unsupported task execution accepted")
		}
	}
	var decoded AgentRunRequest
	if json.Unmarshal([]byte(`{"mooringDocumentTools":true}`), &decoded) != nil || decoded.mooringDocumentTools {
		t.Fatal("JSON selected private execution")
	}
}

// Synthetic providers exercise computed-exec boundaries, not actual native CLI
// privacy, container contents, sidecar deployment or document authority.
func TestMooringDocumentExecRequiresActualBoundedHealthyProbeAndExactExec(t *testing.T) {
	req, err := covRunReq().WithMooringDocumentTools()
	if err != nil {
		t.Fatal(err)
	}
	req.sidecarActive = true
	const token = "agtv2.x"
	for _, mode := range []string{"valid", "stale", "crew-token", "config", "health-overflow", "probe-running", "home-mismatch", "native-tools-no-stdin", "duplicate-home", "wrong-run-token"} {
		t.Run(mode, func(t *testing.T) {
			ctr := &covContainer{expectedHash: "synthetic-sidecar"}
			o := New(ctr, newMemState(), covQuietLogger())
			o.ipcToken = "synthetic-private-master"
			health := sidecarHealth{Status: "ok", SidecarHash: ctr.expectedHash, TokenFP: internaltoken.Fingerprint(sidecarIPCToken(o.ipcToken, req.WorkspaceID, req.CrewID, privateRunLogger)), ConfigFingerprint: gridcraftSidecarFingerprint(o.ipcToken, req.Credentials, false)}
			if mode == "stale" {
				health.SidecarHash = "older"
			}
			if mode == "crew-token" {
				health.TokenFP = "other"
			}
			if mode == "config" {
				health.ConfigFingerprint = "other"
			}
			ctr.route = func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
				body, id := "ready\n", "files"
				if cfg.User == "1002:1002" {
					b, _ := json.Marshal(health)
					body, id = string(b), "health"
					if mode == "health-overflow" {
						body = strings.Repeat("x", (16<<10)+1)
					}
				} else if mode == "home-mismatch" {
					body = ""
				}
				return &provider.ExecResult{ExecID: id, Reader: io.NopCloser(strings.NewReader(body))}, nil
			}
			ctr.inspect = func(id string) (bool, int, error) { return mode == "probe-running" && id == "health", 0, nil }
			cfg := provider.ExecConfig{ContainerID: req.ContainerID, User: "1001:1001", Stdin: strings.NewReader(req.UserMessage), Env: []string{"HOME=" + agentHomeDir(req.AgentSlug, req.RunID), "CREWSHIP_AGENT_TOKEN=" + token}}
			if mode == "native-tools-no-stdin" {
				cfg.Stdin = nil
			}
			if mode == "duplicate-home" {
				cfg.Env = append(cfg.Env, cfg.Env[0])
			}
			if mode == "wrong-run-token" {
				cfg.Env[1] = "CREWSHIP_AGENT_TOKEN=other"
			}
			err := o.qualifyMooringDocumentExec(context.Background(), req, cfg, token)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("computed-exec guard: %v", err)
			}
		})
	}
}
