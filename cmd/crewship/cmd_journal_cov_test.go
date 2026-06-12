package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// covWorkspaceID is CUID-shaped (c + 21 lowercase alnum) so neither the
// client nor cmd-level helpers attempt slug resolution round-trips.
const covWorkspaceID = "cwsaaaaaaaaaaaaaaaaaaaa"

// setupStubCLICov points the package-global CLI config at a clitest stub
// server with an authenticated, CUID-workspace session. Restores all
// mutated globals via saveCLIState + t.Setenv cleanups.
func setupStubCLICov(t *testing.T, stub *clitest.StubServer) {
	t.Helper()
	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagServer = ""
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceID, Server: stub.URL()}
}

// setFormatCov sets the global --format value and restores it at cleanup.
func setFormatCov(t *testing.T, format string) {
	t.Helper()
	orig := flagFormat
	flagFormat = format
	t.Cleanup(func() { flagFormat = orig })
}

// setFlagCov sets a cobra flag and restores value + Changed at cleanup —
// command flag state is package-global and leaks across tests otherwise.
func setFlagCov(t *testing.T, cmd *cobra.Command, name, value string) {
	t.Helper()
	f := cmd.Flags().Lookup(name)
	if f == nil {
		t.Fatalf("command %q has no --%s flag", cmd.Name(), name)
	}
	orig := f.Value.String()
	origChanged := f.Changed
	if err := cmd.Flags().Set(name, value); err != nil {
		t.Fatalf("set --%s=%s: %v", name, value, err)
	}
	t.Cleanup(func() {
		_ = f.Value.Set(orig)
		f.Changed = origChanged
	})
}

// captureStdoutCov runs fn with os.Stdout redirected into a pipe and
// returns everything written plus fn's error.
func captureStdoutCov(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	outCh := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		outCh <- string(b)
	}()
	runErr := fn()
	_ = w.Close()
	os.Stdout = orig
	return <-outCh, runErr
}

func TestJournalRunE_AuthAndWorkspaceErrors(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	if err := journalCmd.RunE(journalCmd, nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("no-auth: want 'not logged in', got %v", err)
	}

	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")
	if err := journalCmd.RunE(journalCmd, nil); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("no-workspace: want workspace error, got %v", err)
	}
}

func TestJournalRunE_ValidationErrors(t *testing.T) {
	cases := []struct {
		name    string
		flag    string
		value   string
		wantErr string
	}{
		{"bad severity", "severity", "info,bogus", `invalid --severity value "bogus"`},
		{"bad actor", "actor-type", "alien", `invalid --actor-type value "alien"`},
		{"bad priority", "priority", "urgent", `invalid --priority value "urgent"`},
		{"lines too low", "lines", "0", "--lines must be between 1 and 500"},
		{"lines too high", "lines", "501", "--lines must be between 1 and 500"},
		{"bad since", "since", "not-a-time", "bad --since"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saveCLIState(t)
			cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceID}
			setFlagCov(t, journalCmd, tc.flag, tc.value)

			err := journalCmd.RunE(journalCmd, nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestJournalRunE_ListQueryAndOutput(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)

	crewID := "ccrewaaaaaaaaaaaaaaaaaa"
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": crewID, "slug": "backend-team"},
	}))
	stub.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{
		"entries": []map[string]any{
			{"ts": "2026-01-02T03:04:05Z", "entry_type": "run.completed", "severity": "info",
				"summary": "deploy finished", "actor_type": "agent"},
			{"ts": "2026-01-02T03:05:00Z", "entry_type": "peer.escalation", "severity": "error",
				"summary": "OOMKilled", "actor_type": "system"},
		},
		"count": 2,
	}))

	setFlagCov(t, journalCmd, "lines", "75")
	setFlagCov(t, journalCmd, "crew", "backend-team")
	setFlagCov(t, journalCmd, "agent", "agent-1")
	setFlagCov(t, journalCmd, "mission", "mis-1")
	setFlagCov(t, journalCmd, "trace-id", "trace-1")
	setFlagCov(t, journalCmd, "type", "run.completed")
	setFlagCov(t, journalCmd, "exclude-type", "container.metrics")
	setFlagCov(t, journalCmd, "severity", "info,error")
	setFlagCov(t, journalCmd, "actor-type", "agent,system")
	setFlagCov(t, journalCmd, "priority", "high")
	setFlagCov(t, journalCmd, "query", "deploy")
	setFlagCov(t, journalCmd, "since", "24h")

	out, err := captureStdoutCov(t, func() error {
		return journalCmd.RunE(journalCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"deploy finished", "OOMKilled", "run.completed"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}

	calls := stub.CallsFor("GET", "/api/v1/journal")
	if len(calls) != 1 {
		t.Fatalf("expected exactly one GET /api/v1/journal, got %d", len(calls))
	}
	q, qerr := url.ParseQuery(calls[0].Query)
	if qerr != nil {
		t.Fatalf("parse query: %v", qerr)
	}
	wantParams := map[string]string{
		"limit":              "75",
		"crew_id":            crewID,
		"agent_id":           "agent-1",
		"mission_id":         "mis-1",
		"trace_id":           "trace-1",
		"entry_type":         "run.completed",
		"exclude_entry_type": "container.metrics",
		"severity":           "info,error",
		"actor_type":         "agent,system",
		"priority":           "high",
		"q":                  "deploy",
	}
	for k, v := range wantParams {
		if got := q.Get(k); got != v {
			t.Errorf("query param %s: got %q want %q", k, got, v)
		}
	}
	if q.Get("since") == "" {
		t.Error("query param since missing — --since 24h should produce an RFC3339 timestamp")
	}
}

func TestJournalRunE_JSONFormat(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	setFormatCov(t, "json")

	stub.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{
		"entries": []map[string]any{{"id": "j_1", "summary": "hello"}},
		"count":   1,
	}))

	out, err := captureStdoutCov(t, func() error {
		return journalCmd.RunE(journalCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", err, out)
	}
	if len(entries) != 1 || entries[0]["summary"] != "hello" {
		t.Errorf("unexpected entries: %v", entries)
	}
}

func TestJournalRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)

	stub.OnGet("/api/v1/journal", clitest.ErrorResponse(500, "Internal server error"))

	err := journalCmd.RunE(journalCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "Internal server error") {
		t.Fatalf("want server error surfaced, got %v", err)
	}
}

