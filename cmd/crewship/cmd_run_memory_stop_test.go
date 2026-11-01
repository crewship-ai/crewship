//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
	"github.com/crewship-ai/crewship/internal/runauthority"
	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/crewship-ai/crewship/internal/work"
	"github.com/crewship-ai/crewship/internal/ws"
)

type cliMemoryStoppedProvider struct {
	cliMemoryProvider
	stopped    bool
	workID     string
	generation int64
}

func (p *cliMemoryStoppedProvider) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if len(cfg.Env) == 0 && strings.Contains(strings.Join(cfg.Cmd, " "), "tmux kill-session") {
		p.mu.Lock()
		p.stopped = true
		p.mu.Unlock()
		return &provider.ExecResult{ExecID: "stop-exact-memory-exec", Reader: io.NopCloser(strings.NewReader("ABSENT"))}, nil
	}
	result, err := p.cliMemoryProvider.Exec(ctx, cfg)
	if err == nil && len(cfg.Env) > 0 {
		p.mu.Lock()
		defer p.mu.Unlock()
		err = p.db.QueryRowContext(ctx, `SELECT work_id,generation FROM work_attempts WHERE run_id=?`, p.run).Scan(&p.workID, &p.generation)
	}
	return result, err
}
func (p *cliMemoryStoppedProvider) ExecInspect(_ context.Context, id string) (bool, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if id == "memory-exec" && p.stopped {
		return false, 143, nil
	}
	return false, 0, nil
}

// This joins the production Bridge, IPC resolver, authenticated internal API,
// journal and SQLite work ledger. Only container execution is controlled; no
// work authorization rows or terminal projections are inserted by the fixture.
func TestHostChatConfirmedStopPersistsCurrentLedgerOutcome(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO users(id,email) VALUES('memory-owner','owner@memory.test')`)
	seed(`INSERT INTO workspaces(id,name,slug) VALUES('memory-w','Memory','memory-w')`)
	seed(`INSERT INTO workspace_members(id,user_id,workspace_id,role) VALUES('memory-member','memory-owner','memory-w','OWNER')`)
	seed(`INSERT INTO crews(id,workspace_id,name,slug,network_mode,container_memory_mb,container_cpus,cached_image) VALUES('memory-crew','memory-w','Memory','memory-crew','free',4096,2.0,'synthetic-memory-image')`)
	seed(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status,cli_adapter,tool_profile,memory_enabled) VALUES('memory-agent','memory-w','memory-crew','Memory','memory-agent','AGENT','IDLE','CLAUDE_CODE','CODING',1)`)
	seed(`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility,origin) VALUES('memory-chat','memory-w','memory-agent','memory-owner','private','CLI')`)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	writer := journal.NewWriter(db, logger, journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	root := t.TempDir()
	router, err := api.NewRouter(db, "synthetic-memory-stop-jwt-secret-long-enough", logger, api.WithInternalToken(cliMemoryMaster), api.WithJournal(writer), api.WithStoragePath(root), api.WithMemoryVersionsBlobRoot(filepath.Join(root, "blobs")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	server := httptest.NewServer(router)
	defer server.Close()
	state, err := bbolt.New(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	container := &cliMemoryStoppedProvider{cliMemoryProvider: cliMemoryProvider{db: db, url: server.URL}}
	orch := orchestrator.New(container, state, logger)
	resolver := chatbridge.NewIPCResolver(server.URL, cliMemoryMaster, logger)
	store := conversation.NewStore(root, logger)
	t.Cleanup(store.Close)
	bridge := chatbridge.New(orch, container, store, logcollector.NewWriter(root, logger), resolver, chatbridge.BridgeConfig{}, logger)
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	err = bridge.HandleChatMessage(ctx, "memory-owner", "memory-chat", "write memory then stop", func(e ws.ChatEvent) {
		if e.Type == "text" {
			cancel(ws.ErrExplicitStopRequested)
		}
	})
	if !errors.Is(err, ws.ErrRunCancelled) {
		t.Fatalf("durable cancellation not acknowledged: %v", err)
	}
	container.mu.Lock()
	run, workID, generation, writeErr := container.run, container.workID, container.generation, container.writeErr
	container.mu.Unlock()
	if writeErr != nil || run == "" || workID == "" || generation <= 0 {
		t.Fatalf("provider never acquired fenced current attempt: %s %s %d %v", run, workID, generation, writeErr)
	}
	ledger := work.NewStore(db)
	binding, owned, err := ledger.HostChatAttempt(t.Context(), run)
	if err != nil || !owned || !binding.Ended || binding.State != work.StateCancelled || binding.WorkID != workID || binding.Generation != generation {
		t.Fatalf("stop replaced/lost current attempt: %+v %v", binding, err)
	}
	projection, owned, err := ledger.RunProjection(t.Context(), run)
	if err != nil || !owned || !projection.Ready || projection.Status != "CANCELLED" || projection.Result.ExitCode != nil {
		t.Fatalf("actual API/SQLite stop projection: %+v %v", projection, err)
	}
	// CANCELLED presentation deliberately omits a display exit code. The
	// immutable attempt still must retain the actual provider terminal proof.
	var evidence, rawResult string
	if err = db.QueryRow(`SELECT exit_evidence,run_result_json FROM work_attempts WHERE run_id=? AND work_id=? AND generation=?`, run, workID, generation).Scan(&evidence, &rawResult); err != nil {
		t.Fatal(err)
	}
	var result work.RunResult
	if err = json.Unmarshal([]byte(rawResult), &result); err != nil || result.ExitCode == nil || *result.ExitCode != 143 || evidence != "provider_exit:143" {
		t.Fatalf("durable provider proof lost: %+v evidence=%q %v", result, evidence, err)
	}
	var terminal, attempts int
	if err = db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=? AND entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout')`, run).Scan(&terminal); err != nil || terminal != 1 {
		t.Fatalf("terminal receipts=%d %v", terminal, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM work_attempts WHERE work_id=?`, workID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("stop manufactured attempt: %d %v", attempts, err)
	}
	if err = container.mutate(run, generation, "after-stop-write"); err == nil {
		t.Fatal("cancelled attempt retained memory authority")
	}
	receipt, err := chataudience.CaptureTrusted(t.Context(), db, "memory-chat", "memory-owner", nil)
	if err != nil || receipt == nil {
		t.Fatal(err)
	}
	next := chatbridge.WithHostChatWorkAdmission(runauthority.WithTriggerMessage(chataudience.WithReceipt(t.Context(), receipt), "next-after-confirmed-stop"))
	if err = resolver.CreateRun(next, "msg_after_confirmed_stop", "memory-agent", "memory-chat", "memory-w", "USER", nil); err != nil {
		t.Fatalf("actual confirmed exit failed to release serial capacity: %v", err)
	}
	if _, owned, err = ledger.HostChatAttempt(t.Context(), "msg_after_confirmed_stop"); err != nil || !owned {
		t.Fatal(fmt.Errorf("next normal API admission has no attempt: %v", err))
	}
}
