package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func runCraftAgentCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newCraftCommand()
	cmd.PersistentPreRun = rootCmd.PersistentPreRun
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}
func TestCraftAgentCLIStartRequiresExplicitWholeDocumentConsent(t *testing.T) {
	stub := pageStub(t)
	for _, args := range [][]string{
		{"task", "start", "health", "--chat-id", "chat", "--message-id", "message"},
		{"task", "start", "health", "--chat-id", "chat", "--message-id", "message", "--whole-document=false"},
		{"task", "start", "health", "--message-id", "message", "--whole-document"},
		{"task", "start", "health", "--chat-id", "chat", "--whole-document"},
		{"task", "start", "health", "--chat-id", "", "--message-id", "message", "--whole-document"},
		{"task", "start", "health", "--chat-id", "chat\x00bad", "--message-id", "message", "--whole-document"},
		{"task", "start", "health", "--chat-id", strings.Repeat("界", 201), "--message-id", "message", "--whole-document"},
		{"task", "start", "..", "--chat-id", "chat", "--message-id", "message", "--whole-document"},
		{"task", "status", "health", ""}, {"task", "status", "health", "bad\x00work"}, {"task", "stop", ""},
	} {
		if _, err := runCraftAgentCLI(t, args...); err == nil {
			t.Fatalf("unqualified command accepted: %q", args)
		}
	}
	if calls := stub.Calls(); len(calls) != 0 {
		t.Fatalf("invalid input made HTTP calls: %d", len(calls))
	}
}
func TestCraftAgentCLIStartUsesOnlyHumanAuthAndExactConsentBody(t *testing.T) {
	stub := pageStub(t)
	path := "/api/v1/pages/health/craft/tasks"
	stub.OnPost(path, func(r *http.Request, body []byte) (int, []byte, string) {
		if r.Header.Get("Authorization") != "Bearer page-acceptance-token" || r.Header.Get("X-Craft-Session") != "" || r.Header.Get("X-Internal-Token") != "" {
			t.Error("task did not use only normal H1 auth")
		}
		var v map[string]any
		if json.Unmarshal(body, &v) != nil || len(v) != 3 || v["chat_id"] != "chat" || v["message_id"] != "message" || v["whole_document"] != true {
			t.Errorf("wrong explicit H1 consent body: %s", body)
		}
		return 202, []byte(`{"work_id":"accepted-work","status":"queued"}`), "application/json"
	})
	out, err := runCraftAgentCLI(t, "task", "start", "health", "--chat-id", "chat", "--message-id", "message", "--whole-document")
	if err != nil || !strings.Contains(out, "accepted-work") || !strings.Contains(out, "queued") {
		t.Fatal("normal CLI start failed", out, err)
	}
	if len(stub.CallsFor("POST", path)) != 1 || len(stub.Calls()) != 1 {
		t.Fatal("start retried, admitted human editor or used wrong API")
	}
}
func TestCraftAgentCLIStatusAndStopUseScopedEscapedPathsWithoutSession(t *testing.T) {
	stub := pageStub(t)
	page := "@page/one ?#"
	work := "work/two?#"
	status := "/api/v1/pages/" + page + "/craft/tasks/" + work
	stop := "/api/v1/pages/" + page + "/craft/tasks/cancel"
	check := func(r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer page-acceptance-token" || r.Header.Get("X-Craft-Session") != "" || r.Header.Get("X-Internal-Token") != "" {
			t.Error("non-H1 identity sent")
		}
		if !strings.Contains(r.URL.EscapedPath(), "%2F") || !strings.Contains(r.URL.EscapedPath(), "%3F") || !strings.Contains(r.URL.EscapedPath(), "%23") {
			t.Error("opaque identifiers were not escaped")
		}
	}
	stub.OnGet(status, func(r *http.Request, _ []byte) (int, []byte, string) {
		check(r)
		return 200, []byte(`{"work_id":"work/two?#","state":"running","operation_inflight":true}`), "application/json"
	})
	stub.OnPost(stop, func(r *http.Request, body []byte) (int, []byte, string) {
		check(r)
		if string(bytes.TrimSpace(body)) != "{}" {
			t.Errorf("stop sent an identity or extra consent: %s", body)
		}
		return 202, []byte(`{"grant_revoked":true,"stop_pending":true}`), "application/json"
	})
	if out, err := runCraftAgentCLI(t, "task", "status", page, work); err != nil || !strings.Contains(out, "running") {
		t.Fatal(out, err)
	}
	if out, err := runCraftAgentCLI(t, "task", "stop", page); err != nil || !strings.Contains(out, "stop_pending") {
		t.Fatal(out, err)
	}
	if len(stub.Calls()) != 2 {
		t.Fatal("status/stop retried or admitted a human edit session")
	}
}
func TestCraftAgentCLINeverRetriesOrReflectsErrorBodies(t *testing.T) {
	stub := pageStub(t)
	path := "/api/v1/pages/health/craft/tasks"
	stub.OnPost(path, func(*http.Request, []byte) (int, []byte, string) {
		return 503, []byte(`{"error":"private-runtime-token and private document"}`), "application/json"
	})
	out, err := runCraftAgentCLI(t, "task", "start", "health", "--chat-id", "chat", "--message-id", "message", "--whole-document=true")
	if err == nil {
		t.Fatal("unconfirmed task reported success")
	}
	if strings.Contains(out+err.Error(), "private-runtime-token") {
		t.Fatal("private error body was reflected")
	}
	if len(stub.CallsFor("POST", path)) != 1 {
		t.Fatal("task replayed after unknown admission outcome")
	}
}

func TestCraftAgentCLIRefusesHTTPRedirectReplay(t *testing.T) {
	pageStub(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/pages/health/craft/tasks" {
			t.Error("task consent was replayed to redirect target")
		}
		w.Header().Set("Location", "/must-not-follow")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	t.Setenv("CREWSHIP_SERVER", server.URL)
	t.Setenv("CREWSHIP_TOKEN", "redirect-human-token")
	if _, err := runCraftAgentCLI(t, "task", "start", "health", "--chat-id", "chat", "--message-id", "message", "--whole-document"); err == nil {
		t.Fatal("redirected task reported accepted")
	}
	if calls.Load() != 1 {
		t.Fatal("task request was redirected/replayed", calls.Load())
	}
}

func TestCraftAgentCLIConsentFlagDefaultsFalse(t *testing.T) {
	cmd, _, err := newCraftCommand().Find([]string{"task", "start"})
	if err != nil {
		t.Fatal("native H1 task command missing", err)
	}
	flag := cmd.Flags().Lookup("whole-document")
	if flag == nil || flag.DefValue != "false" || flag.Changed {
		t.Fatal("whole-document consent did not default to false")
	}
}

// A proposal is a successful task outcome; status must print it with its id.
func TestCraftAgentCLIStatusPrintsProposalOutcome(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet("/api/v1/pages/health/craft/tasks/work", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"work_id":"work","state":"succeeded","outcome":"proposal","proposal_id":"ncp_05ce1f638d32a97955d06534","events":[{"operation":"propose","status":201,"at":"2026-10-29T22:03:51Z"}]}`), "application/json"
	})
	out, err := runCraftAgentCLI(t, "task", "status", "health", "work")
	if err != nil || !strings.Contains(out, "succeeded") || !strings.Contains(out, `"outcome"`) || !strings.Contains(out, "proposal") || !strings.Contains(out, "ncp_05ce1f638d32a97955d06534") {
		t.Fatal("status did not print the proposal outcome", out, err)
	}
}
