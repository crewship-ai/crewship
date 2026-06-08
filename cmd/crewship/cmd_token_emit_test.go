package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// newEmitCmd builds a bare command carrying the same secret-output
// flags emitToken reads, so the test can drive each mode in isolation
// without standing up the HTTP create/rotate path.
func newEmitCmd() (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	c := &cobra.Command{Use: "x"}
	c.Flags().String("output-file", "", "")
	c.Flags().Bool("quiet", false, "")
	var out, errOut bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errOut)
	return c, &out, &errOut
}

const emitTok = "tok_secret_value_123"

// TestSecTokenEmit_DefaultKeepsSecretOffStdout pins the default-mode
// contract: the bearer must NOT appear on stdout (which a CI step log
// or a `> file` redirect would persist), and a sensitive-token warning
// must be emitted to stderr.
func TestSecTokenEmit_DefaultKeepsSecretOffStdout(t *testing.T) {
	cmd, out, errOut := newEmitCmd()
	if err := emitToken(cmd, "ci-runner", "tok_id_1", emitTok); err != nil {
		t.Fatalf("emitToken: %v", err)
	}
	if strings.Contains(out.String(), emitTok) {
		t.Errorf("token leaked to stdout in default mode: %q", out.String())
	}
	if !strings.Contains(errOut.String(), emitTok) {
		t.Errorf("token should be on stderr in default mode: %q", errOut.String())
	}
	if !strings.Contains(strings.ToLower(errOut.String()), "store this token securely") {
		t.Errorf("missing sensitive-token warning on stderr: %q", errOut.String())
	}
	// Metadata stays on stdout for human readability.
	if !strings.Contains(out.String(), "ci-runner") || !strings.Contains(out.String(), "tok_id_1") {
		t.Errorf("metadata should be on stdout: %q", out.String())
	}
}

// TestSecTokenEmit_QuietBareTokenOnly checks the capture mode: stdout
// is exactly the bare token (so $(...) gets a clean value), and the
// advisory rides stderr where command substitution won't capture it.
func TestSecTokenEmit_QuietBareTokenOnly(t *testing.T) {
	cmd, out, errOut := newEmitCmd()
	if err := cmd.Flags().Set("quiet", "true"); err != nil {
		t.Fatal(err)
	}
	if err := emitToken(cmd, "ci-runner", "tok_id_1", emitTok); err != nil {
		t.Fatalf("emitToken: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != emitTok {
		t.Errorf("quiet stdout = %q, want bare token %q", got, emitTok)
	}
	if !strings.Contains(strings.ToLower(errOut.String()), "sensitive") {
		t.Errorf("quiet mode should warn on stderr: %q", errOut.String())
	}
}

// TestSecTokenEmit_OutputFile0600 verifies the file sink: the token
// lands in a 0600 file, stdout never sees it.
func TestSecTokenEmit_OutputFile0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token.txt")

	cmd, out, _ := newEmitCmd()
	if err := cmd.Flags().Set("output-file", path); err != nil {
		t.Fatal(err)
	}
	if err := emitToken(cmd, "ci-runner", "tok_id_1", emitTok); err != nil {
		t.Fatalf("emitToken: %v", err)
	}
	if strings.Contains(out.String(), emitTok) {
		t.Errorf("token leaked to stdout in file mode: %q", out.String())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	if strings.TrimSpace(string(data)) != emitTok {
		t.Errorf("file content = %q, want %q", string(data), emitTok)
	}

	// 0600 perms — skip the bit check on Windows where Unix mode bits
	// don't map cleanly.
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("token file perms = %o, want 0600", perm)
		}
	}
}

// TestSecTokenEmitFlagsWired guards the create/rotate flag surface so a
// refactor that drops the secret-safe sinks fails loudly.
func TestSecTokenEmitFlagsWired(t *testing.T) {
	t.Parallel()
	for _, c := range []*cobra.Command{tokenCreateCmd, tokenRotateCmd} {
		if c.Flags().Lookup("output-file") == nil {
			t.Errorf("%s missing --output-file flag", c.Name())
		}
		if c.Flags().Lookup("quiet") == nil {
			t.Errorf("%s missing --quiet flag", c.Name())
		}
	}
}
