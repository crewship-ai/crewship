package server

// Server runtime: Start / Shutdown drive the process lifecycle, plus
// the side helpers they call (IPC listener, conversation-store adapter
// for the goapi router, orphaned-run recovery, devcontainer catalog
// refresh). Extracted from server.go so the constructor wiring stays
// readable in one file and the runtime path in another.

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/presence"
	"github.com/crewship-ai/crewship/internal/provider"
)

// Server is the main crewship process, wiring together the HTTP server, IPC

// stats collector, and all background goroutines. It blocks until ctx is done.
func (s *Server) Start(ctx context.Context) error {
	s.startedAt = time.Now()

	// Recover orphaned RUNNING runs from previous crashes/restarts.
	// Without this, agents whose runs were interrupted stay RUNNING forever.
	if s.db != nil {
		s.recoverOrphanedRuns(ctx)
	}

	ctx, cancel := context.WithCancel(ctx)
	s.runCtx, s.runCancel = ctx, cancel
	defer cancel()

	errCh := make(chan error, 2)

	go func() {
		s.logger.Info("starting HTTP server", "addr", s.httpServer.Addr)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	go func() {
		if err := s.startIPC(); err != nil {
			errCh <- fmt.Errorf("ipc server: %w", err)
		}
	}()

	go s.wsHub.Run(ctx)
	go s.orchestrator.Start(ctx)

	// Rehydrate stats + file-watcher tracking for crew containers that
	// survived a previous crewshipd run. Without this, the stats
	// collector and listening-port scanner stay blind to existing
	// containers until each crew's next dispatch (which calls
	// EnsureCrewRuntime + the registration callback). Synchronous so
	// the bookkeeping is in place before the collectors start polling.
	if s.statsCollector != nil && s.db != nil {
		s.rehydrateContainers(ctx)
	}

	if s.statsCollector != nil {
		go s.statsCollector.Run(ctx)
	}

	if s.tokenSyncer != nil {
		go s.tokenSyncer.Run(ctx)
	}
	if s.credMonitor != nil {
		go s.credMonitor.Run(ctx)
	}

	// Crew Journal background workers. Each is a small goroutine that
	// only runs when s.db and the journal writer are live — early init
	// paths that come up without DB (tests, --dry-run) skip silently.
	if s.db != nil && s.journalWriter != nil {
		// Harbor Master timeout sweeper: every 30s, flip past-due pending
		// approvals to 'timeout' status so blocked agents unstick
		// deterministically even if the UI is down.
		go harbormaster.StartTimeoutSweeper(ctx, s.db, s.journalWriter, 30*time.Second)

		// Crow's Nest port scanner: every 10s, diff the ACTIVE set of
		// port_exposures rows and emit network.port_opened /
		// network.port_closed journal entries for each change. See
		// port_exposure_scanner.go for why we poll instead of subscribing
		// to Docker events.
		go runPortExposureScanner(ctx, s.db, s.journalWriter, s.logger)

		// Crow's Nest listening-port scanner: every 15s, exec into each
		// tracked crew container and read /proc/net/tcp{,6} to discover
		// LISTEN sockets that the agent didn't go through /expose-port
		// to register (python -m http.server, dev servers, etc.). Emits
		// the same network.port_* journal types so the Network panel
		// renders both sources uniformly.
		if s.container != nil && s.statsCollector != nil {
			go runListeningPortScanner(ctx, s.container, s.statsCollector, s.journalWriter, s.logger)
		}

		// Watch Roster offline sweeper: every 60s, flip agents idle >5min
		// to offline. The transition itself emits agent.status_change so
		// the journal records the timeout rather than silent disappearance.
		go func() {
			t := time.NewTicker(60 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if err := presence.SweepOffline(ctx, s.db, s.journalWriter, 5*time.Minute); err != nil {
						s.logger.Warn("presence sweep failed", "err", err)
					}
				}
			}
		}()

		// Memory consolidation + compaction workers run on their own
		// schedules (6h consolidation, daily 03:00 UTC compaction).
		// Reuse the summarizer already built for the shared
		// consolidator (router path), so the background + manual runs
		// go through one LLM instance with one set of middleware.
		var summarizer consolidate.SummarizerClient
		if s.consolidator != nil {
			summarizer = s.consolidator.Summarizer
		}
		if summarizer != nil {
			s.logger.Info("memory consolidation enabled", "model", s.cfg.Keeper.Model)
		} else {
			s.logger.Info("memory consolidation disabled (set KEEPER_OLLAMA_URL + KEEPER_MODEL to enable)")
		}
		// Versioning blob root: feeds consolidate's RecordVersion call
		// on every successful appendRules / snapshotPins. Empty when
		// no MemoryRoot is configured — versioning silently disables.
		var blobRoot string
		if s.cfg.Storage.MemoryRoot != "" {
			blobRoot = filepath.Join(s.cfg.Storage.MemoryRoot, "versions")
		}
		consolidate.StartBackground(ctx, s.db, s.journalWriter, summarizer, consolidate.RunnerOptions{
			BlobRoot: blobRoot,
		})
	}

	select {
	case err := <-errCh:
		cancel()
		_ = s.Shutdown()
		return err
	case <-ctx.Done():
		return s.Shutdown()
	}
}

