package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// ndjsonStreamServer answers /api/v1/chats/{id}/stream with the given lines
// and records the query string of every request, so a test can assert the
// resume watermark the CLI sent on reconnect.
func ndjsonStreamServer(t *testing.T, bodies ...string) (*httptest.Server, *[]string) {
	t.Helper()
	var queries []string
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/stream") {
			http.NotFound(w, r)
			return
		}
		queries = append(queries, r.URL.RawQuery)
		i := int(atomic.AddInt32(&n, 1)) - 1
		if i >= len(bodies) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, bodies[i])
	}))
	t.Cleanup(srv.Close)
	return srv, &queries
}

// The default (non-follow) run: text lands on stdout, the stream ends on
// run_complete, and the command exits 0.
func TestStreamChatRun_PrintsTextAndExitsOnRunComplete(t *testing.T) {
	srv, _ := ndjsonStreamServer(t, strings.Join([]string{
		`{"type":"stream.open","chat_id":"c1","from_seq":0,"active":true}`,
		`{"type":"run_begin","seq":1,"from_seq":0}`,
		`{"type":"thinking","seq":2,"content":"pondering"}`,
		`{"type":"text","seq":3,"content":"hello world"}`,
		`{"type":"done","seq":4}`,
		`{"type":"stream.end","reason":"run_complete","last_seq":4}`,
	}, "\n")+"\n")

	client := cli.NewClient(srv.URL, "fake-token", covWorkspaceID)
	out, err := captureStdoutCov(t, func() error {
		return streamChatRun(client, "c1", chatStreamOptions{})
	})
	if err != nil {
		t.Fatalf("streamChatRun: %v", err)
	}
	if !strings.Contains(out, "hello world") {
		t.Errorf("stdout = %q, want the agent text", out)
	}
	// Thinking is operator chatter, not run output — it must not pollute a
	// pipeline reading stdout.
	if strings.Contains(out, "pondering") {
		t.Errorf("stdout = %q, thinking must not land on stdout", out)
	}
}

// An `error` frame must exit non-zero: a script wrapping `crewship chat
// stream` has no other way to notice the run failed.
func TestStreamChatRun_ErrorFrameExitsNonZero(t *testing.T) {
	srv, _ := ndjsonStreamServer(t, strings.Join([]string{
		`{"type":"stream.open","chat_id":"c1"}`,
		`{"type":"error","seq":1,"content":"tool blew up"}`,
		`{"type":"done","seq":2}`,
		`{"type":"stream.end","reason":"run_complete","last_seq":2}`,
	}, "\n")+"\n")

	client := cli.NewClient(srv.URL, "fake-token", covWorkspaceID)
	_, err := captureStdoutCov(t, func() error {
		return streamChatRun(client, "c1", chatStreamOptions{})
	})
	if err == nil || !strings.Contains(err.Error(), "tool blew up") {
		t.Fatalf("err = %v, want the agent error surfaced as a non-zero exit", err)
	}
}

// A connection that drops mid-run must reconnect with the last seq it saw, so
// the server's replay buffer fills the gap instead of the caller losing it.
func TestStreamChatRun_ResumesFromLastSeqAfterDrop(t *testing.T) {
	srv, queries := ndjsonStreamServer(t,
		// First connection: two frames, then the server hangs up with no
		// stream.end — the drop case.
		`{"type":"stream.open","chat_id":"c1","active":true}`+"\n"+`{"type":"text","seq":9,"content":"first"}`+"\n",
		// Second connection: completes.
		`{"type":"stream.open","chat_id":"c1","active":true}`+"\n"+`{"type":"text","seq":10,"content":"second"}`+"\n"+`{"type":"done","seq":11}`+"\n"+`{"type":"stream.end","reason":"run_complete","last_seq":11}`+"\n",
	)

	client := cli.NewClient(srv.URL, "fake-token", covWorkspaceID)
	out, err := captureStdoutCov(t, func() error {
		return streamChatRun(client, "c1", chatStreamOptions{quiet: true})
	})
	if err != nil {
		t.Fatalf("streamChatRun: %v", err)
	}
	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Errorf("stdout = %q, want both sides of the reconnect", out)
	}
	if len(*queries) < 2 {
		t.Fatalf("connections = %d, want a reconnect after the drop", len(*queries))
	}
	if !strings.Contains((*queries)[1], "last_seq=9") {
		t.Errorf("reconnect query = %q, want last_seq=9", (*queries)[1])
	}
}

