package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/llmproxy"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /readyz", s.handleReadyz)
	s.mux.HandleFunc("GET /metrics", s.handleMetrics)
	s.mux.HandleFunc("GET /ws", s.handleWebSocket)
	s.mux.HandleFunc("GET /ws/terminal", s.handleTerminalWebSocket)
}

func (s *Server) registerIPCRoutes() {
	s.ipcMux.HandleFunc("GET /health", s.handleIPCHealth)
	s.ipcMux.HandleFunc("GET /agents/{id}/status", s.handleAgentStatus)
	s.ipcMux.HandleFunc("POST /agents/{id}/start", s.handleAgentStart)
	s.ipcMux.HandleFunc("POST /agents/{id}/stop", s.handleAgentStop)
	s.ipcMux.HandleFunc("GET /crews/{id}/container/status", s.handleContainerStatus)
	s.ipcMux.HandleFunc("POST /crews/{id}/container/start", s.handleContainerStart)
	s.ipcMux.HandleFunc("POST /crews/{id}/container/stop", s.handleContainerStop)
	s.ipcMux.HandleFunc("GET /agents/{id}/logs", s.handleAgentLogs)
	s.ipcMux.HandleFunc("GET /crews/{id}/stats", s.handleCrewStats)
	s.ipcMux.HandleFunc("GET /crews/{id}/files", s.handleFileList)
	s.ipcMux.HandleFunc("GET /crews/{id}/files/download", s.handleFileDownload)
	s.ipcMux.HandleFunc("PUT /crews/{id}/files/save", s.handleFileSave)
	s.ipcMux.HandleFunc("GET /chats/{id}/messages", s.handleChatMessages)
	s.ipcMux.HandleFunc("POST /credentials/sync", s.handleCredentialSync)
	s.ipcMux.HandleFunc("GET /credentials/{workspaceId}/token", s.handleCredentialToken)
	s.ipcMux.HandleFunc("GET /debug/logs", s.handleDebugLogs)
	s.ipcMux.HandleFunc("GET /debug/info", s.handleDebugInfo)
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

func (s *Server) handleTerminalWebSocket(w http.ResponseWriter, r *http.Request) {
	if s.terminalHandler == nil {
		http.Error(w, "terminal not available", http.StatusServiceUnavailable)
		return
	}
	s.terminalHandler.ServeHTTP(w, r)
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

		handler := func(event orchestrator.AgentEvent) {
			if logBuf != nil {
				_ = logBuf.Append(logcollector.LogEntry{
					Timestamp: event.Timestamp,
					Level:     "info",
					Agent:     req.AgentSlug,
					Event:     event.Type,
					Content:   event.Content,
					Metadata:  event.Metadata,
				})
			}

			// Broadcast real-time log events to the workspace channel
			if s.wsHub != nil {
				channel := "workspace:" + req.WorkspaceID
				s.wsHub.Broadcast(channel, ws.ServerMessage{
					Type:    "agent.log",
					Channel: channel,
					Payload: map[string]interface{}{
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
					},
				})
			}
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

func (s *Server) handleContainerStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if s.container == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"crew_id": id, "status": "not_configured"})
		return
	}

	status, err := s.container.ContainerStatus(r.Context(), id)
	if err != nil {
		s.logger.Error("container status failed", "crew_id", id, "error", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"crew_id": id, "status": "unknown"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"crew_id": id,
		"status":  status.State,
		"uptime":  status.Uptime,
	})
}

func (s *Server) handleContainerStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.logger.Info("container start request", "crew_id", id)

	if s.container == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "container provider not configured"})
		return
	}

	containerID, err := s.container.EnsureCrewRuntime(r.Context(), provider.CrewConfig{
		ID:       id,
		MemoryMB: s.cfg.Container.DefaultMemoryMB,
		CPUs:     s.cfg.Container.DefaultCPUs,
	})
	if err != nil {
		s.logger.Error("container start failed", "crew_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "container start failed"})
		return
	}

	s.ensureFileWatcher(id)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"crew_id":      id,
		"container_id": containerID,
		"status":       "running",
	})
}