// Shutdown gracefully stops all server subsystems, draining connections and
// flushing logs before returning.

func (s *Server) Shutdown() error {
	s.logger.Info("shutting down servers")

	s.orchestrator.StopAccepting()
	if s.missionEngine != nil {
		s.missionEngine.Shutdown()
	}
	if s.runCancel != nil {
		s.runCancel()
	}
	// Stop background goroutines launched by New() itself (catalog
	// refresh, etc.) and wait for them to exit so any disk writes they
	// were mid-stream have settled before the process exits.
	s.StopBackground()

	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Server.ShutdownTimeout)
	defer cancel()

	var firstErr error
	if err := s.httpServer.Shutdown(ctx); err != nil {
		s.logger.Error("http server shutdown error", "error", err)
		firstErr = err
	}
	if err := s.ipcServer.Shutdown(ctx); err != nil {
		s.logger.Error("ipc server shutdown error", "error", err)
		if firstErr == nil {
			firstErr = err
		}
	}

	s.logWriter.Close()
	s.convStore.Close()
	// Detach the file-watcher's journal pointer BEFORE closing the
	// writer. Otherwise a late fsnotify event firing in the gap between
	// "Close starts draining" and "goroutine actually exits" would
	// dereference a draining/closed writer and either lose the entry or
	// (worse) panic on a closed channel.
	s.fileJournalPtr.Store(nil)
	// Close the journal writer after HTTP shutdown so any handlers still
	// draining requests have flushed their emits. Close drains the
	// buffered channel synchronously, so entries that made it in before
	// shutdown hit the DB.
	if s.journalWriter != nil {
		if err := s.journalWriter.Close(); err != nil {
			s.logger.Error("journal writer close error", "error", err)
		}
	}
	// Flush any OTel spans still buffered in the exporter before process
	// exit. Noop tracer's shutdown is a no-op so this is always safe.
	if s.telemetryShutdown != nil {
		s.telemetryShutdown()
	}
	// fileWatcher goroutines are closed via context cancellation (runCancel above);
	// explicit Close() is a no-op but signals intent.
	if s.fileWatcher != nil {
		s.fileWatcher.Close()
	}
	// Stop background goroutines owned by the API router (e.g. port-expose
	// registry's TTL purger). Done after the HTTP listener is drained so
	// no handler is still touching the registry.
	if s.apiRouter != nil {
		s.apiRouter.Shutdown()
	}

	if s.state != nil {
		if err := s.state.Close(); err != nil {
			s.logger.Error("state provider close error", "error", err)
		}
	}

	return firstErr
}

// StopBackground cancels server-owned background goroutines that were
// launched by New() (rather than Start()) — currently the devcontainer
// catalog refresh and mise runtime refresh tickers — and waits for them
// to exit. Safe to call multiple times and from any state.
//
// Production callers should prefer Shutdown(), which calls this as part
// of its sequence. Direct use is for handler-only unit tests that build
// a Server with New() but never run the full Start/Shutdown lifecycle —
// without this their async catalog HTTP fetch keeps writing to the
// test's t.TempDir() after the test returns, racing with TempDir
// cleanup and surfacing as "directory not empty" under -race -count=3.
func (s *Server) StopBackground() {
	if s.bgCancel != nil {
		s.bgCancel()
	}
	s.bgWg.Wait()
}

