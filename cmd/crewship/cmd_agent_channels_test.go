package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// `notifychannel agents list <channel>` answers "who may post HERE". Its
// mirror — "what can THIS agent reach" — had no CLI at all, because it had no
// route. Both directions matter for an audit: the first tells you a channel is
// over-shared, the second tells you an agent is over-privileged, and neither
// can be derived from the other without walking every channel.
func TestAgentChannelsCmd_ListsWhatAnAgentMayReach(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/agents/ag_1/notification-channels", clitest.JSONResponse(200, map[string]any{
		"channels": []map[string]any{
			{"id": "nch_1", "type": "shoutrrr", "provider": "discord", "enabled": true},
			{"id": "nch_2", "type": "webhook", "enabled": false},
		},
	}))

	var runErr error
	out := covCaptureStdoutCli9(t, func() {
		runErr = agentChannelsCmd.RunE(agentChannelsCmd, []string{"ag_1"})
	})
	if runErr != nil {
		t.Fatalf("agent channels: %v", runErr)
	}

	for _, want := range []string{"nch_1", "discord", "nch_2", "webhook"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	// A disabled channel must be visibly disabled: "the agent may post here"
	// and "posting here does anything" are different facts.
	if !strings.Contains(out, "false") {
		t.Errorf("output does not show the disabled channel's state:\n%s", out)
	}
}

func TestAgentChannelsCmd_EmptyIsNotAnError(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/agents/ag_2/notification-channels", clitest.JSONResponse(200, map[string]any{
		"channels": []map[string]any{},
	}))

	var runErr error
	covCaptureStdoutCli9(t, func() {
		runErr = agentChannelsCmd.RunE(agentChannelsCmd, []string{"ag_2"})
	})
	// An agent with no grants is the DEFAULT state, not a failure — this is a
	// default-deny system.
	if runErr != nil {
		t.Fatalf("an agent with no channels should not error: %v", runErr)
	}
}

func TestAgentChannelsCmd_AuthGates(t *testing.T) {
	covSaveState(t)
	cliCfg = nil
	if err := agentChannelsCmd.RunE(agentChannelsCmd, []string{"ag_1"}); err == nil ||
		!strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected a not-logged-in error, got %v", err)
	}
}
