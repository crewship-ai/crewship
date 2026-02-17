package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

const crewshipSystemPreamble = `You are running inside a Crewship agent container.
Working directory: your current directory is your private workspace.
Output directory: save all deliverables (files the user should see/download) to the path in $CREWSHIP_OUTPUT_DIR.
Do NOT attempt to write outside /workspace or /output -- the filesystem is read-only elsewhere.
`

func BuildCLICommand(req AgentRunRequest) []string {
	switch req.CLIAdapter {
	case "CLAUDE_CODE":
		cmd := []string{
			"claude", "--print",
			"--no-session-persistence",
			"--dangerously-skip-permissions",
			"--verbose",
		}
		systemPrompt := crewshipSystemPreamble + req.SystemPrompt
		cmd = append(cmd, "--system-prompt", systemPrompt)
		if req.ToolProfile == "MINIMAL" {
			cmd = append(cmd, "--tools", "Read,Search,Grep")
		}
		cmd = append(cmd, req.UserMessage)
		return cmd

	case "CODEX_CLI":
		cmd := []string{"codex", "--quiet"}
		if req.ToolProfile == "CODING" {
			cmd = append(cmd, "--sandbox")
		}
		cmd = append(cmd, req.UserMessage)
		return cmd

	case "GEMINI_CLI":
		return []string{"gemini", "-p", req.UserMessage}

	case "OPENCODE":
		return []string{"opencode", "run", req.UserMessage}

	default:
		return []string{"claude", "--print", req.UserMessage}
	}
}

func BuildEnvVars(req AgentRunRequest, activeCred *Credential) []string {
	env := []string{
		"CREWSHIP_AGENT_ID=" + req.AgentID,
		"CREWSHIP_TEAM_ID=" + req.TeamID,
		"CREWSHIP_SESSION_ID=" + req.SessionID,
	}

	if activeCred != nil {
		envVar := resolveEnvVar(activeCred)
		env = append(env, envVar+"="+activeCred.PlainValue)
	}

	for _, cred := range req.Credentials {
		if activeCred != nil && cred.ID == activeCred.ID {
			continue
		}
		if cred.EnvVarName != "" && cred.PlainValue != "" {
			envVar := resolveEnvVar(&cred)
			alreadySet := false
			for _, e := range env {
				if len(e) > len(envVar) && e[:len(envVar)+1] == envVar+"=" {
					alreadySet = true
					break
				}
			}
			if !alreadySet {
				env = append(env, envVar+"="+cred.PlainValue)
			}
		}
	}

	return env
}

// resolveEnvVar returns the correct env var name for a credential.
// AI_CLI_TOKEN (OAuth setup tokens) use CLAUDE_CODE_OAUTH_TOKEN for Claude Code.
func resolveEnvVar(cred *Credential) string {
	if cred.Type == "AI_CLI_TOKEN" {
		return "CLAUDE_CODE_OAUTH_TOKEN"
	}
	return cred.EnvVarName
}

// credentialsJSON is the format Claude CLI expects at ~/.claude/.credentials.json
type credentialsJSON struct {
	ClaudeAiOauth oauthEntry `json:"claudeAiOauth"`
}

type oauthEntry struct {
	AccessToken  string   `json:"accessToken"`
	RefreshToken string   `json:"refreshToken"`
	ExpiresAt    string   `json:"expiresAt"`
	Scopes       []string `json:"scopes"`
}

// claudeConfigJSON is ~/.claude.json -- skips onboarding in the container.
type claudeConfigJSON struct {
	HasCompletedOnboarding   bool `json:"hasCompletedOnboarding"`
	HasAvailableSubscription bool `json:"hasAvailableSubscription"`
	AutoUpdates              bool `json:"autoUpdates"`
}

// setupClaudeCredentials writes OAuth credential files into the agent container
// so that `claude --print` can authenticate with a Pro/Max subscription token.
// This follows the pattern from cabinlab/claude-code-sdk-docker.
func setupClaudeCredentials(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	cred *Credential,
	logger *slog.Logger,
) error {
	if cred == nil || cred.PlainValue == "" {
		return nil
	}
	if cred.Type != "AI_CLI_TOKEN" {
		return nil // only inject files for OAuth setup tokens
	}

	token := cred.PlainValue

	credsData, err := json.Marshal(credentialsJSON{
		ClaudeAiOauth: oauthEntry{
			AccessToken:  token,
			RefreshToken: token,
			ExpiresAt:    "2099-12-31T23:59:59.999Z",
			Scopes:       []string{"user:inference", "user:profile"},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal credentials.json: %w", err)
	}

	configData, err := json.Marshal(claudeConfigJSON{
		HasCompletedOnboarding:   true,
		HasAvailableSubscription: true,
		AutoUpdates:              false,
	})
	if err != nil {
		return fmt.Errorf("marshal claude.json: %w", err)
	}

	// Write credentials file and patch .claude.json (merge if exists, create if not).
	// Uses jq if available for merging, falls back to overwrite.
	script := fmt.Sprintf(
		`mkdir -p /home/agent/.claude && `+
			`cat > /home/agent/.claude/.credentials.json << 'CREDEOF'
%s
CREDEOF
if command -v jq >/dev/null 2>&1 && [ -f /home/agent/.claude.json ]; then
  jq '. + {"hasCompletedOnboarding":true,"hasAvailableSubscription":true,"autoUpdates":false}' /home/agent/.claude.json > /tmp/.claude.json.tmp && mv /tmp/.claude.json.tmp /home/agent/.claude.json
else
  cat > /home/agent/.claude.json << 'CFGEOF'
%s
CFGEOF
fi
chmod 600 /home/agent/.claude/.credentials.json /home/agent/.claude.json`,
		strings.ReplaceAll(string(credsData), "'", "'\\''"),
		strings.ReplaceAll(string(configData), "'", "'\\''"),
	)

	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
		User:        "1001:1001",
	}

	result, err := container.Exec(ctx, cfg)
	if err != nil {
		return fmt.Errorf("write claude credentials: %w", err)
	}
	io.Copy(io.Discard, result.Reader)
	result.Reader.Close()

	logger.Debug("claude credentials injected", "container_id", containerID[:min(12, len(containerID))])
	return nil
}

func (o *Orchestrator) streamOutput(ctx context.Context, result *provider.ExecResult, req AgentRunRequest, handler EventHandler) {
	var closeOnce sync.Once
	closeReader := func() {
		closeOnce.Do(func() {
			result.Reader.Close()
		})
	}
	defer closeReader()

	go func() {
		<-ctx.Done()
		closeReader()
	}()

	scanner := bufio.NewScanner(result.Reader)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		event := AgentEvent{
			Type:      "text",
			Content:   line,
			Timestamp: time.Now(),
		}

		if handler != nil {
			handler(event)
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		o.logger.Debug("scanner error", "error", err, "agent_id", req.AgentID)
	}
}