func (s *Server) startIPC() error {
	socketPath := s.cfg.IPC.SocketPath

	// Remove stale socket file
	_ = removeSocketFile(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", socketPath, err)
	}
	// V-12: Restrict socket permissions to owner only
	if err := os.Chmod(socketPath, 0600); err != nil {
		s.logger.Warn("failed to set socket permissions", "error", err)
	}

	s.logger.Info("starting IPC server", "socket", socketPath)
	if err := s.ipcServer.Serve(listener); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("ipc serve: %w", err)
	}
	return nil
}

// convStoreAdapter bridges conversation.Store → api.ConversationReader.

type convStoreAdapter struct {
	store *conversation.Store
}

// Read adapts conversation.Store.Read to the api.ConversationReader interface.
func (a *convStoreAdapter) Read(ctx context.Context, sessionID string, offset, limit int) ([]goapi.ConversationMessage, error) {
	msgs, err := a.store.Read(ctx, sessionID, offset, limit)
	if err != nil {
		return nil, err
	}
	out := make([]goapi.ConversationMessage, len(msgs))
	for i, m := range msgs {
		out[i] = goapi.ConversationMessage{
			Role:    string(m.Role),
			Content: m.Content,
		}
	}
	return out, nil
}

// recoverOrphanedRuns marks stale RUNNING runs as CANCELLED and resets
// agent statuses. This handles cases where the server crashed or was
// restarted while agent runs were in progress.
//
// Post Phase J of unified-journal: source of truth is the journal —
// orphaned runs are traces with a run.started entry but no terminal
// run.* entry. We emit run.cancelled for each to give them a clean
// terminal state, then reset any agent still flagged RUNNING that
// has no live run anymore.

func (s *Server) recoverOrphanedRuns(ctx context.Context) {
	if s.journalWriter == nil {
		// Without a journal writer we can't write the cancel entries —
		// but we can still reset agents to IDLE since their status is
		// stored on the agents table.
		s.logger.Debug("recover orphaned runs: no journal writer, skipping cancel entries")
	}

	type orphan struct {
		id, agentID, workspaceID string
	}
	// GROUP BY trace_id + workspace_id deduplicates the result set when
	// a retried CreateRun wrote multiple run.started entries for the
	// same logical run. Without it, recovery would emit one
	// run.cancelled per duplicate row and pollute the timeline.
	// MIN(rowid) just picks one canonical row to read agent_id off.
	var orphans []orphan
	rows, err := s.db.QueryContext(ctx, `
		SELECT je1.trace_id, MAX(je1.agent_id), je1.workspace_id
		FROM journal_entries je1
		WHERE je1.entry_type = 'run.started'
		  AND NOT EXISTS (
		    SELECT 1 FROM journal_entries je2
		    WHERE je2.workspace_id = je1.workspace_id
		      AND je2.trace_id = je1.trace_id
		      AND je2.entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout')
		  )
		GROUP BY je1.workspace_id, je1.trace_id`)
	if err != nil {
		s.logger.Error("recover orphaned runs: scan", "error", err)
		return
	}
	for rows.Next() {
		var o orphan
		if scanErr := rows.Scan(&o.id, &o.agentID, &o.workspaceID); scanErr == nil {
			orphans = append(orphans, o)
		}
	}
	_ = rows.Close()
	if len(orphans) == 0 {
		return
	}

	s.logger.Info("recovered orphaned runs", "count", len(orphans))

	// Emit run.cancelled per orphan so the Runs view shows them as
	// terminal. Severity 'notice' because this is routine recovery,
	// not an actual failure.
	if s.journalWriter != nil {
		for _, o := range orphans {
			_, _ = s.journalWriter.Emit(ctx, journal.Entry{
				WorkspaceID: o.workspaceID,
				AgentID:     o.agentID,
				Type:        journal.EntryRunCancelled,
				Severity:    journal.SeverityNotice,
				ActorType:   journal.ActorSystem,
				Summary:     "run cancelled — server restart recovery",
				Payload:     map[string]any{"reason": "server_restart"},
				TraceID:     o.id,
			})
		}
		// Flush before the agent reset SELECT so it sees the just-
		// emitted terminal entries — the writer is async and the
		// SELECT counts traces with no terminal entry.
		if err := s.journalWriter.Flush(ctx); err != nil {
			s.logger.Warn("flush journal before agent reset", "error", err)
		}
	}

	// Reset agents to IDLE if no live run remains for them. The je2
	// subquery is workspace-scoped so a terminal entry that happens to
	// share a trace_id across workspaces can't suppress this query.
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(ctx, `
		UPDATE agents SET status = 'IDLE', updated_at = ?
		WHERE status = 'RUNNING'
		AND id NOT IN (
			SELECT DISTINCT je1.agent_id
			FROM journal_entries je1
			WHERE je1.entry_type = 'run.started'
			  AND je1.agent_id IS NOT NULL
			  AND NOT EXISTS (
			    SELECT 1 FROM journal_entries je2
			    WHERE je2.workspace_id = je1.workspace_id
			      AND je2.trace_id = je1.trace_id
			      AND je2.entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout')
			  )
		)`, now); err != nil {
		s.logger.Error("reset agent statuses after recovery", "error", err)
	}
}

