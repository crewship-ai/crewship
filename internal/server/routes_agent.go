package server

// Agent lifecycle handlers — start, stop, status, logs, chat-message
// IPC. Extracted from routes.go for readability; the routes themselves
// are still mounted by registerIPCRoutes / registerRoutes.

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
)

func (s *Server) handleAgentStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.logger.Debug("agent status request", "agent_id", id)

	if s.state == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"agent_id": id, "status": "idle"})
		return
	}

	data, err := s.state.Get(r.Context(), "agent_runs", id)
	if err != nil || data == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"agent_id": id, "status": "idle"})
		return
	}

	if !json.Valid(data) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"agent_id": id, "status": "idle"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

type agentStartRequest struct {
	WorkspaceID    string                    `json:"workspace_id"`
	CrewID         string                    `json:"crew_id"`
	CrewSlug       string                    `json:"crew_slug"`
	AgentSlug      string                    `json:"agent_slug"`
	ChatID         string                    `json:"session_id"`
	CLIAdapter     string                    `json:"cli_adapter"`
	SystemPrompt   string                    `json:"system_prompt"`
	UserMessage    string                    `json:"user_message"`
	ToolProfile    string                    `json:"tool_profile"`
	TimeoutSecs    int                       `json:"timeout_seconds"`
	Credentials    []orchestrator.Credential `json:"credentials"`
	NetworkMode    string                    `json:"network_mode"`
	AllowedDomains []string                  `json:"allowed_domains"`
	MemoryMB       int                       `json:"memory_mb"`
	CPUs           float64                   `json:"cpus"`
	TTLHours       int                       `json:"ttl_hours"`
}

func (s *Server) handleAgentStart(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	s.logger.Info("agent start request", "agent_id", agentID)

	if s.container == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "container provider not configured"})
		return
	}

	var req agentStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	memoryMB := req.MemoryMB
	if memoryMB <= 0 {
		memoryMB = s.cfg.Container.DefaultMemoryMB
	}
	cpus := req.CPUs
	if cpus <= 0 {
		cpus = s.cfg.Container.DefaultCPUs
	}
	containerID, err := s.container.EnsureCrewRuntime(r.Context(), provider.CrewConfig{
		ID:       req.CrewID,
		Slug:     req.CrewSlug,
		MemoryMB: memoryMB,
		CPUs:     cpus,
	})
	if err != nil {
		s.logger.Error("failed to ensure team runtime", "crew_id", req.CrewID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to start container"})
		return
	}

	// Start file watcher for this crew's output directory (idempotent).
	s.ensureFileWatcher(req.CrewID)

	// Register container for stats collection.
	if s.statsCollector != nil {
		s.statsCollector.Register(containerID, req.CrewID, req.WorkspaceID)
	}

	runReq := orchestrator.AgentRunRequest{
		AgentID:        agentID,
		AgentSlug:      req.AgentSlug,
		CrewID:         req.CrewID,
		CrewSlug:       req.CrewSlug,
		WorkspaceID:    req.WorkspaceID,
		ChatID:         req.ChatID,
		ContainerID:    containerID,
		CLIAdapter:     req.CLIAdapter,
		SystemPrompt:   req.SystemPrompt,
		UserMessage:    req.UserMessage,
		ToolProfile:    req.ToolProfile,
		Credentials:    req.Credentials,
		TimeoutSecs:    req.TimeoutSecs,
		NetworkMode:    req.NetworkMode,
		AllowedDomains: req.AllowedDomains,
		MemoryMB:       memoryMB,
		CPUs:           cpus,
		TTLHours:       req.TTLHours,
	}

	go func() {
		timeout := time.Duration(req.TimeoutSecs) * time.Second
		if timeout <= 0 {
			timeout = 30 * time.Minute
		}
		ctx, cancel := context.WithTimeout(s.runCtx, timeout)
		defer cancel()

		var logBuf *logcollector.OutputBuffer
		if s.logWriter != nil {
			logBuf = logcollector.NewOutputBuffer(s.logWriter, req.CrewID, req.AgentSlug)
			defer logBuf.Close()
		}

		base, _ := orchestrator.NewBufferingHandler(orchestrator.BufferingHandlerOpts{
			LogBuf:    logBuf,
			AgentSlug: req.AgentSlug,
		})
		handler := func(event orchestrator.AgentEvent) {
			base(event)

			// Broadcast real-time log events to the workspace channel
			s.wsHub.BroadcastWorkspace(req.WorkspaceID, "agent.log",
				map[string]interface{}{
					"ts":       event.Timestamp,
					"level":    "info",
					"agent":    req.AgentSlug,
					"agent_id": agentID,
					"event":    event.Type,
					"content":  event.Content,
					"metadata": sanitizeMetadata(func() map[string]interface{} {
						if m, ok := event.Metadata.(map[string]interface{}); ok {
							return m
						}
						return nil
					}()),
				})
		}
		if err := s.orchestrator.RunAgent(ctx, runReq, handler); err != nil {
			s.logger.Error("agent run failed", "agent_id", agentID, "error", err)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"agent_id":     agentID,
		"container_id": containerID,
		"status":       "starting",
	})
}

func (s *Server) handleAgentStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.logger.Info("agent stop request", "agent_id", id)

	if s.state != nil {
		data, err := s.state.Get(r.Context(), "agent_runs", id)
		if err == nil && data != nil {
			var run orchestrator.RunState
			if json.Unmarshal(data, &run) == nil && run.Status == "running" {
				run.Status = "stopped"
				run.LastActivity = time.Now()
				if updated, err := json.Marshal(run); err == nil {
					_ = s.state.Set(r.Context(), "agent_runs", id, updated)
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"agent_id": id,
		"status":   "stopped",
	})
}

func (s *Server) handleAgentLogs(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	crewID := r.URL.Query().Get("crew_id")

	if crewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crew_id query parameter required"})
		return
	}

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	// Clamp the offset: a negative value is meaningless and a huge one would
	// drive a wasteful full scan of the agent log file with nothing to return.
	const maxLogOffset = 10_000_000
	if offset < 0 {
		offset = 0
	}
	if offset > maxLogOffset {
		offset = maxLogOffset
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 100
	}

	if s.logReader == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"agent_id": agentID, "logs": []interface{}{}})
		return
	}

	entries, err := s.logReader.ReadAgentLogs(crewID, agentID, offset, limit)
	if err != nil {
		s.logger.Error("read agent logs failed", "agent_id", agentID, "error", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"agent_id": agentID, "logs": []interface{}{}})
		return
	}
	if entries == nil {
		entries = []logcollector.LogEntry{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"agent_id": agentID, "logs": entries})
}

func (s *Server) handleChatMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if s.convStore == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"chat_id": id, "messages": []interface{}{}})
		return
	}

	messages, err := s.convStore.Read(r.Context(), id, 0, 0)
	if err != nil {
		s.logger.Error("read chat messages failed", "chat_id", id, "error", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"chat_id": id, "messages": []interface{}{}})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"chat_id": id, "messages": messages})
}
