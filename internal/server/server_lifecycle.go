package server

// Server runtime: Start / Shutdown drive the process lifecycle, plus
// the side helpers they call (IPC listener, conversation-store adapter
// for the goapi router, orphaned-run recovery, devcontainer catalog
// refresh). Extracted from server.go so the constructor wiring stays
// readable in one file and the runtime path in another.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/ephemeral"
	"github.com/crewship-ai/crewship/internal/episodic"
	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/presence"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/scrubber"
	"github.com/crewship-ai/crewship/internal/ws"
)

// Stuck-QUEUED assignment sweeper boot defaults. The sweeper
// (goapi.AssignmentHandler.StartStuckQueueSweeper) is the crash-recovery
// net for the assignment queue: QUEUED rows are normally drained by the
// completion-path pump, but a crash between "row set QUEUED" and "next
// completion fires" strands them forever — nothing re-pumps after a
// restart. The boot-time sweeper catches exactly that.
//
//   - stuckQueueSweepInterval (60s): how often the sweeper scans. Cheap
//     query (partial index on assignments(status, queued_at)), so a
//     minute-grain scan costs nothing on idle systems while keeping
//     post-crash recovery latency operator-friendly.
//   - stuckQueueStaleAfter (10min): how long a row must sit QUEUED
//     before it counts as stuck. Generously above any healthy pump
//     cadence (completions fire every few seconds under load), so the
//     sweeper never races a live pump — it only ever sees rows whose
//     pump path is genuinely gone.
//
// Declared as vars (not consts) so the boot-wiring integration test can
// shrink them to milliseconds; production code never mutates them.
var (
	stuckQueueSweepInterval = 60 * time.Second
	stuckQueueStaleAfter    = 10 * time.Minute
)