func (s *Server) handleContainerStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.logger.Info("container stop request", "crew_id", id)

	if s.container == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "container provider not configured"})
		return
	}

	// Resolve crew slug from DB so we can build the container name via provider.
	var slug string
	if s.db != nil {
		_ = s.db.QueryRowContext(r.Context(), "SELECT slug FROM crews WHERE id = ?", id).Scan(&slug)
	}
	containerName := id // fallback: use raw id (works for Docker container hashes)
	if slug != "" {
		containerName = s.container.CrewContainerName(slug)
	}

	if err := s.container.StopCrewRuntime(r.Context(), containerName); err != nil {
		s.logger.Error("container stop failed", "crew_id", id, "container", containerName, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "container stop failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"crew_id": id, "status": "stopped"})
}

func (s *Server) handleFileList(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("id")
	agentSlug := r.URL.Query().Get("agent_slug")

	if s.storage == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"crew_id": crewID, "files": []interface{}{}})
		return
	}

	// If agent_slug is provided, list agent's output namespace + root-level crew files
	dir := crewID
	if agentSlug != "" {
		clean := filepath.Base(agentSlug)
		if clean == "." || clean == ".." || strings.ContainsAny(clean, `/\`) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid agent_slug"})
			return
		}
		dir = filepath.Join(crewID, clean)
	}

	// Optional subdir parameter for lazy-loading subdirectories
	if subdir := r.URL.Query().Get("subdir"); subdir != "" {
		cleaned := filepath.Clean(subdir)
		if strings.HasPrefix(cleaned, "..") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid subdir"})
			return
		}
		dir = filepath.Join(dir, cleaned)
	}

	recursive := r.URL.Query().Get("recursive") == "true"

	var files []provider.FileInfo
	var err error
	if recursive {
		files, err = s.storage.ListRecursive(r.Context(), dir)
	} else {
		files, err = s.storage.List(r.Context(), dir)
	}
	if err != nil {
		s.logger.Error("file list failed", "crew_id", crewID, "agent_slug", agentSlug, "error", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"crew_id": crewID, "files": []interface{}{}})
		return
	}

	// When listing an agent's namespace, also include root-level crew files
	// (files the agent saved to /output/ instead of /output/<agent-slug>/)
	if agentSlug != "" {
		var rootFiles []provider.FileInfo
		if recursive {
			rootFiles, err = s.storage.ListRecursive(r.Context(), crewID)
		} else {
			rootFiles, err = s.storage.List(r.Context(), crewID)
		}
		if err == nil {
			for _, f := range rootFiles {
				if !f.IsDir {
					files = append(files, f)
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"crew_id": crewID, "files": files})
}

func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("id")
	filePath := r.URL.Query().Get("path")

	if filePath == "" {
		http.Error(w, "path query parameter required", http.StatusBadRequest)
		return
	}

	// Sanitize path to prevent directory traversal
	cleanPath := filepath.Clean(filePath)
	if strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	// Validate the path belongs to this crew (path from List is crew_id/agent/file)
	if !strings.HasPrefix(cleanPath, crewID+"/") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	if s.storage == nil {
		http.Error(w, "storage not configured", http.StatusServiceUnavailable)
		return
	}

	reader, err := s.storage.Read(r.Context(), cleanPath)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	defer reader.Close()

	filename := filepath.Base(filePath)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, reader); err != nil {
		s.logger.Error("file download stream error", "path", filePath, "error", err)
	}
}

func (s *Server) handleFileSave(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("id")
	filePath := r.URL.Query().Get("path")

	if filePath == "" {
		http.Error(w, "path query parameter required", http.StatusBadRequest)
		return
	}

	cleanPath := filepath.Clean(filePath)
	if strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(cleanPath, crewID+"/") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	if s.storage == nil {
		http.Error(w, "storage not configured", http.StatusServiceUnavailable)
		return
	}

	defer r.Body.Close()
	if err := s.storage.Write(r.Context(), cleanPath, r.Body); err != nil {
		s.logger.Error("file save failed", "path", filePath, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save file"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "saved", "path": filePath})
}

func (s *Server) handleAgentLogs(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	crewID := r.URL.Query().Get("crew_id")

	if crewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crew_id query parameter required"})
		return
	}

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
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

func (s *Server) handleCredentialSync(w http.ResponseWriter, _ *http.Request) {
	if s.tokenSyncer == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "LLM proxy not enabled"})
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(s.runCtx, 10*time.Second)
		defer cancel()
		if err := s.tokenSyncer.SyncNow(ctx); err != nil {
			s.logger.Error("credential sync failed", "error", err)
		}
	}()
	writeJSON(w, http.StatusOK, map[string]string{"status": "sync_triggered"})
}

func (s *Server) handleCredentialToken(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.PathValue("workspaceId")
	provider := r.URL.Query().Get("provider")

	if provider == "" {
		provider = "ANTHROPIC"
	}

	if s.tokenPool == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "token pool not initialized"})
		return
	}

	conn := s.tokenPool.SelectToken(workspaceID, llmproxy.ProviderType(provider))
	if conn == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no active credential for provider"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"credential_id": conn.ID,
		"provider":      conn.Provider,
		"type":          conn.Type,
		"access_token":  conn.AccessToken,
	})
}

func (s *Server) handleDebugLogs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 200
	}

	if s.debugLogs == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"logs": []interface{}{}})
		return
	}

	entries := s.debugLogs.Entries(limit)

	filterLevel := r.URL.Query().Get("level")
	filterAgent := r.URL.Query().Get("agent_id")

	if filterLevel != "" || filterAgent != "" {
		var filtered []interface{}
		for _, e := range entries {
			if filterLevel != "" && e.Level != filterLevel {
				continue
			}
			if filterAgent != "" {
				agentVal, hasAgent := e.Attrs["agent_id"]
				if hasAgent && agentVal != filterAgent {
					continue
				}
			}
			filtered = append(filtered, e)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"logs": filtered})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"logs": entries})
}

func (s *Server) handleDebugInfo(w http.ResponseWriter, _ *http.Request) {
	info := map[string]interface{}{
		"status":      "ok",
		"uptime":      time.Since(s.startedAt).String(),
		"uptime_secs": time.Since(s.startedAt).Seconds(),
		"connections":  s.wsHub.ConnectionCount(),
		"started_at":  s.startedAt.Format(time.RFC3339),
	}

	providers := map[string]string{
		"container": s.cfg.Container.Provider,
		"storage":   s.cfg.Storage.Provider,
		"state":     s.cfg.State.Provider,
	}
	info["providers"] = providers

	info["container_available"] = s.container != nil
	info["storage_available"] = s.storage != nil
	info["state_available"] = s.state != nil
	info["llm_proxy_enabled"] = s.tokenSyncer != nil

	config := map[string]interface{}{
		"runtime_image":     s.cfg.Container.RuntimeImage,
		"default_memory_mb": s.cfg.Container.DefaultMemoryMB,
		"default_cpus":      s.cfg.Container.DefaultCPUs,
		"network":           s.cfg.Container.Network,
		"log_path":          s.cfg.Storage.LogPath,
		"storage_base_path": s.cfg.Storage.BasePath,
	}
	info["config"] = config

	writeJSON(w, http.StatusOK, info)
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("failed to encode JSON response", "error", err)
	}
}

func (s *Server) handleCrewStats(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("id")
	if s.container == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"crew_id": crewID, "stats": nil})
		return
	}
	// Look up container by crew ID from the stats collector instead of
	// trusting a client-supplied container_id query parameter.
	if s.statsCollector != nil {
		containerID, m := s.statsCollector.LatestByCrewID(crewID)
		if m != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"crew_id": crewID, "container_id": containerID,
				"cpu_percent": m.CPUPercent, "memory_used": m.MemoryUsed,
				"memory_limit": m.MemoryLimit, "memory_percent": m.MemoryPct,
				"net_rx_bytes": m.NetRx, "net_tx_bytes": m.NetTx,
				"pids": m.PIDs, "timestamp": m.Timestamp,
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"crew_id": crewID, "stats": nil})
}

func (s *Server) ensureFileWatcher(crewID string) {
	if s.fileWatcher == nil {
		return
	}
	if _, loaded := s.watchedCrews.LoadOrStore(crewID, true); loaded {
		return
	}
	if err := s.fileWatcher.Watch(s.runCtx, crewID); err != nil {
		s.logger.Warn("failed to start file watcher", "crew_id", crewID, "error", err)
		s.watchedCrews.Delete(crewID)
	}
}

// sanitizeMetadata filters agent event metadata to a safe allowlist before
// broadcasting to workspace WebSocket clients, preventing leakage of tool
// inputs, error details, or MCP configuration.
func sanitizeMetadata(raw any) map[string]interface{} {
	m, ok := raw.(map[string]interface{})
	if !ok || m == nil {
		return nil
	}
	allowed := map[string]bool{
		"source": true, "summary": true, "duration": true, "duration_ms": true,
		"tool_name": true, "num_turns": true, "total_cost_usd": true,
		"usage": true, "model": true, "session_id": true, "exit_code": true,
	}
	safe := make(map[string]interface{}, len(m))
	for k, v := range m {
		if allowed[k] {
			safe[k] = v
		}
	}
	return safe
}


