package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestShellPromptString(t *testing.T) {
	t.Parallel()
	if got := shellPromptString(""); !strings.Contains(got, "[?]") {
		t.Errorf("empty agent should render '?': %q", got)
	}
	if got := shellPromptString("viktor"); !strings.Contains(got, "[viktor]") {
		t.Errorf("agent slug missing from prompt: %q", got)
	}
	if got := shellPromptString("viktor"); !strings.Contains(got, "crewship") {
		t.Errorf("prompt should brand itself: %q", got)
	}
}

func TestShellRunE_AuthGates(t *testing.T) {
	covSaveState(t)
	cliCfg = &cli.CLIConfig{}
	if err := shellCmd.RunE(shellCmd, nil); err == nil {
		t.Error("expected not-logged-in error")
	}
	cliCfg = &cli.CLIConfig{Token: "tok"}
	if err := shellCmd.RunE(shellCmd, nil); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}

// TestShellRunE_SlashCommandSession drives the REPL through a scripted
// stdin: every slash command except bare-text dispatch (which would hit
// the ask pipeline) is exercised, then /quit ends the loop.
func TestShellRunE_SlashCommandSession(t *testing.T) {
	covSaveState(t)
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: covWSCli9, Server: "http://127.0.0.1:1"} // no requests are made
	t.Cleanup(ResetAIFirstLatches)

	// RunE is invoked directly (not via Execute), so the command has no
	// context yet; repl.Run dereferences it for cancellation.
	shellCmd.SetContext(context.Background())
	t.Cleanup(func() { shellCmd.SetContext(context.Background()) })

	script := strings.Join([]string{
		"/help",
		"bare text before an agent is set", // BareHandler error branch (no active agent)
		"/agent",                           // print current (empty) agent
		"/agent viktor",
		"/workspace",    // usage error branch
		"/workspace w2", // mutates flagWorkspace (restored by covSaveState)
		"/cd w3",        // alias path
		"/plan",
		"/effort",       // print current effort
		"/effort high",  // valid set
		"/effort bogus", // validation error branch
		"/think",
		"/history",
		"/clear",
		// Bare dispatch with agent latched + plan sticky + effort set:
		// the ask pipeline fails fast on the unreachable server, and the
		// REPL must keep running afterwards.
		"what is the deploy status?",
		"/quit",
	}, "\n") + "\n"

	// The bare dispatch writes through askCmd's flags; restore them so
	// later ask-related tests in this package see pristine defaults.
	for _, name := range []string{"agent", "quiet", "prompt"} {
		f := askCmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("ask command lost its --%s flag", name)
		}
		t.Cleanup(func() {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		})
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })
	go func() {
		_, _ = w.WriteString(script)
		_ = w.Close()
	}()

	var runErr error
	out := covCaptureStdoutCli9(t, func() {
		_ = covCaptureStderrCli9(t, func() {
			runErr = shellCmd.RunE(shellCmd, nil)
		})
	})
	if runErr != nil {
		t.Fatalf("shell session should exit cleanly via /quit: %v", runErr)
	}

	for _, want := range []string{
		"crewship shell — type /help for commands",
		"/agent <slug>",                    // /help output
		"active agent:",                    // /agent with no args
		"agent → viktor",                   // /agent viktor
		"workspace → w2",                   // /workspace w2
		"workspace → w3",                   // /cd alias
		"plan-mode: true",                  // /plan toggled on
		"effort → high",                    // /effort high
		"show-thinking: true",              // /think toggled on
		"readline history is a v2 feature", // /history stub
	} {
		if !strings.Contains(out, want) {
			t.Errorf("session output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "[viktor]") {
		t.Errorf("prompt should update to the latched agent:\n%s", out)
	}
}

// TestShellRunE_ExitCommand verifies /exit (the /quit alias) also ends
// the loop cleanly.
func TestShellRunE_ExitCommand(t *testing.T) {
	covSaveState(t)
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: covWSCli9, Server: "http://127.0.0.1:1"}
	t.Cleanup(ResetAIFirstLatches)
	shellCmd.SetContext(context.Background())
	t.Cleanup(func() { shellCmd.SetContext(context.Background()) })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })
	go func() {
		_, _ = w.WriteString("/exit\n")
		_ = w.Close()
	}()

	var runErr error
	_ = covCaptureStdoutCli9(t, func() {
		runErr = shellCmd.RunE(shellCmd, nil)
	})
	if runErr != nil {
		t.Fatalf("/exit should end the session cleanly: %v", runErr)
	}
}
