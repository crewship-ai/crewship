package main

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// A transport double catches attempted requests without opening a socket.
// Asking the server whether this removed mode is enabled is already a side effect.
func TestLoginGoogleUnavailableWithoutSideEffects(t *testing.T) {
	configPath := tempCLIConfig(t)
	swapStdin(t, "not-consumed\n")
	calls := 0
	oldTransport := http.DefaultTransport
	http.DefaultTransport = artifactAgentRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"enabled":false}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	output := captureStdoutCovCli2(t, func() {
		err := loginWithGoogle("https://unused.example.invalid")
		if err == nil || !strings.Contains(err.Error(), "Google sign-in is unavailable") || !strings.Contains(err.Error(), "--token") || !strings.Contains(err.Error(), "--pair") {
			t.Errorf("expected unavailable message with supported alternatives, got %v", err)
		}
	})
	if calls != 0 {
		t.Errorf("unavailable mode attempted %d HTTP requests", calls)
	}
	if output != "" {
		t.Errorf("unavailable mode printed browser/token instructions: %q", output)
	}
	remaining, err := io.ReadAll(os.Stdin)
	if err != nil || string(remaining) != "not-consumed\n" {
		t.Errorf("stdin consumed: %q, %v", remaining, err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Errorf("unavailable mode created or changed config: %v", err)
	}
}

func TestLoginGoogleUnavailableBeforeServerValidation(t *testing.T) {
	saveCLIState(t)
	saveLoginFlags(t)
	tempCLIConfig(t)
	t.Setenv("CREWSHIP_SERVER", "")
	flagServer = "://invalid"
	cliCfg = &cli.CLIConfig{}
	loginGoogleFlag, loginPairFlag, loginTokenFlag = true, false, ""
	err := loginCmd.RunE(loginCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "Google sign-in is unavailable") {
		t.Fatalf("expected local unavailable result before resolving server, got %v", err)
	}
}
