package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// stdinPipeForCmd points os.Stdin at a fresh pipe. closeAfter=false keeps the
// writer open for the whole test — the harness / CI / cron shape from #2770
// where the CLI used to block forever before creating the run.
func stdinPipeForCmd(t *testing.T, input string, closeAfter bool) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	if input != "" {
		if _, err := w.WriteString(input); err != nil {
			t.Fatalf("write pipe: %v", err)
		}
	}
	if closeAfter {
		_ = w.Close()
	}
	t.Cleanup(func() {
		_ = w.Close()
		os.Stdin = orig
		_ = r.Close()
	})
}

// TestPromptCommands_StdinRule drives `run` and `ask` in --dry-run (no
// server) against a real pipe on os.Stdin for every row of the #2770 rule.
func TestPromptCommands_StdinRule(t *testing.T) {
	cases := []struct {
		name      string
		cmd       *cobra.Command
		args      []string
		stdinFlag bool
		input     string
		closePipe bool
		want      string // exact dry-run stdout
		wantSub   []string
	}{
		{name: "run: positional prompt, open pipe", cmd: runCmd, args: []string{"viktor", "say hi"}, want: "say hi\n"},
		{name: "ask: positional prompt, open pipe", cmd: askCmd, args: []string{"say hi"}, want: "say hi\n"},
		{name: "run: positional prompt ignores pipe data", cmd: runCmd, args: []string{"viktor", "say hi"}, input: "ctx\n", closePipe: true, want: "say hi\n"},
		{name: "run: piped prompt, no positional", cmd: runCmd, args: []string{"viktor"}, input: "hi from pipe\n", closePipe: true, wantSub: []string{"hi from pipe"}},
		{name: "ask: piped prompt, no positional", cmd: askCmd, args: nil, input: "hi from pipe\n", closePipe: true, wantSub: []string{"hi from pipe"}},
		{name: "run: --stdin appends context", cmd: runCmd, args: []string{"viktor", "review"}, stdinFlag: true, input: "diff line\n", closePipe: true, wantSub: []string{"review\n\n", "~~~ stdin\ndiff line\n~~~"}},
		{name: "ask: --stdin appends context", cmd: askCmd, args: []string{"review"}, stdinFlag: true, input: "diff line\n", closePipe: true, wantSub: []string{"review\n\n", "~~~ stdin\ndiff line\n~~~"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saveCLIState(t)
			cliCfg = &cli.CLIConfig{} // dry-run needs no login
			setFlagCov(t, tc.cmd, "dry-run", "true")
			if tc.stdinFlag {
				setFlagCov(t, tc.cmd, "stdin", "true")
			}
			t.Cleanup(ResetAIFirstLatches)
			stdinPipeForCmd(t, tc.input, tc.closePipe)

			type result struct {
				out string
				err error
			}
			done := make(chan result, 1)
			go func() {
				out, err := captureStdoutCov(t, func() error {
					return tc.cmd.RunE(tc.cmd, tc.args)
				})
				done <- result{out, err}
			}()
			var res result
			select {
			case res = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("command blocked on stdin (#2770)")
			}
			if res.err != nil {
				t.Fatalf("RunE: %v", res.err)
			}
			if tc.want != "" && res.out != tc.want {
				t.Errorf("stdout = %q, want %q", res.out, tc.want)
			}
			for _, s := range tc.wantSub {
				if !strings.Contains(res.out, s) {
					t.Errorf("stdout = %q, missing %q", res.out, s)
				}
			}
		})
	}
}

func TestPromptCommands_HaveStdinFlag(t *testing.T) {
	for _, c := range []*cobra.Command{runCmd, askCmd, rootCmd} {
		if c.Flags().Lookup("stdin") == nil {
			t.Errorf("%s missing --stdin flag", c.Name())
		}
	}
}
