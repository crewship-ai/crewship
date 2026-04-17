package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// shellHandler executes handler_config.command as a shell script via
// `sh -c`. The command runs with:
//
//   - A context-bound timeout (default 30s, override via
//     handler_config.timeout_secs). The exec.CommandContext is killed when
//     the timeout fires.
//   - A curated environment that carries the event scope and payload so
//     scripts don't have to parse argv. Parent env is NOT inherited beyond
//     what's needed for basic sh resolution (PATH), keeping the blast
//     radius small if a shell hook is compromised.
//   - stdout + stderr captured; each truncated to 4 KB in Result.Payload so
//     the journal doesn't balloon when a script spews MB of logs.
//
// Exit code 0 produces OutcomePass. Non-zero produces OutcomeBlock so the
// dispatcher's blocking logic can short-circuit; non-blocking hooks
// degrade OutcomeBlock to a logged warning upstream.
//
// NOTE: This is dockerless. On Linux we would layer seccomp / cgroup / uid
// isolation; today we rely on the hook being OWNER-only and on the caller
// having audited the command. See CLAUDE.md about sidecar UID boundaries —
// shell hooks run in the crewshipd process, not in an agent container.
func shellHandler(ctx context.Context, h Hook, ec EventContext) (Result, error) {
	start := time.Now()

	command, _ := h.HandlerConfig["command"].(string)
	if command == "" {
		return Result{
			Outcome: OutcomeError,
			Message: "shell handler missing handler_config.command",
			Latency: time.Since(start),
		}, fmt.Errorf("shell: empty command")
	}

	timeout := 30 * time.Second
	if t, ok := h.HandlerConfig["timeout_secs"].(float64); ok && t > 0 {
		timeout = time.Duration(t) * time.Second
	}
	if t, ok := h.HandlerConfig["timeout_secs"].(int); ok && t > 0 {
		timeout = time.Duration(t) * time.Second
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "sh", "-c", command)
	// Put the shell and every child it spawns in a fresh process group so
	// context timeout can kill the whole subtree. Without Setpgid, Linux
	// only sends SIGKILL to the immediate sh child and a grandchild
	// `sleep 5` runs to completion while cmd.Run() blocks on its inherited
	// stdout/stderr pipes — the symptom TestShellHandlerTimeout caught on
	// the dev VM. cmd.Cancel kills the whole group instead of just sh.
	configureProcessGroup(cmd)

	payloadJSON, _ := json.Marshal(ec.Payload)
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"CREWSHIP_EVENT=" + string(ec.Event),
		"CREWSHIP_WORKSPACE_ID=" + ec.WorkspaceID,
		"CREWSHIP_CREW_ID=" + ec.CrewID,
		"CREWSHIP_AGENT_ID=" + ec.AgentID,
		"CREWSHIP_MISSION_ID=" + ec.MissionID,
		"CREWSHIP_TOOL_NAME=" + ec.ToolName,
		"CREWSHIP_PAYLOAD=" + string(payloadJSON),
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	latency := time.Since(start)

	payload := map[string]any{
		"stdout": truncate(stdout.String(), 4096),
		"stderr": truncate(stderr.String(), 4096),
	}

	// Context timeout fires as err != nil with ctx.Err() != nil.
	if cctx.Err() == context.DeadlineExceeded {
		payload["timed_out"] = true
		return Result{
			Outcome: OutcomeBlock,
			Message: fmt.Sprintf("shell hook timed out after %s", timeout),
			Latency: latency,
			Payload: payload,
		}, nil
	}

	if err != nil {
		// ExitError is "script ran but exited non-zero" — that's a Block
		// vote, not a handler-error. Anything else (sh not found, context
		// canceled by caller, etc.) is an internal error.
		if _, ok := err.(*exec.ExitError); ok {
			payload["exit_code"] = cmd.ProcessState.ExitCode()
			return Result{
				Outcome: OutcomeBlock,
				Message: truncate(stderr.String(), 256),
				Latency: latency,
				Payload: payload,
			}, nil
		}
		return Result{
			Outcome: OutcomeError,
			Message: err.Error(),
			Latency: latency,
			Payload: payload,
		}, err
	}

	payload["exit_code"] = 0
	return Result{
		Outcome: OutcomePass,
		Message: truncate(stdout.String(), 256),
		Latency: latency,
		Payload: payload,
	}, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}
