package orchestrator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/runoutput"
)

// This function prepares an exec only. The caller's existing ExecGate must
// still run before container.Exec: preparation must never start a workload.
func buildDurableExecCommand(req AgentRunRequest, args, env []string, workDir string) (provider.ExecConfig, error) {
	directory, err := durableRunDirectory(req)
	if err != nil {
		return provider.ExecConfig{}, err
	}
	if req.TimeoutSecs <= 0 {
		return provider.ExecConfig{}, fmt.Errorf("durable execution requires an explicit positive timeout")
	}
	spec := runoutput.LaunchInput{
		Command:  runoutput.Command{Args: args, Dir: workDir},
		Deadline: req.durableDeadline,
	}
	if spec.Deadline.IsZero() {
		spec.Deadline = time.Now().Add(time.Duration(req.TimeoutSecs) * time.Second)
	}
	if getAdapter(req.CLIAdapter).PromptViaStdin(req) {
		spec.Command.Stdin = []byte(req.UserMessage)
	} else if tooLarge, _ := firstOversizedArg(args); tooLarge {
		return provider.ExecConfig{}, fmt.Errorf("agent argument exceeds execve limit")
	}
	data, err := json.Marshal(spec)
	if err != nil || len(data) > 8<<20 {
		return provider.ExecConfig{}, fmt.Errorf("durable execution input exceeds supported envelope")
	}
	return provider.ExecConfig{
		ContainerID: req.ContainerID,
		// Use the provider's read-only mounted helper, never an agent PATH entry.
		Cmd: []string{"/usr/local/bin/crewship-sidecar", "run-launch", "--dir", directory, "--session", TmuxSessionName(req.AgentSlug, req.RunID)},
		Env: env, WorkingDir: workDir, User: "1001:1001", Stdin: bytes.NewReader(data),
	}, nil
}

func durableRunDirectory(req AgentRunRequest) (string, error) {
	if !ValidRunID(req.RunID) || !path.IsAbs(req.DurableOutputDir) || path.Clean(req.DurableOutputDir) != req.DurableOutputDir || strings.ContainsRune(req.DurableOutputDir, 0) {
		return "", fmt.Errorf("invalid durable execution identity or directory")
	}
	return path.Join(req.DurableOutputDir, req.RunID), nil
}
