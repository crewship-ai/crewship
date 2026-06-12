package main

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
)

const helperEnv = "GEN_WS_TOKEN_HELPER"

// TestHelperRunMain is not a real test: when re-exec'd with helperEnv
// set, it runs main() so the parent test can observe exit codes and
// stderr without killing its own process (main calls os.Exit on every
// error path).
func TestHelperRunMain(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("helper process only")
	}
	main()
	// Success path: main returns normally and the test binary exits 0.
}

// runTool re-execs the current test binary as a gen-ws-token process
// with the given env, returning stderr and the exit code.
func runTool(t *testing.T, env map[string]string) (stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperRunMain")
	cmd.Env = append(os.Environ(),
		helperEnv+"=1",
		// Reset all tool inputs; the map below re-adds what each case needs.
		"NEXTAUTH_SECRET=", "USER_ID=", "SESSION_ID=",
	)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err == nil {
		return errBuf.String(), 0
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("run helper: %v", err)
	}
	return errBuf.String(), exitErr.ExitCode()
}

func TestMain_MissingEnvVarsExit2(t *testing.T) {
	cases := []struct {
		name       string
		env        map[string]string
		wantStderr string
	}{
		{
			name:       "missing secret",
			env:        map[string]string{"USER_ID": "u1", "SESSION_ID": "s1"},
			wantStderr: "NEXTAUTH_SECRET required",
		},
		{
			name:       "missing user id",
			env:        map[string]string{"NEXTAUTH_SECRET": "test-secret", "SESSION_ID": "s1"},
			wantStderr: "USER_ID required",
		},
		{
			name:       "missing session id",
			env:        map[string]string{"NEXTAUTH_SECRET": "test-secret", "USER_ID": "u1"},
			wantStderr: "SESSION_ID required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stderr, code := runTool(t, tc.env)
			if code != 2 {
				t.Errorf("exit code = %d, want 2 (stderr: %s)", code, stderr)
			}
			if !strings.Contains(stderr, tc.wantStderr) {
				t.Errorf("stderr = %q, want substring %q", stderr, tc.wantStderr)
			}
		})
	}
}

// TestMain_SuccessIssuesValidWSTicket exercises the happy path
// in-process (no os.Exit is reached when all env vars are set), so
// the run counts toward coverage. Stdout is captured via a pipe and
// the printed token is validated with the same secret — proving the
// tool emits a real WS ticket, not just any string.
func TestMain_SuccessIssuesValidWSTicket(t *testing.T) {
	const secret = "unit-test-secret-not-a-real-deployment-value"
	t.Setenv("NEXTAUTH_SECRET", secret)
	t.Setenv("USER_ID", "user-123")
	t.Setenv("SESSION_ID", "s_abc")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	main()

	w.Close()
	os.Stdout = origStdout
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		t.Fatal("main printed no token")
	}

	v, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	claims, err := v.ValidateWS(token)
	if err != nil {
		t.Fatalf("printed token failed WS validation: %v", err)
	}
	if claims.ID != "user-123" {
		t.Errorf("claims.ID = %q, want %q", claims.ID, "user-123")
	}
	if claims.Sid != "s_abc" {
		t.Errorf("claims.Sid = %q, want %q", claims.Sid, "s_abc")
	}
}