// TestJournalRunE_FollowPermanentError exercises the --follow branch:
// a 401 SSE handshake is a permanent error, so followJournal must bail
// out immediately rather than entering the reconnect loop.
func TestJournalRunE_FollowPermanentError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)

	stub.OnGet("/api/v1/journal/stream", clitest.ErrorResponse(401, "Unauthorized"))
	setFlagCov(t, journalCmd, "follow", "true")

	err := journalCmd.RunE(journalCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("want permanent SSE error (status 401), got %v", err)
	}
}

// TestFollowJournal_ReconnectThenPermanent drives one full reconnect
// cycle: the first connection streams a heartbeat + one valid entry +
// one malformed entry then closes; the client must reconnect with
// Last-Event-ID set to the last seen ID, and the 404 on reconnect must
// terminate the loop as a permanent error.
func TestFollowJournal_ReconnectThenPermanent(t *testing.T) {
	var reqCount int32
	var lastEventID atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/journal/stream" {
			http.NotFound(w, r)
			return
		}
		if atomic.AddInt32(&reqCount, 1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, ": heartbeat\n\n")
			fmt.Fprint(w, "id: 42\n")
			fmt.Fprint(w, `data: {"ts":"2026-01-01T00:00:00Z","entry_type":"run.completed","severity":"info","summary":"stream entry","actor_type":"agent"}`+"\n\n")
			fmt.Fprint(w, "data: not-json\n\n")
			fmt.Fprint(w, "id: 43\n\n") // id-only frame: advances cursor, no data
			return // server closes; client should reconnect
		}
		lastEventID.Store(r.Header.Get("Last-Event-ID"))
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := cli.NewClient(srv.URL, "fake-token", covWorkspaceID)
	out, err := captureStdoutCov(t, func() error {
		return followJournal(client, url.Values{})
	})
	if err == nil || !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("want permanent 404 after reconnect, got %v", err)
	}
	if !strings.Contains(out, "stream entry") {
		t.Errorf("streamed entry not printed; stdout:\n%s", out)
	}
	if got, _ := lastEventID.Load().(string); got != "43" {
		t.Errorf("Last-Event-ID on reconnect: got %q want %q (id-only frame must advance the cursor)", got, "43")
	}
	if n := atomic.LoadInt32(&reqCount); n != 2 {
		t.Errorf("expected exactly 2 connection attempts, got %d", n)
	}
}

