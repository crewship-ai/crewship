package scheduler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/robfig/cron/v3"
)

// Config holds default resource limits applied to scheduled agent runs.
type Config struct {
	DefaultMemoryMB int
	DefaultCPUs     float64
}

// Scheduler runs agents on cron schedules, managing cron entries and
// triggering agent runs through the orchestrator.
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

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	entryMap map[string]cron.EntryID // agentID → cron entry
}

// New creates a Scheduler that loads cron schedules from the database
// and triggers agent runs via the orchestrator.
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
	return &Scheduler{
		c:         cron.New(cron.WithParser(parser)),
		db:        db,
		resolver:  resolver,
		orch:      orch,
		container: container,
		logWriter: logWriter,
		convStore: convStore,
		logger:    logger,
		cfg:       cfg,
		parser:    parser,
		ctx:       ctx,
		cancel:    cancel,
		entryMap:  make(map[string]cron.EntryID),
	}
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
	s.c.Start()
	s.logger.Info("scheduler started", "jobs", len(s.entryMap))
	return nil
}

// Stop cancels in-flight runs and waits for the cron engine to shut down.
func (s *Scheduler) Stop() {
	s.cancel() // signal in-flight triggerAgent goroutines to stop
	stopCtx := s.c.Stop()
	<-stopCtx.Done()
	s.logger.Info("scheduler stopped")
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
		s.triggerAgent(agCopy)
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
		s.triggerAgent(ag)
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

	// Update next_run in DB
	if sched, err := s.parser.Parse(cronExpr); err == nil {
		next := sched.Next(time.Now())
		if _, err := s.db.ExecContext(ctx, "UPDATE agents SET schedule_next_run = ? WHERE id = ?",
			next.UTC().Format(time.RFC3339), agentID); err != nil {
			s.logger.Warn("update schedule_next_run", "agent_id", agentID, "error", err)
		}
	}
	return nil
}

func (s *Scheduler) triggerAgent(ag scheduledAgent) {
	ctx, cancel := context.WithTimeout(s.ctx, 45*time.Minute)
	defer cancel()

	s.logger.Info("scheduled trigger", "agent", ag.Slug, "crew", ag.CrewSlug)

	chatID := generateID()
	runID := generateID()
	prompt := ag.Prompt
	if prompt == "" {
		prompt = "This is a scheduled run. Execute your primary tasks."
	}

	// 1. Create chat
	if err := s.resolver.CreateChat(ctx, chatbridge.CreateChatRequest{
		ChatID:      chatID,
		AgentID:     ag.ID,
		WorkspaceID: ag.Workspace,
		Title:       fmt.Sprintf("Scheduled: %s", ag.Name),
	}); err != nil {
		s.logger.Error("scheduled: create chat failed", "agent", ag.Slug, "error", err)
		s.updateTimestamps(ag.ID, ag.Cron, true)
		return
	}

	// 2. Resolve chat → full ChatInfo (credentials, system prompt, skills, etc.)
	info, err := s.resolver.ResolveChat(ctx, chatID)
	if err != nil {
		s.logger.Error("scheduled: resolve chat failed", "agent", ag.Slug, "error", err)
		s.updateTimestamps(ag.ID, ag.Cron, true)
		return
	}

	// 3. Ensure container is running
	var containerID string
	if s.container != nil {
		crewID := info.CrewID
		crewSlug := info.CrewSlug
		if crewID == "" {
			crewID = "scheduler-" + ag.Workspace
			crewSlug = "scheduler"
		}
		cID, err := s.container.EnsureCrewRuntime(ctx, provider.CrewConfig{
			ID:          crewID,
			Slug:        crewSlug,
			MemoryMB:    s.cfg.DefaultMemoryMB,
			CPUs:        s.cfg.DefaultCPUs,
			Image:       info.RuntimeImage,
			CachedImage: info.CachedImage,
		})
		if err != nil {
			s.logger.Error("scheduled: container failed", "agent", ag.Slug, "error", err)
			s.updateTimestamps(ag.ID, ag.Cron, true)
			return
		}
		containerID = cID
	}

	// 4. Persist user message to conversation store
	if s.convStore != nil {
		_ = s.convStore.Append(ctx, chatID, conversation.Message{
			ID:        generateID(),
			Role:      conversation.RoleUser,
			Content:   prompt,
			Timestamp: time.Now().UTC(),
		})
	}

	// 5. Build AgentRunRequest
	req := orchestrator.AgentRunRequest{
		AgentID:        info.AgentID,
		AgentSlug:      info.AgentSlug,
		AgentRole:      info.AgentRole,
		CrewID:         info.CrewID,
		CrewSlug:       info.CrewSlug,
		WorkspaceID:    info.WorkspaceID,
		ChatID:         chatID,
		ContainerID:    containerID,
		CLIAdapter:     info.CLIAdapter,
		LLMModel:       info.LLMModel,
		SystemPrompt:   info.SystemPrompt,
		UserMessage:    prompt,
		ToolProfile:    info.ToolProfile,
		Credentials:    info.Credentials,
		TimeoutSecs:    info.TimeoutSecs,
		MemoryEnabled:  info.MemoryEnabled,
		CrewMembers:    info.CrewMembers,
		AllCrews:       info.AllCrews,
		ActiveMissions: info.ActiveMissions,
		NetworkMode:    info.NetworkMode,
		AllowedDomains: info.AllowedDomains,
	}

	// 6. Create run record
	runMeta := map[string]interface{}{
		"cli_adapter": info.CLIAdapter,
		"crew_id":     info.CrewID,
		"crew_slug":   info.CrewSlug,
		"agent_slug":  info.AgentSlug,
		"tags":        []string{"scheduled", info.CLIAdapter},
	}
	if err := s.resolver.CreateRun(ctx, runID, ag.ID, chatID, ag.Workspace, "SCHEDULED", runMeta); err != nil {
		s.logger.Warn("scheduled: create run failed", "error", err)
	}

	// 7. Run agent
	startedAt := time.Now()
	var fullResponse strings.Builder

	var logBuf *logcollector.OutputBuffer
	if s.logWriter != nil {
		logBuf = logcollector.NewOutputBuffer(s.logWriter, info.CrewID, info.AgentSlug)
		defer logBuf.Close()
	}

	var resultMeta map[string]interface{}
	handler := func(event orchestrator.AgentEvent) {
		if event.Type == "text" {
			fullResponse.WriteString(event.Content)
		}
		if event.Type == "result" {
			if m, ok := event.Metadata.(map[string]interface{}); ok {
				resultMeta = m
			}
		}
		if logBuf != nil {
			_ = logBuf.Append(logcollector.LogEntry{
				Timestamp: event.Timestamp,
				Level:     "info",
				Agent:     info.AgentSlug,
				Event:     event.Type,
				Content:   event.Content,
				Metadata:  event.Metadata,
			})
		}
	}

	runErr := s.orch.RunAgent(ctx, req, handler)

	// 8. Update run record
	completedMeta := map[string]interface{}{
		"duration_ms": time.Since(startedAt).Milliseconds(),
	}
	if resultMeta != nil {
		for _, k := range []string{"total_cost_usd", "num_turns", "usage", "model_usage"} {
			if v, ok := resultMeta[k]; ok {
				completedMeta[k] = v
			}
		}
	}

	if runErr != nil {
		errMsg := runErr.Error()
		if err := s.resolver.UpdateRun(ctx, runID, "FAILED", nil, &errMsg, completedMeta); err != nil {
			s.logger.Warn("failed to update run status", "run_id", runID, "status", "FAILED", "error", err)
		}
		s.logger.Error("scheduled run failed", "agent", ag.Slug, "error", runErr, "duration_ms", completedMeta["duration_ms"])
	} else {
		exitCode := 0
		if err := s.resolver.UpdateRun(ctx, runID, "COMPLETED", &exitCode, nil, completedMeta); err != nil {
			s.logger.Warn("failed to update run status", "run_id", runID, "status", "COMPLETED", "error", err)
		}
		s.logger.Info("scheduled run completed", "agent", ag.Slug, "duration_ms", completedMeta["duration_ms"])
	}

	// Persist assistant response
	if s.convStore != nil && fullResponse.Len() > 0 {
		_ = s.convStore.Append(ctx, chatID, conversation.Message{
			ID:        generateID(),
			Role:      conversation.RoleAssistant,
			Content:   fullResponse.String(),
			Timestamp: time.Now().UTC(),
		})
		_ = s.resolver.IncrementMessageCount(ctx, chatID, 2)
	}

	// 9. Update schedule timestamps
	s.updateTimestamps(ag.ID, ag.Cron, false)
}

