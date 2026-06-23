package server

// Coverage tests for server_lifecycle.go: the conversation-search
// adapter, boot-time container rehydration, the ephemeral-broadcast
// adapter, Shutdown's optional-subsystem hooks, startIPC's listen-error
// path, recoverOrphanedRuns' no-journal-writer degradation, and a
// full-deps Start/Shutdown round trip that drives the background-worker
// wiring branches.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/logging"
)

func TestConvStoreAdapter_SearchConversations(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	logger := logging.New("error", "json", nil)
	store := conversation.NewStore(t.TempDir(), logger, conversation.WithDB(db))
	t.Cleanup(store.Close)

	msg := conversation.Message{
		ID:        "m1",
		AgentID:   "agentA",
		Role:      conversation.RoleUser,
		Content:   "please deploy the staging pipeline tonight",
		Timestamp: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
	}
	if err := store.Append(context.Background(), "sess1", msg); err != nil {
		t.Fatalf("append: %v", err)
	}

	a := &convStoreAdapter{store: store}
	hits, err := a.SearchConversations(context.Background(), "agentA", "deploy", 5)
	if err != nil {
		t.Fatalf("SearchConversations: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	h := hits[0]
	if h.SessionID != "sess1" || h.AgentID != "agentA" || h.Role != "user" {
		t.Errorf("hit scope = %+v, want sess1/agentA/user", h)
	}
	if h.Content != msg.Content {
		t.Errorf("content = %q, want original message", h.Content)
	}
	if _, perr := time.Parse(time.RFC3339Nano, h.Timestamp); perr != nil {
		t.Errorf("timestamp %q is not RFC3339Nano: %v", h.Timestamp, perr)
	}

	// Wrong agent scope must not leak the other agent's messages.
	other, err := a.SearchConversations(context.Background(), "agentB", "deploy", 5)
	if err != nil {
		t.Fatalf("SearchConversations (agentB): %v", err)
	}
	if len(other) != 0 {
		t.Errorf("agentB hits = %d, want 0 (agent scoping)", len(other))
	}
}

func TestConvStoreAdapter_SearchConversations_NoMirrorErrors(t *testing.T) {
	t.Parallel()
	logger := logging.New("error", "json", nil)
	store := conversation.NewStore(t.TempDir(), logger) // no WithDB → no FTS mirror
	t.Cleanup(store.Close)

	a := &convStoreAdapter{store: store}
	if _, err := a.SearchConversations(context.Background(), "agentA", "anything", 5); err == nil {
		t.Error("want error from search without a DB-backed mirror, got nil")
	}
}

func TestConvStoreAdapter_ReadErrorPropagates(t *testing.T) {
	t.Parallel()
	logger := logging.New("error", "json", nil)
	store := conversation.NewStore(t.TempDir(), logger)
	t.Cleanup(store.Close)
	a := &convStoreAdapter{store: store}
	// ".." in the session ID fails the store's validation; the adapter
	// must propagate the error rather than mapping nil messages.
	if _, err := a.Read(context.Background(), "x..y", 0, 0); err == nil {
		t.Error("want invalid-session error from adapter Read, got nil")
	}
}

// covLookupContainer implements provider.ContainerProvider (via the
// embedded mockContainer) plus CrewContainerLookup with per-slug canned
// answers, so rehydrateContainers' four branches are all exercised.
type covLookupContainer struct {
	mockContainer
}

func (c *covLookupContainer) FindCrewContainer(_ context.Context, slug string) (string, bool, error) {
	switch slug {
	case "run-slug":
		return "ctr-running", true, nil
	case "stop-slug":
		return "ctr-stopped", false, nil
	case "err-slug":
		return "", false, errors.New("docker transport down")
	default:
		return "", false, nil
	}
}

func TestRehydrateContainers_RegistersOnlyRunning(t *testing.T) {
	s := newTestServerWithDeps(t)
	lookup := &covLookupContainer{}
	s.container = lookup
	s.statsCollector = NewStatsCollector(lookup, nil, nil, time.Hour)

	mustExec(t, s.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_rh','RH','ws-rh')`)
	for _, c := range [][2]string{
		{"cr_run", "run-slug"},
		{"cr_stop", "stop-slug"},
		{"cr_none", "none-slug"},
		{"cr_err", "err-slug"},
	} {
		mustExec(t, s.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, 'ws_rh', ?, ?)`,
			c[0], c[0], c[1])
	}

	s.rehydrateContainers(context.Background())

	tracked := s.statsCollector.Tracked()
	if len(tracked) != 1 {
		t.Fatalf("tracked = %d containers, want exactly 1 (only the running one)", len(tracked))
	}
	tc := tracked[0]
	if tc.ContainerID != "ctr-running" || tc.CrewID != "cr_run" || tc.WorkspaceID != "ws_rh" {
		t.Errorf("tracked = %+v, want ctr-running/cr_run/ws_rh", tc)
	}
}

func TestRehydrateContainers_ProviderWithoutLookupSkips(t *testing.T) {
	s := newTestServerWithDeps(t) // mockContainer: no CrewContainerLookup
	s.statsCollector = NewStatsCollector(s.container, nil, nil, time.Hour)
	mustExec(t, s.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_nl','NL','ws-nl')`)
	mustExec(t, s.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr_nl','ws_nl','NL','nl-slug')`)

	s.rehydrateContainers(context.Background())

	if n := len(s.statsCollector.Tracked()); n != 0 {
		t.Errorf("tracked = %d, want 0 when provider lacks CrewContainerLookup", n)
	}
}

func TestRehydrateContainers_QueryErrorIsBestEffort(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	lookup := &covLookupContainer{}
	s := &Server{
		db:             db,
		logger:         logging.New("error", "json", nil),
		container:      lookup,
		statsCollector: NewStatsCollector(lookup, nil, nil, time.Hour),
	}
	s.rehydrateContainers(context.Background()) // must not panic
	if n := len(s.statsCollector.Tracked()); n != 0 {
		t.Errorf("tracked = %d, want 0 on DB failure", n)
	}
}

func TestEphemeralHubAdapter_NilHubIsNoOp(t *testing.T) {
	t.Parallel()
	var a ephemeralHubAdapter // nil hub
	// Contract: headless harnesses (no WS layer) must not panic.
	a.BroadcastWorkspaceEvent("ws1", "agent.expired", map[string]string{"agent_id": "a1"})
}

func TestEphemeralHubAdapter_ForwardsToHub(t *testing.T) {
	s := newTestServerForT(t)
	a := ephemeralHubAdapter{hub: s.wsHub}
	// No subscribers — the broadcast must be a safe no-op rather than a
	// panic or a blocked send.
	a.BroadcastWorkspaceEvent("ws1", "agent.expired", map[string]string{"agent_id": "a1"})
}

// covErrCloseState fails Close so Shutdown's state-close error branch
// executes (logged, never propagated).
type covErrCloseState struct {
	mockState
}

func (c *covErrCloseState) Close() error { return errors.New("close failed") }

func TestShutdown_RunsOptionalShutdownHooks(t *testing.T) {
	s := newTestServerForT(t)
	var telCalled, pprofCalled, pyroCalled bool
	s.telemetryShutdown = func() { telCalled = true }
	s.pprofShutdown = func() { pprofCalled = true }
	s.pyroscopeShutdown = func() { pyroCalled = true }
	s.state = &covErrCloseState{}
	_, cancel := context.WithCancel(context.Background())
	s.runCancel = cancel

	// A failing state-provider Close is logged but must not surface.
	if err := s.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !telCalled || !pprofCalled || !pyroCalled {
		t.Errorf("shutdown hooks called = (telemetry:%v pprof:%v pyroscope:%v), want all true",
			telCalled, pprofCalled, pyroCalled)
	}
}

func TestStartIPC_ListenErrorPropagates(t *testing.T) {
	s := newTestServerForT(t)
	// Parent directory does not exist → net.Listen("unix", ...) fails.
	s.cfg.IPC.SocketPath = filepath.Join(t.TempDir(), "missing-subdir", "ipc.sock")

	err := s.startIPC()
	if err == nil {
		t.Fatal("want listen error for socket in missing directory, got nil")
	}
}

func TestRecoverOrphanedRuns_WithoutJournalWriterSkipsCancelEntries(t *testing.T) {
	s := newTestServerWithDeps(t)
	s.journalWriter = nil

	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, s.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_or','OR','ws-or')`)
	mustExec(t, s.db, `INSERT INTO agents (id, workspace_id, name, slug, status, created_at, updated_at)
	                   VALUES ('ag_or','ws_or','A','a-or','RUNNING',?,?)`, now, now)
	mustExec(t, s.db, `INSERT INTO journal_entries
		(id, workspace_id, agent_id, ts, entry_type, severity, actor_type, summary, payload, refs, trace_id, priority)
		VALUES ('je_or','ws_or','ag_or', strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		        'run.started','info','sidecar','run started','{}','{}','tr_or','normal')`)

	s.recoverOrphanedRuns(context.Background())

	// Without a journal writer no run.cancelled can be emitted...
	var terminals int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM journal_entries
		WHERE trace_id = 'tr_or' AND entry_type != 'run.started'`).Scan(&terminals); err != nil {
		t.Fatal(err)
	}
	if terminals != 0 {
		t.Errorf("terminal entries = %d, want 0 without a journal writer", terminals)
	}
	// ...so the trace still counts as live and the agent must NOT be
	// reset (resetting while the run trace is open would lie about state).
	var status string
	if err := s.db.QueryRow(`SELECT status FROM agents WHERE id = 'ag_or'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "RUNNING" {
		t.Errorf("agent status = %q, want RUNNING (trace still open)", status)
	}
}

func TestRecoverOrphanedRuns_ScanErrorIsBestEffort(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "closed2.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	s := &Server{db: db, logger: logging.New("error", "json", nil)}
	s.recoverOrphanedRuns(context.Background()) // must log + return, not panic
}

// TestStart_FullDepsBootAndShutdown drives Server.Start with the widest
// dependency set an offline test can wire: DB + container provider +
// state + LLM proxy + Keeper summarizer config + memory root. Pins that
// (a) boot reaches the background-worker wiring without panicking,
// (b) recoverOrphanedRuns runs as part of Start (observable: the
// orphaned trace gains run.cancelled and the agent flips to IDLE), and
// (c) ctx cancellation shuts the whole thing down cleanly.
func TestStart_FullDepsBootAndShutdown(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_boot2','B','ws-boot2')`)
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, name, slug, status, created_at, updated_at)
	                 VALUES ('ag_boot2','ws_boot2','A','a-boot2','RUNNING',?,?)`, now, now)
	mustExec(t, db, `INSERT INTO journal_entries
		(id, workspace_id, agent_id, ts, entry_type, severity, actor_type, summary, payload, refs, trace_id, priority)
		VALUES ('je_boot2','ws_boot2','ag_boot2', strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		        'run.started','info','sidecar','orphan','{}','{}','tr_boot2','normal')`)

	sockPath := filepath.Join("/tmp", "cs-cov-"+randomShort()+".sock")
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	cfg := silentCfg()
	cfg.IPC.SocketPath = sockPath
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = 0 // ephemeral
	cfg.Storage.BasePath = t.TempDir()
	cfg.Storage.LogPath = t.TempDir()
	cfg.Storage.MemoryRoot = t.TempDir()
	cfg.Container.SidecarEnabled = true
	cfg.Keeper.Enabled = true
	cfg.Keeper.OllamaURL = "http://127.0.0.1:1" // never dialled during boot
	cfg.Keeper.Model = "test-model"
	cfg.Auth.InternalToken = "cov-internal-token"
	cfg.LLMProxy.Enabled = true
	cfg.LLMProxy.TokenSyncInterval = time.Hour   // first fetch only
	cfg.LLMProxy.HealthCheckInterval = time.Hour // first check only
	cfg.Auth.NextjsURL = "http://127.0.0.1:1"    // unreachable, fails fast

	logger := logging.New("error", "json", nil)
	s := New(cfg, logger, &Deps{DB: db, Container: &mockContainer{}, State: newMockState()})
	t.Cleanup(s.StopBackground)

	// The LLM proxy branch of New must have wired both workers — Start
	// then launches them.
	if s.tokenSyncer == nil || s.credMonitor == nil {
		t.Fatalf("tokenSyncer/credMonitor = (%v,%v), want both non-nil with LLMProxy enabled", s.tokenSyncer, s.credMonitor)
	}
	if s.consolidator == nil || s.consolidator.Summarizer == nil {
		t.Fatal("consolidator summarizer not built despite Keeper Ollama config")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	// recoverOrphanedRuns runs synchronously at the top of Start; poll
	// for its observable side effect.
	deadline := time.Now().Add(5 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		var status string
		if err := db.QueryRow(`SELECT status FROM agents WHERE id = 'ag_boot2'`).Scan(&status); err != nil {
			t.Fatalf("poll agent: %v", err)
		}
		if status == "IDLE" {
			recovered = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start returned %v on ctx cancel, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not shut down within 10s of ctx cancel")
	}

	if !recovered {
		t.Error("orphaned RUNNING agent was not reset to IDLE during boot")
	}
	var terminal string
	if err := db.QueryRow(`SELECT entry_type FROM journal_entries
		WHERE trace_id = 'tr_boot2' AND entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout')
		LIMIT 1`).Scan(&terminal); err != nil {
		t.Fatalf("expected terminal entry for orphaned trace: %v", err)
	}
	if terminal != "run.cancelled" {
		t.Errorf("terminal entry = %q, want run.cancelled", terminal)
	}
}

// TestStart_HTTPListenErrorPropagates pins the errCh → Shutdown → return
// branch of Start: a second server on an already-bound port must surface
// the bind failure instead of hanging.
func TestStart_HTTPListenErrorPropagates(t *testing.T) {
	// Occupy a port with a throwaway listener.
	blocker := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(blocker.Close)
	addr := blocker.Listener.Addr().String()
	host, portStr, ok := splitHostPortForTest(addr)
	if !ok {
		t.Fatalf("unexpected listener addr %q", addr)
	}

	sockPath := filepath.Join("/tmp", "cs-cov-"+randomShort()+".sock")
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	cfg := silentCfg()
	cfg.IPC.SocketPath = sockPath
	cfg.Server.Host = host
	cfg.Server.Port = portStr

	logger := logging.New("error", "json", nil)
	s := New(cfg, logger, &Deps{DB: openTestDB(t)})
	t.Cleanup(s.StopBackground)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Start returned nil, want bind error for occupied port")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return within 10s despite occupied port")
	}
}

// splitHostPortForTest parses "127.0.0.1:54321" without dragging in
// strconv noise at the call site.
func splitHostPortForTest(addr string) (host string, port int, ok bool) {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			host = addr[:i]
			p := 0
			for _, c := range addr[i+1:] {
				if c < '0' || c > '9' {
					return "", 0, false
				}
				p = p*10 + int(c-'0')
			}
			return host, p, true
		}
	}
	return "", 0, false
}
