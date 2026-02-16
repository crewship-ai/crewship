package orchestrator

import (
	"bufio"
	"context"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

func BuildCLICommand(req AgentRunRequest) []string {
	switch req.CLIAdapter {
	case "CLAUDE_CODE":
		cmd := []string{"claude", "--print"}
		if req.SystemPrompt != "" {
			cmd = append(cmd, "--system-prompt", req.SystemPrompt)
		}
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
		env = append(env, activeCred.EnvVarName+"="+activeCred.PlainValue)
	}

	for _, cred := range req.Credentials {
		if activeCred != nil && cred.ID == activeCred.ID {
			continue
		}
		if cred.EnvVarName != "" && cred.PlainValue != "" {
			alreadySet := false
			for _, e := range env {
				if len(e) > len(cred.EnvVarName) && e[:len(cred.EnvVarName)+1] == cred.EnvVarName+"=" {
					alreadySet = true
					break
				}
			}
			if !alreadySet {
				env = append(env, cred.EnvVarName+"="+cred.PlainValue)
			}
		}
	}

	return env
}

func (o *Orchestrator) streamOutput(ctx context.Context, result *provider.ExecResult, req AgentRunRequest, handler EventHandler) {
	defer result.Reader.Close()

	scanner := bufio.NewScanner(result.Reader)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

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

	if err := scanner.Err(); err != nil {
		o.logger.Debug("scanner error", "error", err, "agent_id", req.AgentID)
	}
}
