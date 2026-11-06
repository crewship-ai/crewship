package main

import (
	"bytes"
	"crypto/sha256"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

// Use the existing built-binary acceptance seam, including main/Cobra and
// PersistentPreRun. Each child has an explicit owned environment and config.
func TestAcceptanceLoginGoogleUnavailable(t *testing.T) {
	binary := buildCrewshipBinary(t)
	binaryBytes, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("candidate binary SHA256=%x", sha256.Sum256(binaryBytes))
	home := t.TempDir()
	config := filepath.Join(home, "cli-config.yaml")
	initial := []byte("server: https://unused.example.invalid\n")
	if err := os.WriteFile(config, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (int, string, string) {
		t.Helper()
		command := exec.CommandContext(t.Context(), binary, args...)
		command.Dir = home
		command.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "PATH=" + filepath.Join(home, "bin"), "CREWSHIP_CONFIG=" + config, "NO_COLOR=1", "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE="}
		if dir := os.Getenv("TEST_CREWSHIP_CLI_COVERAGE_DIR"); dir != "" {
			command.Env = append(command.Env, "GOCOVERDIR="+dir)
		}
		command.Stdin = strings.NewReader("")
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		result := command.Run()
		code := 0
		if result != nil {
			code = exitCodeOf(t, result)
		}
		return code, stdout.String(), stderr.String()
	}
	code, stdout, stderr := run("login", "--help")
	t.Logf("login --help process exit=%d", code)
	if code != 0 || stderr != "" {
		t.Fatalf("help exit=%d stderr=%q", code, stderr)
	}
	for _, expected := range []string{"email + password", "--token", "--pair", "Google sign-in is unavailable"} {
		if !strings.Contains(stdout, expected) {
			t.Errorf("help missing %q", expected)
		}
	}
	if strings.Contains(stdout, "Sign in via Google OAuth") {
		t.Error("help still advertises Google login")
	}
	code, stdout, stderr = run("login", "--google", "--server", "://invalid")
	t.Logf("login --google --server ://invalid process exit=%d stderr=%q", code, stderr)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "Google sign-in is unavailable") {
		t.Errorf("offline unavailable entry result: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.Contains(stderr, "invalid --server") || strings.Contains(stderr, "contact server") {
		t.Error("unavailable mode reached server handling")
	}
	after, err := os.ReadFile(config)
	if err != nil || !bytes.Equal(initial, after) {
		t.Errorf("owned settings changed: %v", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "cli-config.yaml" {
		t.Error("login created files in owned home")
	}
}