func (s *Scheduler) updateTimestamps(agentID, cronExpr string, errorOnly bool) {
	now := time.Now().UTC().Format(time.RFC3339)

	var nextRun *string
	if sched, err := s.parser.Parse(cronExpr); err == nil {
		next := sched.Next(time.Now()).UTC().Format(time.RFC3339)
		nextRun = &next
	} else {
		// A failed parse here means the cron stored on the agent row no
		// longer parses (stored row was corrupted, validator drift, etc.).
		// Without an explicit signal, schedule_next_run silently freezes
		// at its stale value. Log loudly and clear next_run so the UI
		// reflects the real state instead of pointing at a long-past
		// timestamp.
		s.logger.Warn("schedule cron unparsable; clearing schedule_next_run",
			"agent_id", agentID, "cron", cronExpr, "error", err)
		if _, err := s.db.ExecContext(s.ctx,
			"UPDATE agents SET schedule_next_run = NULL WHERE id = ?", agentID); err != nil {
			s.logger.Warn("clear schedule_next_run", "agent_id", agentID, "error", err)
		}
	}

	// Use the scheduler's lifecycle ctx for all timestamp DB writes so a
	// Stop() during shutdown short-circuits in-flight UPDATEs cleanly
	// instead of racing the DB pool close. context.Background here meant
	// shutdown could log "use of closed connection" warnings even on a
	// graceful stop.
	if errorOnly {
		if nextRun != nil {
			if _, err := s.db.ExecContext(s.ctx, "UPDATE agents SET schedule_next_run = ? WHERE id = ?", *nextRun, agentID); err != nil {
				s.logger.Warn("update schedule_next_run", "agent_id", agentID, "error", err)
			}
		}
		return
	}

	if nextRun != nil {
		if _, err := s.db.ExecContext(s.ctx, "UPDATE agents SET schedule_last_run = ?, schedule_next_run = ? WHERE id = ?",
			now, *nextRun, agentID); err != nil {
			s.logger.Warn("update schedule timestamps", "agent_id", agentID, "error", err)
		}
	} else {
		if _, err := s.db.ExecContext(s.ctx, "UPDATE agents SET schedule_last_run = ? WHERE id = ?", now, agentID); err != nil {
			s.logger.Warn("update schedule_last_run", "agent_id", agentID, "error", err)
		}
	}
}

func generateID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	// Direct byte-append: "sched_" + <unix-nano> + "_" + 24 hex chars.
	// Previous fmt.Sprintf + hex.EncodeToString chain paid 4 heap
	// allocations per call; this shape needs just the final string.
	var buf [64]byte
	out := append(buf[:0], "sched_"...)
	out = strconv.AppendInt(out, time.Now().UnixNano(), 10)
	out = append(out, '_')
	out = hex.AppendEncode(out, b)
	return string(out)
}
