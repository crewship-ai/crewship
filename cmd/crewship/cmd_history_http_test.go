package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// httptest-backed tests for the helpers that talk to /api/v1/chats and
// /api/v1/runs. The aim is to validate the URL/JSON contract our CLI
// expects from the server, not to recreate the server.

func TestFetchFirstUserPrompt_PicksUserMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/chats/") || !strings.HasSuffix(r.URL.Path, "/messages") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[
			{"role":"system","content":"sys"},
			{"role":"assistant","content":"prior"},
			{"role":"user","content":"the real user prompt"},
			{"role":"user","content":"second user msg"}
		]}`))
	}))
	defer srv.Close()

	c := cli.NewClient(srv.URL, "tok", "")
	got := fetchFirstUserPrompt(c, "chat_xyz")
	if got != "the real user prompt" {
		t.Errorf("got %q, want first USER message", got)
	}
}

func TestFetchFirstUserPrompt_HumanRoleAlias(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"messages":[{"role":"Human","content":"hi"}]}`))
	}))
	defer srv.Close()

	got := fetchFirstUserPrompt(cli.NewClient(srv.URL, "t", ""), "c1")
	if got != "hi" {
		t.Errorf("HUMAN role should be treated as user: got %q", got)
	}
}

func TestFetchFirstUserPrompt_NoUserMessages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"messages":[{"role":"assistant","content":"only assistant"}]}`))
	}))
	defer srv.Close()

	got := fetchFirstUserPrompt(cli.NewClient(srv.URL, "t", ""), "c1")
	if got != "" {
		t.Errorf("no user msgs should yield empty, got %q", got)
	}
}

func TestFetchFirstUserPrompt_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	got := fetchFirstUserPrompt(cli.NewClient(srv.URL, "t", ""), "c1")
	if got != "" {
		t.Errorf("server error should yield empty (silent), got %q", got)
	}
}

func TestFetchRun_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runs" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[
			{"id":"r_other","agent_id":"a_other","agent_slug":"piotr","chat_id":"c_other"},
			{"id":"r_target","agent_id":"a_v","agent_slug":"viktor","chat_id":"c_v"}
		]}`))
	}))
	defer srv.Close()

	got, err := fetchRun(cli.NewClient(srv.URL, "t", ""), "r_target")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.AgentID != "a_v" || got.AgentSlug != "viktor" || got.ChatID != "c_v" {
		t.Errorf("metadata mismatch: %+v", got)
	}
}

func TestFetchRun_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"r_other","agent_id":"x"}]}`))
	}))
	defer srv.Close()

	_, err := fetchRun(cli.NewClient(srv.URL, "t", ""), "r_target")
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention not-found: %v", err)
	}
}

func TestFetchPromptsParallel_FillsAllInOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Path: /api/v1/chats/{chatId}/messages
		parts := strings.Split(r.URL.Path, "/")
		// parts: ["", "api", "v1", "chats", "<id>", "messages"]
		if len(parts) < 6 {
			t.Errorf("bad path: %s", r.URL.Path)
		}
		chatID := parts[4]
		_, _ = w.Write([]byte(`{"messages":[{"role":"user","content":"prompt-for-` + chatID + `"}]}`))
	}))
	defer srv.Close()

	c := cli.NewClient(srv.URL, "t", "")
	refs := []runChatRef{
		{RunID: "r1", ChatID: "c1"},
		{RunID: "r2", ChatID: "c2"},
		{RunID: "r3", ChatID: "c3"},
		{RunID: "r4", ChatID: "c4"},
		{RunID: "r5", ChatID: "c5"},
	}
	got := fetchPromptsParallel(c, refs, 3)
	if len(got) != 5 {
		t.Errorf("expected 5 results, got %d: %v", len(got), got)
	}
	for _, r := range refs {
		want := "prompt-for-" + r.ChatID
		if got[r.RunID] != want {
			t.Errorf("run %s: got %q, want %q", r.RunID, got[r.RunID], want)
		}
	}
}

func TestFetchPromptsParallel_EmptyInput(t *testing.T) {
	got := fetchPromptsParallel(nil, nil, 4)
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

func TestFetchPromptsParallel_ZeroWorkersClampsToOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"messages":[{"role":"user","content":"x"}]}`))
	}))
	defer srv.Close()

	got := fetchPromptsParallel(cli.NewClient(srv.URL, "t", ""),
		[]runChatRef{{RunID: "r", ChatID: "c"}}, 0)
	if got["r"] != "x" {
		t.Errorf("zero workers should be clamped to 1 and still produce results: %v", got)
	}
}

func TestFetchRun_HandlesNullChatId(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"r1","agent_id":"a1","agent_slug":"v","chat_id":null}]}`))
	}))
	defer srv.Close()

	got, err := fetchRun(cli.NewClient(srv.URL, "t", ""), "r1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.ChatID != "" {
		t.Errorf("expected empty ChatID for null, got %q", got.ChatID)
	}
}