// A 404 is permanent: reconnecting cannot fix "no such chat", and a hot retry
// loop against it is pure noise.
func TestStreamChatRun_PermanentErrorDoesNotRetry(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := cli.NewClient(srv.URL, "fake-token", covWorkspaceID)
	_, err := captureStdoutCov(t, func() error {
		return streamChatRun(client, "c1", chatStreamOptions{quiet: true})
	})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want the 404 returned without retrying", err)
	}
	if got := cli.ExitCodeFor(err); got != cli.ExitNotFound {
		t.Errorf("exit code = %d, want ExitNotFound (%d) — the CLI exit-code contract must hold for a stream too", got, cli.ExitNotFound)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("connections = %d, want exactly 1 — a 404 must not be retried", got)
	}
}

// `no_active_run` is a clean, expected end: nothing was running. It must exit
// 0 so `crewship chat stream <id> || alert` doesn't fire on an idle session.
func TestStreamChatRun_NoActiveRunExitsCleanly(t *testing.T) {
	srv, _ := ndjsonStreamServer(t, strings.Join([]string{
		`{"type":"stream.open","chat_id":"c1","active":false}`,
		`{"type":"stream.end","reason":"no_active_run","last_seq":0}`,
	}, "\n")+"\n")

	client := cli.NewClient(srv.URL, "fake-token", covWorkspaceID)
	if _, err := captureStdoutCov(t, func() error {
		return streamChatRun(client, "c1", chatStreamOptions{quiet: true})
	}); err != nil {
		t.Fatalf("streamChatRun: %v, want a clean exit for an idle session", err)
	}
}

// A truncated replay buffer cannot be stitched back together by reconnecting;
// the caller has to read history instead. Say so and exit non-zero rather than
// loop.
func TestStreamChatRun_ReplayTruncatedIsFatalNotRetried(t *testing.T) {
	srv, queries := ndjsonStreamServer(t, strings.Join([]string{
		`{"type":"stream.open","chat_id":"c1","active":true}`,
		`{"type":"stream.reset","reason":"replay_truncated"}`,
		`{"type":"stream.end","reason":"replay_truncated","last_seq":0}`,
	}, "\n")+"\n")

	client := cli.NewClient(srv.URL, "fake-token", covWorkspaceID)
	_, err := captureStdoutCov(t, func() error {
		return streamChatRun(client, "c1", chatStreamOptions{quiet: true})
	})
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("err = %v, want a fatal truncation error", err)
	}
	if len(*queries) != 1 {
		t.Errorf("connections = %d, want 1 — reconnecting cannot heal a truncated buffer", len(*queries))
	}
}

// --format ndjson is the agent-facing mode: every server line, verbatim, on
// stdout. That is the whole point of the endpoint.
func TestStreamChatRun_NDJSONFormatPassesLinesThrough(t *testing.T) {
	lines := []string{
		`{"type":"stream.open","chat_id":"c1","active":true}`,
		`{"type":"text","seq":1,"content":"hi"}`,
		`{"type":"done","seq":2}`,
		`{"type":"stream.end","reason":"run_complete","last_seq":2}`,
	}
	srv, _ := ndjsonStreamServer(t, strings.Join(lines, "\n")+"\n")

	setFormatCov(t, "ndjson")
	client := cli.NewClient(srv.URL, "fake-token", covWorkspaceID)
	out, err := captureStdoutCov(t, func() error {
		return streamChatRun(client, "c1", chatStreamOptions{})
	})
	if err != nil {
		t.Fatalf("streamChatRun: %v", err)
	}
	for _, want := range lines {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing verbatim line %s\ngot:\n%s", want, out)
		}
	}
}

// The command must build the request the endpoint documents: last_seq, follow
// and idle all land in the query string.
func TestStreamChatRun_SendsAllQueryParameters(t *testing.T) {
	srv, queries := ndjsonStreamServer(t,
		`{"type":"stream.open","chat_id":"c1"}`+"\n"+`{"type":"stream.end","reason":"no_active_run"}`+"\n")

	client := cli.NewClient(srv.URL, "fake-token", covWorkspaceID)
	if _, err := captureStdoutCov(t, func() error {
		return streamChatRun(client, "c1", chatStreamOptions{lastSeq: 12, follow: true, idleSeconds: 45, quiet: true})
	}); err != nil {
		t.Fatalf("streamChatRun: %v", err)
	}
	q := (*queries)[0]
	for _, want := range []string{"last_seq=12", "follow=true", "idle=45"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q missing %s", q, want)
		}
	}
}

