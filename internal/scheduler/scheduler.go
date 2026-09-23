package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/leader"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/work"
	"github.com/robfig/cron/v3"
)

// Config holds default resource limits applied to scheduled agent runs.
type Config struct {
	DefaultMemoryMB int
	DefaultCPUs     float64

	// MaxConcurrentDispatches bounds simultaneous cron acceptance attempts.
	// 0 = the package default. Execution capacity belongs to the dispatcher.
	MaxConcurrentDispatches int
}

// Scheduler registers cron occurrences and accepts durable agent work.
// The shared dispatcher owns execution after acceptance.
type Scheduler struct {
	c         *cron.Cron
	db        *sql.DB
	resolver  chatbridge.ChatResolver
	orch      *orchestrator.Orchestrator
	container provider.ContainerProvider
	logWriter *logcollector.Writer
	convStore *conversation.Store
	logger    *slog.Logger
	cfg       Config
	parser    cron.Parser // shared, immutable after construction

	// idem is retained for historical direct-executor audit tests only. New
	// scheduled occurrences use work_items' unique identity and claim fence.
	idem *pipeline.IdempotencyStore
	// nowFn is the clock used to derive the occurrence bucket. Overridable in
	// tests so a duplicate-tick / distinct-occurrence pair is deterministic;
	// defaults to time.Now.
	nowFn func() time.Time

	ctx          context.Context
	cancel       context.CancelFunc
	overdueSweep sync.WaitGroup
	// Override only in tests that exercise leader failover without 30s waits.
	overdueSweepInterval time.Duration

	// leaderGate, when non-nil, gates every fire (scheduled agents and
	// platform routines) on holding the scheduler lease, so a multi-replica
	// deploy triggers each occurrence once. Nil = single-instance (always
	// fire), the unchanged default (#1376).
	leaderGate leader.Gate

	// dispatchSem bounds simultaneous scheduled acceptance attempts. Sized in New; a nil
	// semaphore (a Scheduler built by hand) is a pass-through. See
	// dispatch_bound.go.
	dispatchSem chan struct{}

	mu       sync.Mutex
	entryMap map[string]cron.EntryID // agentID → cron entry

	// agentRunLock remains for the historical direct-executor audit tests.
	// Production runs use durable work admission plus orchestrator admission.
	agentRunLock *chatbridge.AgentRunLock

	// Production cron fires only accept work. The dispatcher is the sole
	// executor; a missing acceptance handle must never fall back to RunAgent.
	acceptor     *work.Acceptor
	diskGuard    *work.DiskGuard
	dispatchHint func(string)
}

// SetDurableAcceptance enables the one-owner scheduled-work path. Configure
// before Start; the caller owns and closes the acceptor after Stop.
func (s *Scheduler) SetDurableAcceptance(a *work.Acceptor, disk *work.DiskGuard, hint func(string)) error {
	if a == nil || disk == nil || disk.Path == "" {
		return errors.New("scheduler: durable acceptance requires an acceptor and disk guard")
	}
	s.acceptor, s.diskGuard, s.dispatchHint = a, disk, hint
	return nil
}

// SetAgentRunLock is retained for historical direct-executor tests.
func (s *Scheduler) SetAgentRunLock(l *chatbridge.AgentRunLock) {
	s.agentRunLock = l
}

// SetLeaderGate attaches a leader-election gate so this scheduler only fires
// while its replica holds the scheduler lease. Call before Start. Nil (the
// default) keeps single-instance behaviour.
func (s *Scheduler) SetLeaderGate(g leader.Gate) { s.leaderGate = g }

// isLeader reports whether this replica may fire. A nil gate (single instance)
// always may.
func (s *Scheduler) isLeader() bool {
	return s.leaderGate == nil || s.leaderGate.IsLeader()
}

