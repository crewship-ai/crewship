package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/runoutput"
)

func TestDurableExecPreparesInputsWithoutLaunching(t *testing.T) {
	// No provider is installed: preparation must not perform any container IO
	// before the ordinary RunAgent ExecGate accepts process creation.
	o := &Orchestrator{}
	req := AgentRunRequest{RunID: "run-1", AgentSlug: "agent", ContainerID: "container", CLIAdapter: "CLAUDE_CODE", TimeoutSecs: 60, DurableOutputDir: "/persistent/runs", UserMessage: strings.Repeat("x", 200*1024)}
	req.durableDeadline = time.Now().Add(30 * time.Second)
	cfg, err := o.buildExecCommand(t.Context(), req, []string{"claude"}, []string{"TEST=value"}, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cmd[0] != "/usr/local/bin/crewship-sidecar" || cfg.Cmd[1] != "run-launch" || cfg.User != "1001:1001" {
		t.Fatalf("unexpected exec: %+v", cfg)
	}
	var spec runoutput.LaunchInput
	if err := json.NewDecoder(cfg.Stdin).Decode(&spec); err != nil {
		t.Fatal(err)
	}
	if !spec.Deadline.Equal(req.durableDeadline) {
		t.Fatal("exec preparation reset admitted deadline")
	}
	if string(spec.Command.Stdin) != req.UserMessage || spec.Command.Dir != "/workspace" || len(spec.Command.Env) != 0 {
		t.Fatal("launch input loses prompt/cwd or embeds env before admission")
	}
	for _, arg := range cfg.Cmd {
		if strings.Contains(arg, req.UserMessage) {
			t.Fatal("prompt leaked into helper argv")
		}
	}
}

func TestDurableExecRejectsAmbiguousIdentity(t *testing.T) {
	for _, root := range []string{"relative", "/persistent/../runs", "/runs\x00"} {
		_, err := buildDurableExecCommand(AgentRunRequest{RunID: "run-1", DurableOutputDir: root, TimeoutSecs: 1}, []string{"true"}, nil, "")
		if err == nil {
			t.Fatalf("accepted %q", root)
		}
	}
}
