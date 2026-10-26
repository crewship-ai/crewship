package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/provider"
)

// This selects a tool surface, never a document/run authorization. Every tool
// call still needs the actual attached host mandate and current Page rights.
const mooringDocumentMCP = `{"mcpServers":{"crewship-document":{"type":"http","url":"http://127.0.0.1:9119/mcp/mooring","headers":{"Authorization":"Bearer ${CREWSHIP_AGENT_TOKEN}"},"alwaysLoad":true}}}`

func (r AgentRunRequest) WithMooringDocumentTools() (AgentRunRequest, error) {
	if r.CLIAdapter != "CLAUDE_CODE" || r.SkipSidecar || r.WorkspaceID == "" || r.AgentID == "" || r.CrewID == "" || !ValidRunID(r.RunID) {
		return AgentRunRequest{}, ErrPrivateRunUnqualified
	}
	r = r.WithStrictContentPrivacy()
	r.mooringDocumentTools = true
	// Neither configured MCP servers nor native skills may broaden this one
	// private document task into another content/file/network tool surface.
	r.CrewMCPConfigJSON, r.AgentMCPConfigJSON = "", ""
	r.MCPServers, r.Skills = nil, nil
	return r, nil
}

func writeMooringDocumentMCP(ctx context.Context, container provider.ContainerProvider, containerID string, req AgentRunRequest) error {
	if !req.mooringDocumentTools || !req.strictContentPrivacy || req.CLIAdapter != "CLAUDE_CODE" || containerID != req.ContainerID {
		return ErrPrivateRunUnqualified
	}
	home := agentHomeDir(req.AgentSlug, req.RunID)
	encoded := base64.StdEncoding.EncodeToString([]byte(mooringDocumentMCP))
	script := "printf %s " + shellJoin(encoded) + " | base64 -d > " + shellJoin(home+"/.mcp.json") + " && chmod 600 " + shellJoin(home+"/.mcp.json")
	if err := runOrBatch(ctx, container, preflightStepMCPConfig, provider.ExecConfig{ContainerID: containerID, Cmd: []string{"sh", "-c", script}, User: "1001:1001"}); err != nil {
		return ErrPrivateRunUnqualified
	}
	return nil
}

func mooringExecEnvEquals(env []string, key, expected string) bool {
	count := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, key+"=") {
			if entry != key+"="+expected {
				return false
			}
			count++
		}
	}
	return expected != "" && count == 1
}

// Verify the actual computed exec and run-local file after preflight, before
// the original ExecGate/provider creation. Only this supported native adapter
// and the current actual sidecar can qualify; ordinary runs never enter here.
func (o *Orchestrator) qualifyMooringDocumentExec(ctx context.Context, req AgentRunRequest, cfg provider.ExecConfig, token string) error {
	if !req.mooringDocumentTools {
		return nil
	}
	home := agentHomeDir(req.AgentSlug, req.RunID)
	if !req.strictContentPrivacy || req.CLIAdapter != "CLAUDE_CODE" || !req.sidecarActive || cfg.ContainerID != req.ContainerID || cfg.User != "1001:1001" || cfg.Stdin == nil || !strings.HasPrefix(token, "agtv2.") || !mooringExecEnvEquals(cfg.Env, "HOME", home) || !mooringExecEnvEquals(cfg.Env, "CREWSHIP_AGENT_TOKEN", token) {
		return ErrPrivateRunUnqualified
	}
	reporter, ok := o.container.(sidecarHashReporter)
	if !ok || reporter.ExpectedSidecarHash() == "" {
		return ErrPrivateRunUnqualified
	}
	health := mooringDocumentSidecarHealth(ctx, o.container, req.ContainerID)
	o.mu.RLock()
	ipcToken, gridEnabled := o.ipcToken, o.gridCraftEnabled
	o.mu.RUnlock()
	if health == nil || health.Stale || health.SidecarHash != reporter.ExpectedSidecarHash() || health.TokenFP != internaltoken.Fingerprint(sidecarIPCToken(ipcToken, req.WorkspaceID, req.CrewID, privateRunLogger)) || health.ConfigFingerprint != gridcraftSidecarFingerprint(ipcToken, req.Credentials, gridEnabled) {
		return ErrPrivateRunUnqualified
	}
	digest := sha256.Sum256([]byte(mooringDocumentMCP))
	script := fmt.Sprintf("test -d %[1]s && test ! -L %[1]s && test \"$(readlink -f %[1]s)\" = %[1]s && test \"$(stat -c '%%u:%%g:%%a' %[2]s)\" = 1001:1001:600 && test \"$(sha256sum %[2]s | cut -d ' ' -f1)\" = %[3]s && printf 'ready\\n'", shellJoin(home), shellJoin(home+"/.mcp.json"), shellJoin(hex.EncodeToString(digest[:])))
	result, err := o.container.Exec(ctx, provider.ExecConfig{ContainerID: req.ContainerID, Cmd: []string{"sh", "-c", script}, User: "1001:1001"})
	if err != nil || result == nil || result.Reader == nil || result.ExecID == "" {
		return ErrPrivateRunUnqualified
	}
	defer result.Reader.Close()
	raw, err := io.ReadAll(io.LimitReader(result.Reader, 17))
	if err != nil || string(raw) != "ready\n" {
		return ErrPrivateRunUnqualified
	}
	running, exit, err := o.container.ExecInspect(ctx, result.ExecID)
	if err != nil || running || exit != 0 {
		return ErrPrivateRunUnqualified
	}
	return nil
}

// The private task does not consume unbounded provider diagnostics. This is
// the existing loopback health probe with a closed output bound and confirmed
// probe termination; it does not mint an agent or document authorization.
func mooringDocumentSidecarHealth(ctx context.Context, container provider.ContainerProvider, id string) *sidecarHealth {
	result, err := container.Exec(ctx, provider.ExecConfig{ContainerID: id, Cmd: []string{"sh", "-c", "curl -sf --max-time 2 http://127.0.0.1:9119/health 2>/dev/null || wget -q -T 2 -O - http://127.0.0.1:9119/health 2>/dev/null"}, User: "1002:1002"})
	if err != nil || result == nil || result.Reader == nil || result.ExecID == "" {
		return nil
	}
	defer result.Reader.Close()
	raw, err := io.ReadAll(io.LimitReader(result.Reader, (16<<10)+1))
	if err != nil || len(raw) > 16<<10 {
		return nil
	}
	running, exit, err := container.ExecInspect(ctx, result.ExecID)
	var health sidecarHealth
	if err != nil || running || exit != 0 || json.Unmarshal(raw, &health) != nil || health.Status != "ok" {
		return nil
	}
	return &health
}