func TestJournalGetRunE_TextOutput(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)

	stub.OnGet("/api/v1/journal/j_abc", clitest.JSONResponse(200, map[string]any{
		"id": "j_abc", "ts": "2026-01-02T03:04:05Z", "entry_type": "keeper.decision",
		"severity": "warn", "summary": "credential denied", "actor_type": "keeper",
		"payload": map[string]any{"risk": 7},
		"refs":    map[string]any{"crew_id": "c1"},
	}))

	out, err := captureStdoutCov(t, func() error {
		return journalGetCmd.RunE(journalGetCmd, []string{"j_abc"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"credential denied", "payload", `"risk"`, "refs", `"crew_id"`} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
}

func TestJournalGetRunE_JSONFormat(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	setFormatCov(t, "json")

	stub.OnGet("/api/v1/journal/j_x", clitest.JSONResponse(200, map[string]any{
		"id": "j_x", "summary": "structured",
	}))

	out, err := captureStdoutCov(t, func() error {
		return journalGetCmd.RunE(journalGetCmd, []string{"j_x"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(out), &entry); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if entry["id"] != "j_x" {
		t.Errorf("entry id: got %v want j_x", entry["id"])
	}
}

func TestJournalPriorityRunE(t *testing.T) {
	t.Run("mark required", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceID}
		setFlagCov(t, journalPriorityCmd, "mark", "")

		err := journalPriorityCmd.RunE(journalPriorityCmd, []string{"j_1"})
		if err == nil || !strings.Contains(err.Error(), "--mark is required") {
			t.Fatalf("want '--mark is required', got %v", err)
		}
	})

	t.Run("invalid mark", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceID}
		setFlagCov(t, journalPriorityCmd, "mark", "urgent")

		err := journalPriorityCmd.RunE(journalPriorityCmd, []string{"j_1"})
		if err == nil || !strings.Contains(err.Error(), `invalid --mark "urgent"`) {
			t.Fatalf("want invalid mark error, got %v", err)
		}
	})

	t.Run("happy path posts priority and reason", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)

		stub.OnPost("/api/v1/journal/j_1/priority", clitest.JSONResponse(200, map[string]string{
			"id": "j_1", "priority": "permanent", "previous": "normal",
		}))
		setFlagCov(t, journalPriorityCmd, "mark", "permanent")
		setFlagCov(t, journalPriorityCmd, "reason", "FX compliance rule")

		out, err := captureStdoutCov(t, func() error {
			return journalPriorityCmd.RunE(journalPriorityCmd, []string{"j_1"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if !strings.Contains(out, "priority normal → permanent") {
			t.Errorf("stdout missing transition; got %q", out)
		}

		calls := stub.CallsFor("POST", "/api/v1/journal/j_1/priority")
		if len(calls) != 1 {
			t.Fatalf("expected one POST, got %d", len(calls))
		}
		var body map[string]string
		clitest.MustDecodeJSONBody(calls[0].Body, &body)
		if body["priority"] != "permanent" || body["reason"] != "FX compliance rule" {
			t.Errorf("request body: %v", body)
		}
	})
}

func TestJournalCountRunE(t *testing.T) {
	t.Run("happy path with filters", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)

		stub.OnGet("/api/v1/journal/count", clitest.JSONResponse(200, map[string]int64{"total": 42}))
		setFlagCov(t, journalCountCmd, "severity", "error")
		setFlagCov(t, journalCountCmd, "type", "budget.exceeded")
		setFlagCov(t, journalCountCmd, "agent", "agent-9")
		setFlagCov(t, journalCountCmd, "since", "24h")
		setFlagCov(t, journalCountCmd, "until", "1h")

		out, err := captureStdoutCov(t, func() error {
			return journalCountCmd.RunE(journalCountCmd, nil)
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if strings.TrimSpace(out) != "42" {
			t.Errorf("stdout: got %q want 42", out)
		}

		calls := stub.CallsFor("GET", "/api/v1/journal/count")
		if len(calls) != 1 {
			t.Fatalf("expected one GET count, got %d", len(calls))
		}
		q, _ := url.ParseQuery(calls[0].Query)
		if q.Get("severity") != "error" || q.Get("entry_type") != "budget.exceeded" || q.Get("agent_id") != "agent-9" {
			t.Errorf("count query params: %v", q)
		}
		if q.Get("since") == "" || q.Get("until") == "" {
			t.Errorf("since/until missing from query: %v", q)
		}
	})

	t.Run("bad until", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceID}
		setFlagCov(t, journalCountCmd, "until", "garbage")

		err := journalCountCmd.RunE(journalCountCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "bad --until") {
			t.Fatalf("want bad --until error, got %v", err)
		}
	})

	t.Run("invalid severity rejected before request", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceID}
		setFlagCov(t, journalCountCmd, "severity", "fatal")

		err := journalCountCmd.RunE(journalCountCmd, nil)
		if err == nil || !strings.Contains(err.Error(), `invalid --severity value "fatal"`) {
			t.Fatalf("want severity validation error, got %v", err)
		}
	})
}