// Stuck-RUNNING assignment sweeper boot defaults — the in-process
// companion of the boot-time RecoverInterruptedRunning call below.
// RUNNING rows are backed by real agent executions, so the staleness
// value here is a deliberately generous FLOOR: the sweeper's actual
// per-row bound is max(the target agent's configured timeout_seconds
// plus a grace margin, this floor) — see goapi's
// defaultRunningStaleAfter for the full rationale. It only exists to
// reclaim crew concurrency slots leaked without a restart (e.g. a
// dispatch goroutine that died between claiming the slot and running
// the agent).
//
// Same var-not-const convention as the queued sweeper, for tests.
var (
	stuckRunningSweepInterval = 5 * time.Minute
	stuckRunningStaleAfter    = 2 * time.Hour
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

	// Stuck-QUEUED assignment sweeper: crash-recovery net for the
	// assignment queue (see the stuckQueueSweep* vars above for the
	// cadence rationale). Runs on the same AssignmentHandler instance
	// the HTTP routes use; the goroutine exits when ctx is cancelled
	// at shutdown. nil-guarded because the API router (and with it the
	// handler) only exists when a DB was wired — headless/dry-run
	// boots have no queue to sweep.
	if s.apiRouter != nil {
		if assign := s.apiRouter.Assignments(); assign != nil {
			// Boot-time recovery for RUNNING assignments orphaned by a
			// previous crash/restart. Dispatch goroutines are process-
			// local, so a RUNNING row stamped before s.startedAt cannot
			// have a live driver — without this it would hold its crew
			// concurrency slot forever (claimCrewSlot counts RUNNING
			// rows against the budget) and the delegating lead's chat
			// would never resolve. Companion of recoverOrphanedRuns
			// (which cleans the journal/agents side); this one owns the
			// assignments table + the completion signals. Runs after
			// wsHub.Run so the assignment_failed broadcasts recovery
			// emits are drained instead of piling into the hub buffer,
			// and before ReattachInProgressMissions so the mission
			// engine re-attaches against settled assignment state.
			if n, err := assign.RecoverInterruptedRunning(ctx, s.startedAt); err != nil {
				s.logger.Error("recover interrupted RUNNING assignments", "error", err)
			} else if n > 0 {
				s.logger.Info("recovered interrupted RUNNING assignments", "count", n)
			}

			assign.StartStuckQueueSweeper(ctx, stuckQueueSweepInterval, stuckQueueStaleAfter)
			s.logger.Info("stuck-queue sweeper started",
				"interval", stuckQueueSweepInterval.String(),
				"stale_after", stuckQueueStaleAfter.String())

			// Belt-and-braces ticker for RUNNING slots leaked while the
			// process stays up (no restart to trigger the recovery
			// above). Generous staleness bound — see the vars' comment.
			assign.StartStuckRunningSweeper(ctx, stuckRunningSweepInterval, stuckRunningStaleAfter)
			s.logger.Info("stuck-running sweeper started",
				"interval", stuckRunningSweepInterval.String(),
				"stale_after", stuckRunningStaleAfter.String())
		}
		// Async webhook dispatches (FireWebhook 202-then-run) derive
		// their run context from the server lifecycle, not the HTTP
		// request — a sender hanging up must not cancel an in-flight
		// run, but shutdown (runCancel) must still stop it.
		if pipes := s.apiRouter.PipelinesHandler; pipes != nil {
			pipes.SetLifecycleContext(ctx)
		}
	}

	// Re-attach mission orchestration loops lost in a previous crewshipd
	// run. runMissionLoop is in-memory and only ever spawned by API
	// handlers, so after a restart every IN_PROGRESS mission would sit
	// driverless forever without this scan. Runs after orphaned-run
	// recovery (above) so stale RUNNING state is already cleaned, and on
	// the cancellable run ctx so Shutdown stops the re-attached loops the
	// same way it stops handler-started ones.
	if s.db != nil && s.missionEngine != nil {
		if n := s.missionEngine.ReattachInProgressMissions(ctx); n > 0 {
			s.logger.Info("re-attached in-progress missions after restart", "count", n)
		}
	}

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

	// Episodic indexer sweeper (W2, release-1.0 hardening): embeds
	// high-value journal entries into journal_embeddings so HybridRecall
	// has a vector index to query. Needs only the DB + an embedder; when
	// no embedder is configured the helper logs the sparse-only WARN and
	// /healthz reports the degraded mode instead of recall silently
	// returning "".
	if s.db != nil {
		s.startEpisodicIndexer(ctx)
	}

	// Attachment blob collector (#1768 item 7): every hour, unlink blobs
	// under <storage>/attachments/ that no row names. It is the ONLY
	// caller of the sweep, and the only thing that ever collects blobs
	// whose rows SQLite removed by FK cascade — a crew wipe, a workspace
	// wipe, a comment cascade — none of which run any Go. Without it
	// those bytes are permanent: unreachable, unaccounted for, and not
	// removable through any API. See internal/api/attachments_gc.go for
	// what it collects and what it deliberately does not.
	if s.db != nil {
		goapi.StartAttachmentBlobGC(ctx, s.db, s.logger, s.cfg.Storage.BasePath, 0)
	}

	// Crew Journal background workers. Each is a small goroutine that
	// only runs when s.db and the journal writer are live — early init
	// paths that come up without DB (tests, --dry-run) skip silently.
	if s.db != nil && s.journalWriter != nil {
		// Harbor Master timeout sweeper: every 30s, flip past-due pending
		// approvals to 'timeout' status so blocked agents unstick
		// deterministically even if the UI is down.
		go harbormaster.StartTimeoutSweeper(ctx, s.db, s.journalWriter, 30*time.Second)

		// PR-D F5 ephemeral-agent expiry sweeper: every 5 min, flip
		// expired_at on rows whose TTL has elapsed so they enter
		// "ghost" state. DB row stays — only runtime is recycled by
		// the container provider's own GC. Broadcaster surfaces
		// agent.expired over the WS hub so the UI grays the card
		// immediately instead of waiting for the next list poll.
		ephemeralBcast := ephemeralHubAdapter{hub: s.wsHub}
		ephemeral.StartExpirySweeper(ctx, s.db, s.journalWriter, ephemeralBcast, ephemeral.DefaultSweepInterval, s.logger)

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
		// StorageBasePath is the host root the container provider
		// bind-mounts per crew; the runner resolves each crew's
		// learned-*.md / pins.md directory under it. Leaving it unset
		// used to fall back to the container-absolute
		// "/crew/shared/.memory", so this host process wrote the crew's
		// consolidated memory at the host filesystem root where no
		// container could read it (#1663).
		consolidate.StartBackground(ctx, s.db, s.journalWriter, summarizer, consolidate.RunnerOptions{
			BlobRoot:        blobRoot,
			StorageBasePath: s.cfg.Storage.BasePath,
		})

		// Memory audit watcher: catches direct filesystem writes by
		// agents that bypass the sidecar /memory/write IPC (e.g.
		// Claude Code's Write tool writes to ~/.memory/daily/*.md
		// without curl-ing the sidecar). Without this, the journal,
		// memory_versions, and downstream HITL flow see zero events
		// for ~75% of real-world writes observed on dev1. The
		// watcher reads each changed file, runs the scrubber for
		// PII surfacing, dedups against recent sidecar-recorded
		// rows, and emits memory.updated so the audit trail holds
		// regardless of which path the agent took.
		//
		// fsnotify init can fail on hosts where the kernel doesn't
		// support inotify (Docker-for-Mac bind-mounts, exotic FS)
		// — the helper logs at warn and the server boots normally;
		// the audit is best-effort observability, not a hard gate.
		// Scrubber instance dedicated to the audit watcher — a
		// fresh one rather than sharing with the sidecar because
		// the sidecar runs in-container and we're on the host.
		// Cheap to construct; one per server is the right grain.
		memory.StartAuditWatcher(ctx, s.db, s.journalWriter, memory.AuditWatcherConfig{
			BasePath: s.cfg.Storage.BasePath,
			BlobRoot: blobRoot,
			Scrubber: scrubber.New(),
		}, s.logger)
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

// episodicIndexerPoll is the sleep between indexer sweeps for new
// embeddable journal entries. 30s matches the episodic package default:
// shorter hammers the Ollama embedder during active missions, longer
// widens the recall blind window between an event happening and the
// entry becoming vector-searchable. Each sweep processes up to 64
// entries, so a backlog drains at ~2 entries/second worst case.
//
// var (not const) so the boot-wiring test can shrink it: the initial
// sweep can lose a SQLite busy race against the other boot goroutines,
// and a test waiting out the full production interval for the retry
// would be either flaky or 30s slow. Production never mutates it.
var episodicIndexerPoll = 30 * time.Second

// episodicProbeTimeout bounds the one boot-time embed probe. Short on
// purpose: it must not hold up anything, and "did not answer in 10s" is
// itself the degraded answer.
var episodicProbeTimeout = 10 * time.Second

// startEpisodicIndexer launches the episodic indexer sweeper when an
// embedder is configured, or logs the degraded-mode WARN when it isn't.
// Called from Start() with the run context so the sweeper goroutine
// stops on server shutdown. Requires s.db (caller guards).
//
// The sparse-only branch is deliberately loud: before W2 the indexer was
// never constructed in production, HybridRecall queried an empty index,
// and recall silently returned "" with no operator-visible signal. The
// same vector/sparse-only state is exposed on /healthz (see
// handleHealthz) and surfaced by `crewship doctor`.
func (s *Server) startEpisodicIndexer(ctx context.Context) {
	if s.episodicEmbedder == nil {
		s.logger.Warn("episodic recall running in sparse-only mode — no embedder configured; " +
			"set KEEPER_OLLAMA_URL to an Ollama host serving nomic-embed-text to enable vector recall")
		return
	}
	idx := episodic.NewIndexer(s.db, s.episodicEmbedder, s.logger, episodicIndexerPoll)
	go idx.Start(ctx)
	s.logger.Info("episodic indexer started",
		"model", s.episodicEmbedder.Model(),
		"poll", episodicIndexerPoll.String())

	// Probe once, off the boot path. Without this, a misconfigured
	// embedder is only discovered when something real needs embedding —
	// and if the journal has nothing embeddable yet, /healthz reports
	// vector recall on an embedder nobody has ever called. One cheap call
	// makes the stage failure (Ollama up, model absent) visible at boot
	// instead of silently at the first sweep that finds work.
	go s.probeEpisodicEmbedder(ctx)
}

// probeEpisodicEmbedder makes one embed call so episodicMode() has
// evidence to report. It changes nothing on failure beyond the health
// surfaces and one WARN: a probe failure is not fatal, recall degrades to
// BM25 and the server is still useful.
func (s *Server) probeEpisodicEmbedder(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, episodicProbeTimeout)
	defer cancel()

	if _, err := s.episodicEmbedder.Embed(ctx, "crewship episodic embedder probe"); err != nil {
		s.logger.Warn("episodic: embedder is configured but not working — "+
			"vector recall is DEGRADED and the index will not grow; "+
			"check the Ollama host is serving this model",
			"model", s.episodicEmbedder.Model(),
			"err", err)
		return
	}
	s.logger.Info("episodic: embedder probe ok", "model", s.episodicEmbedder.Model())
}

// episodicMode reports the recall mode for health surfaces:
//
//	vector           embedder wired and its calls are working — indexer
//	                 running, HybridRecall serves vec+BM25
//	vector-degraded  embedder wired but its last call FAILED — the index
//	                 is not growing and recall is silently BM25-only
//	sparse-only      no embedder configured at all
//
// The middle state used to be indistinguishable from the first. This
// answered "vector" on `episodicEmbedder != nil`, which server.go sets as
// soon as Keeper's Ollama URL is present, without ever calling it. On the
// stage slot on 2026-08-07 that Ollama was up but had no nomic-embed-text
// model: 4032 consecutive index failures in a day, an empty vector index,
// and this reporting healthy vector recall throughout. `crewship doctor`
// reads the same field, so it repeated the claim.
//
// An embedder that has not been called yet still reads "vector" — a fresh
// boot with an empty journal never calls Embed, and flagging a fault on no
// evidence is its own false alarm. startEpisodicIndexer probes once at
// boot so the missing-model case does not have to wait for real traffic.
func (s *Server) episodicMode() string {
	if s.episodicEmbedder == nil {
		return "sparse-only"
	}
	if obs, ok := s.episodicEmbedder.(*episodic.ObservedEmbedder); ok && obs.Degraded() {
		return "vector-degraded"
	}
	return "vector"
}

// episodicDetail returns the error behind a "vector-degraded" mode, or ""
// when there is nothing wrong. A bare mode string sends the operator
// looking in the wrong place: a missing model, an unreachable host and a
// timeout all present identically without it.
func (s *Server) episodicDetail() string {
	obs, ok := s.episodicEmbedder.(*episodic.ObservedEmbedder)
	if !ok {
		return ""
	}
	if err := obs.LastError(); err != nil {
		return scrubURLs(err.Error())
	}
	return ""
}

// embedderURLPattern matches a scheme://... token up to the first quote,
// whitespace or comma. Go's http.Client returns a *url.Error whose text
// embeds the FULL request URL, so an unreachable Ollama stringifies as
// `Post "http://user:pass@10.0.5.12:11434/api/embeddings": dial tcp …`.
var embedderURLPattern = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^"\s,]+`)

// embedderHostPortPattern catches the address a SECOND time. A dial
// failure repeats it outside the URL — `Post "http://10.0.5.12:11434/…":
// dial tcp 10.0.5.12:11434: connect: connection refused` — so scrubbing
// only the URL still publishes the host. Requires 2-5 digits after the
// colon, which is why `http 404: {…}` and `"error":"model …"` survive.
var embedderHostPortPattern = regexp.MustCompile(`\b[a-zA-Z0-9][a-zA-Z0-9.-]*:\d{2,5}\b`)

// scrubURLs removes URLs from text bound for /healthz.
//
// /healthz is registered on s.mux with NO auth middleware (routes.go), so
// anything episodicDetail() returns is readable by anyone who can reach
// the port. Publishing the raw error would hand out the internal host and
// port from KEEPER_OLLAMA_URL — and its userinfo, if it carries any.
//
// telemetry.RedactURL is the wrong tool here: it strips credentials but
// keeps host:port, which is exactly what an unauthenticated endpoint must
// not advertise. Everything around the URL is kept, because that is the
// half an operator acts on: "connection refused" says reachability,
// `model "nomic-embed-text" not found` says pull the model, and neither
// needs an address to be useful.
func scrubURLs(s string) string {
	s = embedderURLPattern.ReplaceAllString(s, "<redacted-url>")
	return embedderHostPortPattern.ReplaceAllString(s, "<redacted-addr>")
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

	// Drain async webhook run goroutines BEFORE the journal writer
	// closes so their terminal run entries still land. runCancel()
	// above already cancelled their run context, so this returns as
	// soon as in-flight executors observe the cancel and record
	// their terminal state.
	if s.apiRouter != nil {
		if pipes := s.apiRouter.PipelinesHandler; pipes != nil {
			pipes.WaitWebhookDispatches()
		}
		// Drain in-flight post-run outcome-verdict goroutines (#1403) —
		// both ad-hoc agent runs (InternalHandler) and routine runs
		// (PipelineHandler) — while the journal writer is still open, so a
		// verdict caught mid-generation still records its entry. Bounded so
		// a wedged LLM call can't hang shutdown past the configured timeout.
		s.apiRouter.DrainVerdicts(s.cfg.Server.ShutdownTimeout)
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
	// Drain pprof in-flight requests (a curl pulling /debug/pprof/profile
	// can be blocking for 30s by default). Noop when CREWSHIP_PPROF_ADDR
	// was unset at startup.
	if s.pprofShutdown != nil {
		s.pprofShutdown()
	}
	// Flush any pyroscope-go push batches still in flight. Noop when
	// CREWSHIP_PYROSCOPE_URL was unset.
	if s.pyroscopeShutdown != nil {
		s.pyroscopeShutdown()
	}
	// fileWatcher goroutines exit on context cancellation (runCancel above),
	// and Close() blocks until they have — so it has to come after runCancel
	// or it just burns its 5s timeout. Draining matters because the crew
	// output tree may be deleted right after shutdown, and fsnotify must be
	// done releasing its descriptors first (#1286). Close is terminal: no new
	// watches are accepted afterwards.
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

// ReadTail adapts conversation.Store.ReadTail to the api.ConversationReader
// interface (#1041 — keeper reads only the recent window, not the whole file).
func (a *convStoreAdapter) ReadTail(ctx context.Context, sessionID string, maxMessages int) ([]goapi.ConversationMessage, error) {
	msgs, err := a.store.ReadTail(ctx, sessionID, maxMessages)
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

// SearchConversations adapts conversation.Store.Search to the
// api.ConversationSearcher interface so POST /api/v1/conversations/search
// can run the agent-scoped BM25 query against the v111 FTS5 mirror.
func (a *convStoreAdapter) SearchConversations(ctx context.Context, agentID, query string, limit int) ([]goapi.ConversationSearchHit, error) {
	hits, err := a.store.Search(ctx, agentID, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]goapi.ConversationSearchHit, len(hits))
	for i, h := range hits {
		out[i] = goapi.ConversationSearchHit{
			ID:          h.ID,
			SessionID:   h.SessionID,
			AgentID:     h.AgentID,
			Role:        string(h.Role),
			Content:     h.Content,
			ToolSummary: h.ToolSummary,
			Timestamp:   h.Timestamp.UTC().Format(time.RFC3339Nano),
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

// interruptedChatMessage is the user-facing copy appended to a chat
// whose in-flight reply was killed by a hard server stop (SIGKILL/OOM/
// power loss). Matches the actionable one-liner style of the
// chatbridge's error copy.
const interruptedChatMessage = "The agent's reply was interrupted by a server restart — try again"

func (s *Server) recoverOrphanedRuns(ctx context.Context) {
	if s.journalWriter == nil {
		// Without a journal writer we can't write the cancel entries —
		// but we can still reset agents to IDLE since their status is
		// stored on the agents table.
		s.logger.Debug("recover orphaned runs: no journal writer, skipping cancel entries")
	}

	type orphan struct {
		id, agentID, workspaceID string
		// chatID is the chats row the run was replying into, extracted
		// from the run.started payload. Empty for non-chat runs (routine
		// dispatch, assignments) — those get journal cleanup only.
		chatID string
	}
	// GROUP BY trace_id + workspace_id deduplicates the result set when
	// a retried CreateRun wrote multiple run.started entries for the
	// same logical run. Without it, recovery would emit one
	// run.cancelled per duplicate row and pollute the timeline.
	// MIN(rowid) just picks one canonical row to read agent_id off.
	var orphans []orphan
	rows, err := s.db.QueryContext(ctx, `
		SELECT je1.trace_id, MAX(je1.agent_id), je1.workspace_id,
		       MAX(COALESCE(json_extract(je1.payload, '$.chat_id'), ''))
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
		if scanErr := rows.Scan(&o.id, &o.agentID, &o.workspaceID, &o.chatID); scanErr == nil {
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

		// Surface the interruption to the CHAT each run was replying
		// into. Journal/agent cleanup alone leaves the user's message
		// with no assistant turn and no explanation — total silence.
		// Append an explicit system/error turn (same store + parts
		// shape the chatbridge persists, so history reloads render it
		// as an error bubble), bump the chat's message count, and
		// broadcast it on the session channel for anything already
		// subscribed. Scoped inside the journalWriter branch on
		// purpose: the notify must only happen when the trace was
		// actually terminalized above, otherwise the still-open trace
		// would be re-detected — and re-announced — on every boot.
		//
		// Resumable-stream note: this frame goes through Hub.Broadcast,
		// NOT through the per-run replay buffer (streams.begin/record),
		// so it carries no seq and clients apply it immediately — it
		// can't perturb run_begin/resume reassembly for later runs.
		for _, o := range orphans {
			if o.chatID == "" || s.convStore == nil {
				continue
			}
			if err := s.convStore.Append(ctx, o.chatID, conversation.Message{
				ID:        fmt.Sprintf("msg_recovery_%s", o.id),
				AgentID:   o.agentID,
				Role:      conversation.RoleSystem,
				Content:   interruptedChatMessage,
				Parts:     []conversation.Part{{Type: "error", Content: interruptedChatMessage}},
				Timestamp: time.Now().UTC(),
			}); err != nil {
				s.logger.Warn("recover orphaned runs: persist interrupted-reply turn",
					"chat_id", o.chatID, "run_id", o.id, "error", err)
				continue
			}
			if _, err := s.db.ExecContext(ctx,
				`UPDATE chats SET message_count = message_count + 1 WHERE id = ?`,
				o.chatID); err != nil {
				s.logger.Warn("recover orphaned runs: bump message count",
					"chat_id", o.chatID, "error", err)
			}
			if s.broadcastSessionEvent != nil {
				s.broadcastSessionEvent(o.chatID, ws.ChatEvent{
					Type:     "error",
					Content:  interruptedChatMessage,
					Metadata: map[string]any{"reason": "server_restart", "run_id": o.id},
				})
			}
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
		containerID, running, err := lookup.FindCrewContainer(ctx, c.id, c.slug)
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
		// #1662: rehydration used to register the stats collector and stop
		// there, so a container that survived a crewshipd restart was known
		// to the metrics tile and invisible to the reaper — if it was never
		// woken again it was never stopped, ever. dev1 had one that had been
		// running five days with zero agent runs.
		//
		// The clock is dated from the container's own StartedAt rather than
		// from now. Seeding it with now would hand every restart a fresh full
		// TTL window, and on a host that redeploys more often than the TTL
		// (dev1 tracks main) nothing would ever be reaped — the bug would
		// survive its own fix.
		s.seedCrewReaperClock(ctx, c.id, c.workspaceID, containerID)
		registered++
	}
	if registered > 0 {
		s.logger.Info("rehydrated existing crew containers", "count", registered)
	}
}

// seedCrewReaperClock hands a boot-discovered container to the idle reaper
// with its idle clock dated from the container's own start (#1662).
//
// ContainerStatus.Uptime already carries inspect.State.StartedAt verbatim, so
// no provider change is needed for this. A timestamp we cannot parse falls
// back to now — that is the pre-#1662 behaviour for one TTL window, not a
// missed stop forever.
func (s *Server) seedCrewReaperClock(ctx context.Context, crewID, workspaceID, containerID string) {
	if s.orchestrator == nil || crewID == "" || containerID == "" {
		return
	}
	var storedTTL *int
	var col sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT container_ttl_hours FROM crews WHERE id = ? AND workspace_id = ?`,
		crewID, workspaceID).Scan(&col)
	if err != nil {
		s.logger.Debug("rehydrate: read crew ttl failed", "crew_id", crewID, "err", err)
		return
	}
	if col.Valid {
		v := int(col.Int64)
		storedTTL = &v
	}

	startedAt := time.Now()
	if st, err := s.container.ContainerStatus(ctx, containerID); err == nil && st != nil && st.Uptime != "" {
		// The nearby QueryContext belongs to loadCrewTTLHours, which selects an
		// integer hour count and touches no timestamp — this parse feeds the
		// reaper's in-memory idle clock and never reaches SQL.
		if parsed, perr := time.Parse(time.RFC3339Nano, st.Uptime); perr == nil { // tsformat:allow: read-only parse into a time.Time, never compared or ordered in SQL
			startedAt = parsed
		} else {
			s.logger.Debug("rehydrate: unparseable container start time",
				"container_id", containerID, "value", st.Uptime, "err", perr)
		}
	}

	s.orchestrator.SeedCrewActivity(crewID, containerID,
		goapi.ResolveCrewContainerTTLHours(storedTTL), startedAt)
}

// loadCrewTTLHours reads every live crew's effective container TTL, in hours,
// keyed by crew id. This is the reaper's authority (#1662); a crew missing
// from the returned map is never reaped, so a read failure fails safe by
// returning nil rather than an empty-but-authoritative map.
func loadCrewTTLHours(ctx context.Context, db *sql.DB, logger *slog.Logger) map[string]int {
	rows, err := db.QueryContext(ctx,
		`SELECT id, container_ttl_hours FROM crews WHERE deleted_at IS NULL`)
	if err != nil {
		logger.Debug("crew ttl resolver: query failed", "err", err)
		return nil
	}
	defer rows.Close()

	out := make(map[string]int)
	for rows.Next() {
		var id string
		var col sql.NullInt64
		if err := rows.Scan(&id, &col); err != nil {
			logger.Debug("crew ttl resolver: scan failed", "err", err)
			continue
		}
		var stored *int
		if col.Valid {
			v := int(col.Int64)
			stored = &v
		}
		out[id] = goapi.ResolveCrewContainerTTLHours(stored)
	}
	if err := rows.Err(); err != nil {
		// A truncated list would silently exempt the tail from reaping. Say
		// nothing rather than say something wrong.
		logger.Debug("crew ttl resolver: iterate failed", "err", err)
		return nil
	}
	return out
}

// ephemeralHubAdapter satisfies internal/ephemeral.Broadcaster. The
// sweeper depends on a tiny interface so the package doesn't import
// internal/ws (which would drag the WS dependency into a pure DB
// sweeper); the adapter lives in the server package where ws is
// already wired.
type ephemeralHubAdapter struct {
	hub *ws.Hub
}

// BroadcastWorkspaceEvent forwards to ws.Hub.BroadcastWorkspace. Nil
// hub is a no-op so the sweeper still runs in test/headless harnesses
// where the WS layer isn't constructed.
func (e ephemeralHubAdapter) BroadcastWorkspaceEvent(wsID, eventType string, payload map[string]string) {
	if e.hub == nil {
		return
	}
	e.hub.BroadcastWorkspace(wsID, eventType, payload)
}