// startCatalogRefresh launches background tasks to refresh the devcontainer
// feature and mise runtime catalogs. The initial refresh is fired immediately
// (but decoupled from startup with a 60s timeout); subsequent refreshes run
// every 6h. Failures are logged, not fatal — the fetchers fall back to the
// disk cache / embedded data.
//
// The lifetime is bounded by parentCtx: cancelling it stops the ticker loop
// and propagates into the in-flight refresh's HTTP requests. wg lets the
// Server wait for both goroutines to exit before returning from Shutdown
// (or StopBackground in tests), so on-disk writes have settled before
// callers reclaim the storage path.

func startCatalogRefresh(parentCtx context.Context, wg *sync.WaitGroup, catalog *devcontainer.CatalogFetcher, runtimes *devcontainer.RuntimeFetcher, logger *slog.Logger) {
	refresh := func() {
		ctx, cancel := context.WithTimeout(parentCtx, 60*time.Second)
		defer cancel()
		if err := catalog.RefreshCatalog(ctx); err != nil {
			logger.Warn("devcontainer catalog refresh failed, using cached/fallback", "error", err)
		}
		if err := runtimes.RefreshRuntimes(ctx); err != nil {
			logger.Warn("mise runtime catalog refresh failed, using cached/fallback", "error", err)
		}
	}

	wg.Add(2)
	go func() {
		defer wg.Done()
		refresh()
	}()

	go func() {
		defer wg.Done()
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				refresh()
			case <-parentCtx.Done():
				return
			}
		}
	}()
}

// rehydrateContainers re-registers crew containers that survived a
// previous crewshipd process with the stats collector + file watcher.
// Stats collection and the listening-port scanner only see containers
// that have been registered via the orchestrator callback — without
// this boot-time pass, persisted containers stay invisible until
// their crew is dispatched again.
//
// Best-effort: failures to talk to Docker are logged at debug, never
// propagated. The next dispatch will register through the normal
// callback path anyway.
func (s *Server) rehydrateContainers(ctx context.Context) {
	lookup, ok := s.container.(provider.CrewContainerLookup)
	if !ok {
		// Provider does not expose existing-container lookup (e.g. apple
		// containers). Skip silently — registration will happen on next
		// dispatch via the orchestrator callback.
		return
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, workspace_id, slug
		FROM crews
		WHERE deleted_at IS NULL`)
	if err != nil {
		s.logger.Debug("rehydrate: query crews failed", "err", err)
		return
	}
	defer rows.Close()

	type crewRow struct{ id, workspaceID, slug string }
	var crews []crewRow
	for rows.Next() {
		var c crewRow
		if err := rows.Scan(&c.id, &c.workspaceID, &c.slug); err != nil {
			s.logger.Debug("rehydrate: scan crew failed", "err", err)
			continue
		}
		crews = append(crews, c)
	}
	// Catch iterator failures that happen *after* the last successful
	// Scan — without this, a connection drop mid-scan silently truncates
	// the crew list and we'd skip rehydrating the tail.
	if err := rows.Err(); err != nil {
		s.logger.Debug("rehydrate: iterate crews failed", "err", err)
	}

	registered := 0
	for _, c := range crews {
		containerID, running, err := lookup.FindCrewContainer(ctx, c.slug)
		if err != nil {
			s.logger.Debug("rehydrate: find container failed", "crew_slug", c.slug, "err", err)
			continue
		}
		if containerID == "" {
			continue
		}
		if !running {
			// Stopped container counts as known but not actively
			// streaming metrics. Skip rather than auto-start.
			continue
		}
		s.statsCollector.Register(containerID, c.id, c.workspaceID)
		s.ensureFileWatcher(c.id)
		registered++
	}
	if registered > 0 {
		s.logger.Info("rehydrated existing crew containers", "count", registered)
	}
}
