package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// swapStdinPipe points os.Stdin at the read end of a fresh pipe and returns
// the write end. When closeAfter is true the input is written and the pipe
// closed (a finished `echo hi |`); otherwise the writer stays open for the
// whole test, which is what a harness, CI shell or cron job with an
// inherited pipe looks like (#2770). Cleanup closes the writer either way so
// a BuildPrompt that did block is released instead of leaking forever.
func swapStdinPipe(t *testing.T, input string, closeAfter bool) {
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

// TestBuildPrompt_StdinRule drives the real os.Stdin (no injected readers)
// through every row of the documented rule: a prompt given on the command
// line never touches stdin unless StdinContext (--stdin) asks for it, and
// with no prompt a pipe is still read to EOF as the prompt.
func TestBuildPrompt_StdinRule(t *testing.T) {
	cases := []struct {
		name       string
		opts       PromptOptions
		input      string
		closePipe  bool
		want       string
		wantSubstr []string
	}{
		{
			name:      "positional prompt with open never-closed pipe returns",
			opts:      PromptOptions{Positional: []string{"say", "hi"}, AutoStdin: true},
			closePipe: false,
			want:      "say hi",
		},
		{
			name:      "--prompt text with open never-closed pipe returns",
			opts:      PromptOptions{PromptFlag: "say hi", AutoStdin: true},
			closePipe: false,
			want:      "say hi",
		},
		{
			name:      "positional prompt ignores pending pipe data without --stdin",
			opts:      PromptOptions{Positional: []string{"say", "hi"}, AutoStdin: true},
			input:     "ignored context\n",
			closePipe: true,
			want:      "say hi",
		},
		{
			name:       "no positional reads piped prompt to EOF",
			opts:       PromptOptions{AutoStdin: true},
			input:      "hi from pipe\n",
			closePipe:  true,
			wantSubstr: []string{"hi from pipe"},
		},
		{
			name:       "--stdin with positional appends the stdin block",
			opts:       PromptOptions{Positional: []string{"review"}, AutoStdin: true, StdinContext: true},
			input:      "diff line\n",
			closePipe:  true,
			wantSubstr: []string{"review\n\n", "~~~ stdin\ndiff line\n~~~"},
		},
		{
			name:      "--prompt @- reads the prompt from stdin",
			opts:      PromptOptions{PromptFlag: "@-", AutoStdin: true},
			input:     "from stdin\n",
			closePipe: true,
			want:      "from stdin",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			swapStdinPipe(t, tc.input, tc.closePipe)

			type result struct {
				got string
				err error
			}
			done := make(chan result, 1)
			go func() {
				got, err := BuildPrompt(context.Background(), tc.opts)
				done <- result{got, err}
			}()

			var res result
			select {
			case res = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("BuildPrompt blocked on stdin (#2770)")
			}
			if res.err != nil {
				t.Fatalf("BuildPrompt: %v", res.err)
			}
			if tc.want != "" && res.got != tc.want {
				t.Errorf("got %q, want %q", res.got, tc.want)
			}
			for _, s := range tc.wantSubstr {
				if !strings.Contains(res.got, s) {
					t.Errorf("got %q, missing %q", res.got, s)
				}
			}
		})
	}
}

// TestBuildPrompt_StdinIgnoredNotice checks the stderr hint that tells a
// user their pipe was not read, so `git diff | crewship ask "review"` does
// not silently lose the diff after #2770.
func TestBuildPrompt_StdinIgnoredNotice(t *testing.T) {
	cases := []struct {
		name       string
		opts       PromptOptions
		dataSource bool
		wantNotice bool
	}{
		{"prompt given, stdin is a pipe", PromptOptions{Positional: []string{"hi"}, AutoStdin: true}, true, true},
		{"prompt given, stdin is /dev/null or a tty", PromptOptions{Positional: []string{"hi"}, AutoStdin: true}, false, false},
		{"prompt given with --stdin", PromptOptions{Positional: []string{"hi"}, AutoStdin: true, StdinContext: true}, true, false},
		{"no prompt, stdin is the prompt", PromptOptions{AutoStdin: true}, true, false},
		{"@- consumed stdin", PromptOptions{PromptFlag: "@-", AutoStdin: true}, true, false},
		{"AutoStdin off", PromptOptions{Positional: []string{"hi"}}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var notice bytes.Buffer
			o := tc.opts
			o.Notice = &notice
			o.readStdin = stubStdin([]byte("data"), nil)
			o.isStdinPipe = func() bool { return true }
			o.isStdinData = func() bool { return tc.dataSource }
			if _, err := BuildPrompt(context.Background(), o); err != nil {
				t.Fatalf("BuildPrompt: %v", err)
			}
			got := strings.Contains(notice.String(), "--stdin")
			if got != tc.wantNotice {
				t.Errorf("notice = %q, want notice=%v", notice.String(), tc.wantNotice)
			}
		})
	}
}