// Finding 8 (#1822 review). The reconnect loop reset endReason each iteration
// but never runError, so under --follow an error from run 1 survived into
// run 2: a later clean end (idle_timeout) hit the same arm, saw the stale
// error and exited non-zero citing a run that finished long before. The
// documented contract `crewship chat stream <id> || notify-failure` fired a
// false alarm.
func TestStreamChatRun_ErrorDoesNotSurviveIntoTheNextRun(t *testing.T) {
	srv, _ := ndjsonStreamServer(t,
		// Connection 1: run 1 fails, then the server drops without stream.end.
		`{"type":"stream.open","chat_id":"c1","active":true}`+"\n"+
			`{"type":"error","seq":1,"content":"run one exploded"}`+"\n"+
			`{"type":"done","seq":2}`+"\n",
		// Connection 2: a NEW run, clean, then an ordinary idle end.
		`{"type":"stream.open","chat_id":"c1","active":true}`+"\n"+
			`{"type":"run_begin","seq":3,"from_seq":2}`+"\n"+
			`{"type":"text","seq":4,"content":"run two is fine"}`+"\n"+
			`{"type":"done","seq":5}`+"\n"+
			`{"type":"stream.end","reason":"idle_timeout","last_seq":5}`+"\n",
	)

	client := cli.NewClient(srv.URL, "fake-token", covWorkspaceID)
	_, err := captureStdoutCov(t, func() error {
		return streamChatRun(client, "c1", chatStreamOptions{follow: true, quiet: true})
	})
	if err != nil {
		t.Fatalf("err = %v; the failure belonged to a previous run that a later clean run superseded", err)
	}
}

// The same guard must not go too far: an error in the run that is ACTUALLY
// ending still has to reach the exit status.
func TestStreamChatRun_ErrorInTheEndingRunStillFails(t *testing.T) {
	srv, _ := ndjsonStreamServer(t, strings.Join([]string{
		`{"type":"stream.open","chat_id":"c1","active":true}`,
		`{"type":"run_begin","seq":1,"from_seq":0}`,
		`{"type":"error","seq":2,"content":"this one really failed"}`,
		`{"type":"done","seq":3}`,
		`{"type":"stream.end","reason":"run_complete","last_seq":3}`,
	}, "\n")+"\n")

	client := cli.NewClient(srv.URL, "fake-token", covWorkspaceID)
	_, err := captureStdoutCov(t, func() error {
		return streamChatRun(client, "c1", chatStreamOptions{quiet: true})
	})
	if err == nil || !strings.Contains(err.Error(), "this one really failed") {
		t.Fatalf("err = %v, want the current run's error surfaced", err)
	}
}

// Finding 1, CLI half. `access_revoked` is a permanent verdict: the caller was
// removed from the workspace. Retrying it in a reconnect loop hammers a chat
// they may no longer read and never succeeds.
func TestStreamChatRun_AccessRevokedIsFatalNotRetried(t *testing.T) {
	srv, queries := ndjsonStreamServer(t, strings.Join([]string{
		`{"type":"stream.open","chat_id":"c1","active":true}`,
		`{"type":"stream.end","reason":"access_revoked","last_seq":3}`,
	}, "\n")+"\n")

	client := cli.NewClient(srv.URL, "fake-token", covWorkspaceID)
	_, err := captureStdoutCov(t, func() error {
		return streamChatRun(client, "c1", chatStreamOptions{quiet: true})
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "access") {
		t.Fatalf("err = %v, want a fatal access error", err)
	}
	if len(*queries) != 1 {
		t.Errorf("connections = %d, want 1 — revocation must not be retried", len(*queries))
	}
}

// Finding 10 (#1822 review). The route is registered without wsCtx on purpose
// and never reads X-Workspace-ID — tenancy comes from the chat row. But the
// command gated on requireAuthAndWorkspace, so an agent holding only
// CREWSHIP_TOKEN and a chat id (exactly the caller this feature exists for)
// was refused locally for a request the server would have served.
func TestChatStreamCmd_WorksWithoutAWorkspace(t *testing.T) {
	srv, _ := ndjsonStreamServer(t,
		`{"type":"stream.open","chat_id":"c1","active":false}`+"\n"+
			`{"type":"stream.end","reason":"no_active_run","last_seq":0}`+"\n")

	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagServer = ""
	flagWorkspace = ""
	// A token, no workspace — the agent-in-a-container shape.
	cliCfg = &cli.CLIConfig{Token: "fake-token", Server: srv.URL}
	setFlagCov(t, chatStreamCmd, "quiet", "true")

	if _, err := captureStdoutCov(t, func() error {
		return chatStreamCmd.RunE(chatStreamCmd, []string{"c1"})
	}); err != nil {
		t.Fatalf("err = %v; the stream route takes no workspace, so the CLI must not demand one", err)
	}
}

func TestChatStreamCmd_RequiresAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	if err := chatStreamCmd.RunE(chatStreamCmd, []string{"c1"}); err == nil {
		t.Fatal("want an auth error with no token configured")
	}
}