func TestJournalLookupRunE_TextOutput(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)

	crewRef := "ccrewbbbbbbbbbbbbbbbbbb"
	stub.OnGet("/api/v1/journal/lookup", clitest.JSONResponse(200, map[string]any{
		"crews": []map[string]any{
			{"id": "c1", "name": "Backend", "slug": "backend-team"},
		},
		"agents": []map[string]any{
			{"id": "a1", "name": "Viktor", "slug": "viktor", "crew_id": crewRef},
			{"id": "a2", "name": "Eva", "slug": "eva", "crew_id": nil},
		},
		"missions": []map[string]any{
			{"id": "m1", "title": "Ship v1", "status": "ACTIVE"},
		},
	}))

	out, err := captureStdoutCov(t, func() error {
		return journalLookupCmd.RunE(journalLookupCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Crews (1)", "backend-team", "Agents (2)", "Viktor", "Eva", "Missions (1)", "Ship v1", "ACTIVE"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
	// Agent without a crew renders the "-" placeholder.
	if !strings.Contains(out, "-") {
		t.Errorf("expected '-' placeholder for crewless agent; got:\n%s", out)
	}
}

func TestJournalLookupRunE_JSONFormat(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	setFormatCov(t, "json")

	stub.OnGet("/api/v1/journal/lookup", clitest.JSONResponse(200, map[string]any{
		"crews": []map[string]any{{"id": "c1", "name": "Backend", "slug": "backend-team"}},
	}))

	out, err := captureStdoutCov(t, func() error {
		return journalLookupCmd.RunE(journalLookupCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var body struct {
		Crews []struct {
			Slug string `json:"slug"`
		} `json:"crews"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if len(body.Crews) != 1 || body.Crews[0].Slug != "backend-team" {
		t.Errorf("crews: %+v", body.Crews)
	}
}

func TestPrintJournalEntry_FormatsTimestampAndFields(t *testing.T) {
	out, _ := captureStdoutCov(t, func() error {
		printJournalEntry(map[string]any{
			"ts":         "2026-01-02T03:04:05.123456Z",
			"entry_type": "peer.conversation",
			"severity":   "notice",
			"summary":    "ping pong",
			"actor_type": "agent",
		})
		return nil
	})
	if !strings.Contains(out, "2026-01-02 03:04:05") {
		t.Errorf("timestamp not reformatted; got %q", out)
	}
	for _, want := range []string{"notice", "peer.conversation", "ping pong"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}

	// Unparseable ts is printed verbatim rather than dropped.
	out2, _ := captureStdoutCov(t, func() error {
		printJournalEntry(map[string]any{"ts": "garbage", "summary": "x"})
		return nil
	})
	if !strings.Contains(out2, "garbage") {
		t.Errorf("raw ts not preserved; got %q", out2)
	}
}

// newDeadServerCov returns a base URL whose port is guaranteed closed —
// an httptest server that has already been shut down. Connecting fails
// fast and deterministically, which exercises the transport-error
// branches after client.Get/Post.
func newDeadServerCov(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	return url
}

func setupDeadCLICov(t *testing.T) {
	t.Helper()
	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagServer = ""
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceID, Server: newDeadServerCov(t)}
}

// TestJournalSubcommands_AuthGates pins the requireAuth/requireWorkspace
// short-circuits on every journal subcommand.
func TestJournalSubcommands_AuthGates(t *testing.T) {
	cases := []struct {
		name string
		run  func() error
	}{
		{"get", func() error { return journalGetCmd.RunE(journalGetCmd, []string{"j_1"}) }},
		{"count", func() error { return journalCountCmd.RunE(journalCountCmd, nil) }},
		{"lookup", func() error { return journalLookupCmd.RunE(journalLookupCmd, nil) }},
		{"priority", func() error { return journalPriorityCmd.RunE(journalPriorityCmd, []string{"j_1"}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name+" no auth", func(t *testing.T) {
			saveCLIState(t)
			cliCfg = &cli.CLIConfig{}
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "not logged in") {
				t.Fatalf("want not logged in, got %v", err)
			}
		})
		t.Run(tc.name+" no workspace", func(t *testing.T) {
			saveCLIState(t)
			cliCfg = &cli.CLIConfig{Token: "fake-token"}
			flagWorkspace = ""
			t.Setenv("CREWSHIP_WORKSPACE", "")
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "workspace") {
				t.Fatalf("want workspace error, got %v", err)
			}
		})
	}
}

// TestJournalCommands_NetworkError covers the transport-failure branch
// after each subcommand's HTTP call.
func TestJournalCommands_NetworkError(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T) error
	}{
		{"list", func(t *testing.T) error { return journalCmd.RunE(journalCmd, nil) }},
		{"get", func(t *testing.T) error { return journalGetCmd.RunE(journalGetCmd, []string{"j_1"}) }},
		{"count", func(t *testing.T) error { return journalCountCmd.RunE(journalCountCmd, nil) }},
		{"lookup", func(t *testing.T) error { return journalLookupCmd.RunE(journalLookupCmd, nil) }},
		{"priority", func(t *testing.T) error {
			setFlagCov(t, journalPriorityCmd, "mark", "high")
			return journalPriorityCmd.RunE(journalPriorityCmd, []string{"j_1"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupDeadCLICov(t)
			if err := tc.run(t); err == nil {
				t.Fatal("want connection error against dead server")
			}
		})
	}
}

func TestJournalRunE_CrewResolveError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": "c1", "slug": "other-crew"},
	}))
	setFlagCov(t, journalCmd, "crew", "ghost-crew")

	err := journalCmd.RunE(journalCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "crew not found: ghost-crew") {
		t.Fatalf("want crew not found, got %v", err)
	}
}

func TestJournalRunE_YAMLFormat(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	setFormatCov(t, "yaml")
	stub.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{
		"entries": []map[string]any{{"summary": "yaml entry"}},
	}))

	out, err := captureStdoutCov(t, func() error {
		return journalCmd.RunE(journalCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "summary: yaml entry") {
		t.Errorf("yaml output: %q", out)
	}
}

func TestJournalRunE_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	stub.OnGet("/api/v1/journal", clitest.TextResponse(200, "not json"))

	if err := journalCmd.RunE(journalCmd, nil); err == nil {
		t.Fatal("want decode error")
	}
}

func TestJournalGetRunE_YAMLAndErrors(t *testing.T) {
	t.Run("yaml format", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		setFormatCov(t, "yaml")
		stub.OnGet("/api/v1/journal/j_y", clitest.JSONResponse(200, map[string]any{"id": "j_y"}))

		out, err := captureStdoutCov(t, func() error {
			return journalGetCmd.RunE(journalGetCmd, []string{"j_y"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if !strings.Contains(out, "id: j_y") {
			t.Errorf("yaml output: %q", out)
		}
	})

	t.Run("not found", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet("/api/v1/journal/ghost", clitest.ErrorResponse(404, "entry not found"))
		err := journalGetCmd.RunE(journalGetCmd, []string{"ghost"})
		if err == nil || !strings.Contains(err.Error(), "entry not found") {
			t.Fatalf("want 404 surfaced, got %v", err)
		}
	})

	t.Run("decode error", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet("/api/v1/journal/j_b", clitest.TextResponse(200, "not json"))
		if err := journalGetCmd.RunE(journalGetCmd, []string{"j_b"}); err == nil {
			t.Fatal("want decode error")
		}
	})
}

func TestJournalCountRunE_FormatsAndCrew(t *testing.T) {
	t.Run("json format", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		setFormatCov(t, "json")
		stub.OnGet("/api/v1/journal/count", clitest.JSONResponse(200, map[string]int64{"total": 7}))

		out, err := captureStdoutCov(t, func() error {
			return journalCountCmd.RunE(journalCountCmd, nil)
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		var body struct {
			Total int64 `json:"total"`
		}
		if uerr := json.Unmarshal([]byte(out), &body); uerr != nil || body.Total != 7 {
			t.Errorf("json output: %q (%v)", out, uerr)
		}
	})

	t.Run("yaml format", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		setFormatCov(t, "yaml")
		stub.OnGet("/api/v1/journal/count", clitest.JSONResponse(200, map[string]int64{"total": 9}))

		out, err := captureStdoutCov(t, func() error {
			return journalCountCmd.RunE(journalCountCmd, nil)
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if !strings.Contains(out, "total: 9") {
			t.Errorf("yaml output: %q", out)
		}
	})

	t.Run("crew filter resolves to id", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		crewID := "ccrewcccccccccccccccccc"
		stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
			{"id": crewID, "slug": "backend-team"},
		}))
		stub.OnGet("/api/v1/journal/count", clitest.JSONResponse(200, map[string]int64{"total": 1}))
		setFlagCov(t, journalCountCmd, "crew", "backend-team")

		_, err := captureStdoutCov(t, func() error {
			return journalCountCmd.RunE(journalCountCmd, nil)
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		calls := stub.CallsFor("GET", "/api/v1/journal/count")
		if len(calls) != 1 || !strings.Contains(calls[0].Query, "crew_id="+crewID) {
			t.Errorf("crew_id missing from count query: %+v", calls)
		}
	})

	t.Run("crew resolve failure", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
		setFlagCov(t, journalCountCmd, "crew", "ghost")

		err := journalCountCmd.RunE(journalCountCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "crew not found") {
			t.Fatalf("want crew not found, got %v", err)
		}
	})

	t.Run("server error surfaced", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet("/api/v1/journal/count", clitest.ErrorResponse(500, "count broke"))
		err := journalCountCmd.RunE(journalCountCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "count broke") {
			t.Fatalf("want 500 surfaced, got %v", err)
		}
	})

	t.Run("decode error", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet("/api/v1/journal/count", clitest.TextResponse(200, "not json"))
		if err := journalCountCmd.RunE(journalCountCmd, nil); err == nil {
			t.Fatal("want decode error")
		}
	})
}

func TestJournalLookupRunE_YAMLAndErrors(t *testing.T) {
	t.Run("yaml format", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		setFormatCov(t, "yaml")
		stub.OnGet("/api/v1/journal/lookup", clitest.JSONResponse(200, map[string]any{
			"crews": []map[string]any{{"id": "c1", "name": "Backend", "slug": "backend-team"}},
		}))

		out, err := captureStdoutCov(t, func() error {
			return journalLookupCmd.RunE(journalLookupCmd, nil)
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if !strings.Contains(out, "slug: backend-team") {
			t.Errorf("yaml output: %q", out)
		}
	})

	t.Run("server error", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet("/api/v1/journal/lookup", clitest.ErrorResponse(503, "lookup busy"))
		err := journalLookupCmd.RunE(journalLookupCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "lookup busy") {
			t.Fatalf("want 503 surfaced, got %v", err)
		}
	})

	t.Run("decode error", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet("/api/v1/journal/lookup", clitest.TextResponse(200, "not json"))
		if err := journalLookupCmd.RunE(journalLookupCmd, nil); err == nil {
			t.Fatal("want decode error")
		}
	})
}

func TestJournalPriorityRunE_ServerSideErrors(t *testing.T) {
	t.Run("forbidden surfaced", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnPost("/api/v1/journal/j_1/priority", clitest.ErrorResponse(403, "requires OWNER or ADMIN"))
		setFlagCov(t, journalPriorityCmd, "mark", "pin")

		err := journalPriorityCmd.RunE(journalPriorityCmd, []string{"j_1"})
		if err == nil || !strings.Contains(err.Error(), "requires OWNER or ADMIN") {
			t.Fatalf("want 403 surfaced, got %v", err)
		}
	})

	t.Run("decode error", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnPost("/api/v1/journal/j_1/priority", clitest.TextResponse(200, "not json"))
		setFlagCov(t, journalPriorityCmd, "mark", "pin")

		if err := journalPriorityCmd.RunE(journalPriorityCmd, []string{"j_1"}); err == nil {
			t.Fatal("want decode error")
		}
	})
}
