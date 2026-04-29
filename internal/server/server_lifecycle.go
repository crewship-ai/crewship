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
	"time"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/presence"
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
		consolidate.StartBackground(ctx, s.db, s.journalWriter, summarizer, consolidate.RunnerOptions{})
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

func (s *Server) recoverOrphanedRuns(ctx context.Context) {
	now := time.Now().UTC().Format(time.RFC3339)

	result, err := s.db.ExecContext(ctx, `
		UPDATE agent_runs SET status = 'CANCELLED', finished_at = ?
		WHERE status = 'RUNNING'`, now)
	if err != nil {
		s.logger.Error("recover orphaned runs", "error", err)
		return
	}
	affected, err := result.RowsAffected()
	if err != nil {
		s.logger.Warn("rows affected check failed", "error", err)
		return
	}
	if affected == 0 {
		return
	}

	s.logger.Info("recovered orphaned runs", "count", affected)

	// Reset agents that no longer have active runs to IDLE
	if _, err := s.db.ExecContext(ctx, `
		UPDATE agents SET status = 'IDLE', updated_at = ?
		WHERE status = 'RUNNING'
		AND id NOT IN (SELECT DISTINCT agent_id FROM agent_runs WHERE status = 'RUNNING')`, now); err != nil {
		s.logger.Error("reset agent statuses after recovery", "error", err)
	}
}

// startCatalogRefresh launches background tasks to refresh the devcontainer
// feature and mise runtime catalogs. The initial refresh is fired immediately
// (but decoupled from startup with a 60s timeout); subsequent refreshes run
// every 6h. Failures are logged, not fatal — the fetchers fall back to the
// disk cache / embedded data.

func startCatalogRefresh(catalog *devcontainer.CatalogFetcher, runtimes *devcontainer.RuntimeFetcher, logger *slog.Logger) {
	refresh := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := catalog.RefreshCatalog(ctx); err != nil {
			logger.Warn("devcontainer catalog refresh failed, using cached/fallback", "error", err)
		}
		if err := runtimes.RefreshRuntimes(ctx); err != nil {
			logger.Warn("mise runtime catalog refresh failed, using cached/fallback", "error", err)
		}
	}

	go refresh()

	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			refresh()
		}
	}()
}
