package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"
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

// IPC handlers -- stubs for now, will be implemented with providers

func (s *Server) handleIPCHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAgentStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.logger.Debug("agent status request", "agent_id", id)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"agent_id": id,
		"status":   "idle",
	})
}

func (s *Server) handleAgentStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.logger.Info("agent start request", "agent_id", id)
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"agent_id": id,
		"status":   "starting",
	})
}

func (s *Server) handleAgentStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.logger.Info("agent stop request", "agent_id", id)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"agent_id": id,
		"status":   "stopped",
	})
}

func (s *Server) handleContainerStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"team_id": id,
		"status":  "stopped",
	})
}

func (s *Server) handleContainerStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.logger.Info("container start request", "team_id", id)
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"team_id": id,
		"status":  "starting",
	})
}

func (s *Server) handleContainerStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.logger.Info("container stop request", "team_id", id)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"team_id": id,
		"status":  "stopped",
	})
}

func (s *Server) handleFileList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"team_id": id,
		"files":   []interface{}{},
	})
}

func (s *Server) handleSessionMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id": id,
		"messages":   []interface{}{},
	})
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}


