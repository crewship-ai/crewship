package main

import (
	"net/http"
	"strings"
	"testing"
)

func resetPageBoardCommand(t *testing.T) {
	t.Helper()
	old := findSubcommand(pageCmd.Commands(), "board")
	fresh := newPageBoardCommand()
	pageCmd.RemoveCommand(old)
	pageCmd.AddCommand(fresh)
	t.Cleanup(func() { pageCmd.RemoveCommand(fresh); pageCmd.AddCommand(old) })
}

func TestPageBoardCLIExactMutationAndNoImplicitRetry(t *testing.T) {
	for _, tc := range []struct {
		command, method, suffix string
		extra                   []string
	}{
		{"apply", "POST", "/batches", nil}, {"pin", "POST", "/placements", nil}, {"unpin", "DELETE", "/placements/pin", []string{"pin"}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			stub := pageStub(t)
			resetPageBoardCommand(t)
			path := "/api/v1/pages/drawing/board" + tc.suffix
			// Duplicate keys intentionally reach the strict server unchanged: a CLI
			// must not collapse an ambiguous request into a different valid mutation.
			input := `{"base_seq":2,"mutation_id":"retry","mutation_id":"retry","upserts":[],"deletes":[]}`
			handler := func(_ *http.Request, raw []byte) (int, []byte, string) {
				if string(raw) != input {
					t.Errorf("mutation rewritten: %s", raw)
				}
				return 409, []byte(`{"error":"stale board"}`), "application/json"
			}
			if tc.method == "DELETE" {
				stub.OnDelete(path, handler)
			} else {
				stub.OnPost(path, handler)
			}
			args := append([]string{"page", "board", tc.command, "drawing"}, tc.extra...)
			args = append(args, "--file", "-")
			if _, err := runPageCLI(t, input, args...); err == nil {
				t.Fatal("conflict lost")
			}
			if len(stub.CallsFor(tc.method, path)) != 1 {
				t.Fatal("must not retry a conflict automatically")
			}
		})
	}
}

func TestPageBoardCLIRejectsInvalidMutation(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `{}`, `{"mutation_id":"m","base_seq":-1}`, `{"mutation_id":"m","base_seq":1.5}`, strings.Repeat(" ", (2<<20)+1)} {
		cmd := newPageBoardCommand()
		cmd.SetArgs([]string{"apply", "p", "--file", "-"})
		cmd.SetIn(strings.NewReader(body))
		if err := cmd.Execute(); err == nil {
			t.Fatal("invalid mutation accepted")
		}
	}
}

func TestPageBoardCLIReadAndInit(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		args               []string
	}{
		{"show", "GET", "/api/v1/pages/plan/board", []string{"board", "show", "plan"}},
		{"init", "POST", "/api/v1/pages/plan/board", []string{"board", "init", "plan", "--mutation-id", "same-init"}},
		{"chat results", "GET", "/api/v1/chats/chat/artifact-results", []string{"artifact", "chat-results", "chat"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := pageStub(t)
			resetPageBoardCommand(t)
			resetPageArtifactCommand(t)
			path := tc.path
			handler := func(_ *http.Request, raw []byte) (int, []byte, string) {
				if tc.method == "POST" && string(raw) != `{"mutation_id":"same-init"}` {
					t.Errorf("unexpected initialization %s", raw)
				}
				return 200, []byte(`{"seq":0,"results":[]}`), "application/json"
			}
			if tc.method == "POST" {
				stub.OnPost(path, handler)
			} else {
				stub.OnGet(path, handler)
			}
			if _, err := runPageCLI(t, "", append([]string{"page"}, tc.args...)...); err != nil {
				t.Fatal(err)
			}
			if len(stub.CallsFor(tc.method, path)) != 1 {
				t.Fatal("wrong route")
			}
		})
	}
}
