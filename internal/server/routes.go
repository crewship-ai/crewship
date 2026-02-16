package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
)

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /readyz", s.handleReadyz)
	s.mux.HandleFunc("GET /metrics", s.handleMetrics)
	s.mux.HandleFunc("GET /ws", s.handleWebSocket)
}

func (s *Server) registerIPCRoutes() {
	s.ipcMux.HandleFunc("GET /health", s.handleIPCHealth)
	s.ipcMux.HandleFunc("GET /agents/{id}/status", s.handleAgentStatus)
	s.ipcMux.HandleFunc("POST /agents/{id}/start", s.handleAgentStart)
	s.ipcMux.HandleFunc("POST /agents/{id}/stop", s.handleAgentStop)
	s.ipcMux.HandleFunc("GET /teams/{id}/container/status", s.handleContainerStatus)
	s.ipcMux.HandleFunc("POST /teams/{id}/container/start", s.handleContainerStart)
	s.ipcMux.HandleFunc("POST /teams/{id}/container/stop", s.handleContainerStop)
	s.ipcMux.HandleFunc("GET /agents/{id}/logs", s.handleAgentLogs)
	s.ipcMux.HandleFunc("GET /teams/{id}/files", s.handleFileList)
	s.ipcMux.HandleFunc("GET /sessions/{id}/messages", s.handleSessionMessages)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"service": "crewshipd",
		"uptime":  time.Since(s.startedAt).String(),
	})
}

func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	// TODO: check Docker connectivity, bbolt state, etc.
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "ready",
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	hostname, _ := os.Hostname()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	metrics := []struct {
		name  string
		help  string
		mtype string
		value interface{}
	}{
		{"crewshipd_uptime_seconds", "Time since crewshipd started", "gauge", time.Since(s.startedAt).Seconds()},
		{"crewshipd_goroutines", "Number of goroutines", "gauge", runtime.NumGoroutine()},
		{"crewshipd_memory_alloc_bytes", "Bytes of allocated heap", "gauge", mem.Alloc},
		{"crewshipd_memory_sys_bytes", "Total bytes obtained from system", "gauge", mem.Sys},
		{"crewshipd_gc_runs_total", "Total GC runs", "counter", mem.NumGC},
		{"crewshipd_ws_connections", "Active WebSocket connections", "gauge", s.wsHub.ConnectionCount()},
	}

	for _, m := range metrics {
		fmt.Fprintf(w, "# HELP %s %s\n", m.name, m.help)
		fmt.Fprintf(w, "# TYPE %s %s\n", m.name, m.mtype)
		fmt.Fprintf(w, "%s{hostname=%q} %v\n", m.name, hostname, m.value)
	}
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	s.wsHub.HandleUpgrade(w, r)
}

// IPC handlers -- wired to real providers

func (s *Server) handleIPCHealth(w http.ResponseWriter, _ *http.Request) {
	status := map[string]interface{}{
		"status":      "ok",
		"uptime":      time.Since(s.startedAt).String(),
		"connections": s.wsHub.ConnectionCount(),
	}
	writeJSON(w, http.StatusOK, status)
}

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
	TeamID       string                    `json:"team_id"`
	TeamSlug     string                    `json:"team_slug"`
	AgentSlug    string                    `json:"agent_slug"`
	SessionID    string                    `json:"session_id"`
	CLIAdapter   string                    `json:"cli_adapter"`
	SystemPrompt string                    `json:"system_prompt"`
	UserMessage  string                    `json:"user_message"`
	ToolProfile  string                    `json:"tool_profile"`
	TimeoutSecs  int                       `json:"timeout_seconds"`
	Credentials  []orchestrator.Credential `json:"credentials"`
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

	containerID, err := s.container.EnsureTeamRuntime(r.Context(), provider.TeamConfig{
		ID:       req.TeamID,
		Slug:     req.TeamSlug,
		MemoryMB: s.cfg.Container.DefaultMemoryMB,
		CPUs:     s.cfg.Container.DefaultCPUs,
	})
	if err != nil {
		s.logger.Error("failed to ensure team runtime", "team_id", req.TeamID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to start container"})
		return
	}

	runReq := orchestrator.AgentRunRequest{
		AgentID:      agentID,
		AgentSlug:    req.AgentSlug,
		TeamID:       req.TeamID,
		TeamSlug:     req.TeamSlug,
		SessionID:    req.SessionID,
		ContainerID:  containerID,
		CLIAdapter:   req.CLIAdapter,
		SystemPrompt: req.SystemPrompt,
		UserMessage:  req.UserMessage,
		ToolProfile:  req.ToolProfile,
		Credentials:  req.Credentials,
		TimeoutSecs:  req.TimeoutSecs,
	}

	go func() {
		handler := func(event orchestrator.AgentEvent) {
			if s.logWriter != nil {
				_ = s.logWriter.Append(req.TeamID, req.AgentSlug, logcollector.LogEntry{
					Timestamp: event.Timestamp,
					Level:     "info",
					Agent:     req.AgentSlug,
					Event:     event.Type,
					Content:   event.Content,
				})
			}
		}
		if err := s.orchestrator.RunAgent(r.Context(), runReq, handler); err != nil {
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

func (s *Server) handleContainerStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if s.container == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"team_id": id, "status": "not_configured"})
		return
	}

	status, err := s.container.ContainerStatus(r.Context(), id)
	if err != nil {
		s.logger.Error("container status failed", "team_id", id, "error", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"team_id": id, "status": "unknown"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"team_id": id,
		"status":  status.State,
		"uptime":  status.Uptime,
	})
}

func (s *Server) handleContainerStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.logger.Info("container start request", "team_id", id)

	if s.container == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "container provider not configured"})
		return
	}

	containerID, err := s.container.EnsureTeamRuntime(r.Context(), provider.TeamConfig{
		ID:       id,
		MemoryMB: s.cfg.Container.DefaultMemoryMB,
		CPUs:     s.cfg.Container.DefaultCPUs,
	})
	if err != nil {
		s.logger.Error("container start failed", "team_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "container start failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"team_id":      id,
		"container_id": containerID,
		"status":       "running",
	})
}

func (s *Server) handleContainerStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.logger.Info("container stop request", "team_id", id)

	if s.container == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "container provider not configured"})
		return
	}

	if err := s.container.StopTeamRuntime(r.Context(), id); err != nil {
		s.logger.Error("container stop failed", "team_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "container stop failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"team_id": id, "status": "stopped"})
}

func (s *Server) handleFileList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if s.storage == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"team_id": id, "files": []interface{}{}})
		return
	}

	files, err := s.storage.List(r.Context(), id)
	if err != nil {
		s.logger.Error("file list failed", "team_id", id, "error", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"team_id": id, "files": []interface{}{}})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"team_id": id, "files": files})
}

func (s *Server) handleAgentLogs(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	teamID := r.URL.Query().Get("team_id")

	if teamID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "team_id query parameter required"})
		return
	}

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 100
	}

	entries, err := s.logReader.ReadAgentLogs(teamID, agentID, offset, limit)
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

func (s *Server) handleSessionMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if s.convStore == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"session_id": id, "messages": []interface{}{}})
		return
	}

	messages, err := s.convStore.Read(r.Context(), id, 0, 0)
	if err != nil {
		s.logger.Error("read session messages failed", "session_id", id, "error", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"session_id": id, "messages": []interface{}{}})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"session_id": id, "messages": messages})
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("failed to encode JSON response", "error", err)
	}
}


