package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// feedStdin swaps os.Stdin for a pipe pre-loaded with input, restoring
// the original at cleanup. Used to drive confirmAction's non-TTY
// fallback (fmt.Scanln on stdin). Not safe with t.Parallel().
func feedStdin(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = w.Close()
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = orig
		_ = r.Close()
	})
}

func newConfirmTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "confirmtest"}
	c.Flags().Bool("yes", false, "")
	return c
}

func TestConfirmAction_YesFlagSkipsPrompt(t *testing.T) {
	c := newConfirmTestCmd()
	if err := c.Flags().Set("yes", "true"); err != nil {
		t.Fatal(err)
	}
	// No stdin provided — if the prompt ran it would abort on EOF, so a
	// nil return proves --yes short-circuited.
	if err := confirmAction(c, "Delete everything?"); err != nil {
		t.Errorf("got %v; want nil with --yes", err)
	}
}

func TestConfirmAction_NonTTYAnswers(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"y", "y\n", false},
		{"yes", "yes\n", false},
		{"uppercase Y", "Y\n", false},
		{"n", "n\n", true},
		{"empty (EOF)", "", true},
		{"garbage", "whatever\n", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			feedStdin(t, tc.input)
			err := confirmAction(newConfirmTestCmd(), "Proceed?")
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "aborted") {
					t.Errorf("got %v; want aborted", err)
				}
			} else if err != nil {
				t.Errorf("got %v; want nil", err)
			}
		})
	}
}

func TestRootPersistentPreRun_LoadsConfig(t *testing.T) {
	saveCLIState(t)

	path := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(path, []byte("token: tok-from-file\nworkspace: ws-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWSHIP_CONFIG", path)

	cliCfg = nil
	rootCmd.PersistentPreRun(rootCmd, nil)
	if cliCfg == nil {
		t.Fatal("cliCfg not set by PersistentPreRun")
	}
	if cliCfg.Token != "tok-from-file" {
		t.Errorf("Token: got %q", cliCfg.Token)
	}
	if cliCfg.Workspace != "ws-from-file" {
		t.Errorf("Workspace: got %q", cliCfg.Workspace)
	}
}

func TestRootPersistentPreRun_LoadFailureDegradesToEmptyConfig(t *testing.T) {
	saveCLIState(t)

	// Point CREWSHIP_CONFIG at a directory → ReadFile fails with a
	// non-NotExist error → warning branch + empty config fallback.
	t.Setenv("CREWSHIP_CONFIG", t.TempDir())

	cliCfg = nil
	rootCmd.PersistentPreRun(rootCmd, nil)
	if cliCfg == nil {
		t.Fatal("cliCfg must fall back to an empty config, not stay nil")
	}
	if cliCfg.Token != "" {
		t.Errorf("fallback config should be empty; got token %q", cliCfg.Token)
	}
}

func TestNewAPIClient_NilConfigYieldsEmptyToken(t *testing.T) {
	saveCLIState(t)
	cliCfg = nil
	flagServer = "http://example.invalid:1"
	flagWorkspace = ""

	c := newAPIClient()
	if c == nil {
		t.Fatal("newAPIClient returned nil")
	}
	if c.Token != "" {
		t.Errorf("Token: got %q want empty with nil config", c.Token)
	}
	if c.BaseURL != "http://example.invalid:1" {
		t.Errorf("BaseURL: got %q", c.BaseURL)
	}
}

func TestRequireAuthAndWorkspace(t *testing.T) {
	saveCLIState(t)
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagWorkspace = ""

	cliCfg = nil
	if err := requireAuth(); err == nil {
		t.Error("requireAuth with nil config: want error")
	}
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: ""}
	if err := requireAuth(); err != nil {
		t.Errorf("requireAuth with token: got %v", err)
	}
	if err := requireWorkspace(); err == nil {
		t.Error("requireWorkspace without workspace: want error")
	}
	flagWorkspace = "ws1"
	if err := requireWorkspace(); err != nil {
		t.Errorf("requireWorkspace with flag: got %v", err)
	}
}