// New creates a Scheduler that loads cron schedules from the database.
func New(
	db *sql.DB,
	orch *orchestrator.Orchestrator,
	container provider.ContainerProvider,
	resolver chatbridge.ChatResolver,
	logWriter *logcollector.Writer,
	convStore *conversation.Store,
	cfg Config,
	logger *slog.Logger,
) *Scheduler {
	if cfg.DefaultMemoryMB == 0 {
		cfg.DefaultMemoryMB = 4096
	}
	if cfg.DefaultCPUs == 0 {
		cfg.DefaultCPUs = 2.0
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Use a single parser for both AddFunc registration and updateTimestamps
	// re-parse. Without this, the cron.New() default parser accepts descriptor
	// expressions like "@monthly" while the explicit parser below rejects them,
	// which would let a schedule register but then trip the "unparsable cron"
	// branch in updateTimestamps and clear schedule_next_run on every refresh.
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	var idem *pipeline.IdempotencyStore
	if db != nil {
		idem = pipeline.NewIdempotencyStore(db)
	}
	s := &Scheduler{
		c:                    cron.New(cron.WithParser(parser)),
		db:                   db,
		resolver:             resolver,
		orch:                 orch,
		container:            container,
		logWriter:            logWriter,
		convStore:            convStore,
		logger:               logger,
		cfg:                  cfg,
		parser:               parser,
		idem:                 idem,
		nowFn:                time.Now,
		overdueSweepInterval: 30 * time.Second,
		ctx:                  ctx,
		cancel:               cancel,
		entryMap:             make(map[string]cron.EntryID),
	}
	s.initDispatchBound(cfg.MaxConcurrentDispatches)
	return s
}

type scheduledAgent struct {
	ID        string
	Slug      string
	Name      string
	CrewID    string
	CrewSlug  string
	Cron      string
	Prompt    string
	Workspace string
}

// Start loads all enabled schedules from the database and starts the cron engine.
func (s *Scheduler) Start(ctx context.Context) error {
	if err := s.loadSchedules(ctx); err != nil {
		return fmt.Errorf("load schedules: %w", err)
	}
	if len(s.entryMap) > 0 && (s.acceptor == nil || s.diskGuard == nil) {
		return errors.New("scheduler: enabled agent schedules require durable acceptance; refusing to start without a dispatcher-owned path")
	}
	s.c.Start()
	s.overdueSweep.Add(1)
	go func() {
		defer s.overdueSweep.Done()
		s.runOverdueSweep()
	}()
	s.logger.Info("scheduler started", "jobs", len(s.entryMap))
	return nil
}

// Stop cancels in-flight runs and waits for the cron engine to shut down.
func (s *Scheduler) Stop() {
	s.cancel() // stop in-flight acceptance and platform routines
	stopCtx := s.c.Stop()
	<-stopCtx.Done()
	s.overdueSweep.Wait()
	s.logger.Info("scheduler stopped")
}

// RegisterPlatformRoutine attaches a non-agent cron job — the cadence for
// Keeper Phase 2 sweeps (skill_review F4.1, memory_health_check F4.3) plus
// any future platform-level scheduled work. The fn runs on its own
// goroutine each time the cron fires; it MUST honour the supplied ctx
// (derived from the scheduler's lifecycle ctx) so Stop() can drain.
//
// Errors during fn are logged but never propagated — cron does not retry
// on failure, and a broken sweep should not stop subsequent sweeps. The
// caller is responsible for emitting journal entries for visibility.
//
// Returns an error only if the cron expression itself doesn't parse; a
// successful registration is permanent for the scheduler's lifetime
// (there's no platform-routine unregister surface today — these are wired
// once at boot from server bootstrap).
//
// `name` is logged on every fire for grep-ability; pass the
// routines.RoutineKind string ("skill_review", "memory_health_check").
func (s *Scheduler) RegisterPlatformRoutine(name, cronExpr string, fn func(ctx context.Context)) error {
	if fn == nil {
		return fmt.Errorf("scheduler: RegisterPlatformRoutine requires non-nil fn")
	}
	if _, err := s.parser.Parse(cronExpr); err != nil {
		return fmt.Errorf("scheduler: invalid cron %q for platform routine %q: %w", cronExpr, name, err)
	}
	_, err := s.c.AddFunc(cronExpr, func() {
		// robfig/cron/v3 does NOT recover panics by default (unlike v2):
		// any panic inside fn would crash the entire process, taking
		// down the API server with it and breaking the "future sweeps
		// continue" guarantee. Wrap each fire so a faulty routine
		// degrades to a logged error rather than a hard kill.
		defer func() {
			if r := recover(); r != nil {
				s.logger.Error("platform routine panicked",
					"name", name, "cron", cronExpr, "panic", r)
			}
		}()
		// Per-fire ctx tied to the scheduler lifecycle so Stop() cancels
		// in-flight sweeps. 30-minute hard cap matches the upper bound
		// for a full skill-catalog sweep on an instance with thousands
		// of skills (each evaluator call ~5s × Haiku latency, but we
		// stream serially to keep the LLM token budget bounded —
		// parallelism would burn cost without a clear win for a daily
		// audit-not-gate workload).
		//
		// Leader gate: platform sweeps (Keeper Phase 2) run once per cluster,
		// not once per replica. Nil gate (single instance) always passes.
		if !s.isLeader() {
			return
		}
		jobCtx, cancel := context.WithTimeout(s.ctx, 30*time.Minute)
		defer cancel()
		s.logger.Info("platform routine fired", "name", name, "cron", cronExpr)
		fn(jobCtx)
	})
	if err != nil {
		return fmt.Errorf("scheduler: register platform routine %q: %w", name, err)
	}
	s.logger.Info("platform routine registered", "name", name, "cron", cronExpr)
	return nil
}

func (s *Scheduler) loadSchedules(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, a.slug, a.name, COALESCE(a.crew_id, ''), COALESCE(c.slug, ''),
		       a.schedule_cron, COALESCE(a.schedule_prompt, ''), a.workspace_id
		FROM agents a
		LEFT JOIN crews c ON c.id = a.crew_id
		WHERE a.schedule_enabled = 1 AND a.schedule_cron IS NOT NULL
		      AND a.schedule_cron != '' AND a.deleted_at IS NULL`)
	if err != nil {
		return fmt.Errorf("query scheduled agents: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var ag scheduledAgent
		if err := rows.Scan(&ag.ID, &ag.Slug, &ag.Name, &ag.CrewID, &ag.CrewSlug,
			&ag.Cron, &ag.Prompt, &ag.Workspace); err != nil {
			s.logger.Error("scan scheduled agent", "error", err)
			continue
		}
		if err := s.addEntry(ag); err != nil {
			s.logger.Error("register cron job", "agent", ag.Slug, "cron", ag.Cron, "error", err)
			continue
		}
		s.logger.Info("registered schedule", "agent", ag.Slug, "cron", ag.Cron)
	}
	return rows.Err()
}

func (s *Scheduler) addEntry(ag scheduledAgent) error {
	agCopy := ag
	entryID, err := s.c.AddFunc(ag.Cron, func() {
		s.acceptScheduled(agCopy)
	})
	if err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", ag.Cron, err)
	}
	s.mu.Lock()
	s.entryMap[ag.ID] = entryID
	s.mu.Unlock()
	return nil
}

// UpdateSchedule is called from the API when an agent's schedule changes.
func (s *Scheduler) UpdateSchedule(ctx context.Context, agentID, cronExpr, prompt string, enabled bool) error {
	if !enabled || cronExpr == "" {
		s.mu.Lock()
		if oldID, ok := s.entryMap[agentID]; ok {
			s.c.Remove(oldID)
			delete(s.entryMap, agentID)
		}
		s.mu.Unlock()
		s.logger.Info("schedule removed", "agent_id", agentID)
		return nil
	}

	// Load agent info from DB before touching the cron engine so the old entry
	// remains active if the DB call fails.
	var ag scheduledAgent
	err := s.db.QueryRowContext(ctx, `
		SELECT a.id, a.slug, a.name, COALESCE(a.crew_id, ''), COALESCE(c.slug, ''), a.workspace_id
		FROM agents a LEFT JOIN crews c ON c.id = a.crew_id
		WHERE a.id = ?`, agentID).Scan(&ag.ID, &ag.Slug, &ag.Name, &ag.CrewID, &ag.CrewSlug, &ag.Workspace)
	if err != nil {
		return fmt.Errorf("load agent %s: %w", agentID, err)
	}
	ag.Cron = cronExpr
	ag.Prompt = prompt

	// Register new entry first; only remove old one on success.
	newEntryID, err := s.c.AddFunc(ag.Cron, func() {
		s.acceptScheduled(ag)
	})
	if err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", ag.Cron, err)
	}

	s.mu.Lock()
	if oldID, ok := s.entryMap[agentID]; ok {
		s.c.Remove(oldID)
	}
	s.entryMap[agentID] = newEntryID
	s.mu.Unlock()

	s.logger.Info("schedule updated", "agent", ag.Slug, "cron", cronExpr)

	// The API committed the due cursor with schedule_enabled/cron before this
	// callback. Writing it here could overwrite an occurrence already advanced
	// by AcceptDue while this callback was being registered.
	return nil
}
