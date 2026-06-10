package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/llmproxy"
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
	s.ipcMux.HandleFunc("GET /crews/{id}/container-files", s.handleContainerFileList)
	s.ipcMux.HandleFunc("GET /crews/{id}/git-log", s.handleContainerGitLog)
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

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	checks := map[string]string{}

	// Database
	if s.db != nil {
		if err := s.db.PingContext(ctx); err != nil {
			checks["db"] = err.Error()
		} else {
			checks["db"] = "ok"
		}
	}

	// State store (bbolt) — lightweight read on a non-existent bucket is safe
	if s.state != nil {
		if _, err := s.state.List(ctx, "readyz"); err != nil {
			checks["state"] = err.Error()
		} else {
			checks["state"] = "ok"
		}
	}

	allOK := true
	for _, v := range checks {
		if v != "ok" {
			allOK = false
			break
		}
	}

	status := http.StatusOK
	if !allOK {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]interface{}{
		"status": allOK,
		"checks": checks,
	})
}

// metricsAuthorized gates /metrics. The endpoint exposes hostname,
// uptime, goroutine and memory counters, and live WS-connection counts —
// useful to a Prometheus scraper, but also a side-channel for an
// attacker timing a DoS or a deploy. Two ways to authorize:
//
//   - The connection peer is loopback (typical: localhost Prometheus or
//     a node-local sidecar scrape — no token needed).
//   - The request carries Authorization: Bearer <token> matching
//     CREWSHIP_METRICS_TOKEN, compared in constant time.
//
// When CREWSHIP_METRICS_TOKEN is unset, only loopback is permitted. The
// previous behaviour ("anyone can read") survived only because no PoC had
// chained the leak into something more impactful — see F-003.
//
// The loopback check evaluates the TRUE client IP via api.ExtractClientIP,
// not the raw r.RemoteAddr — otherwise a same-host reverse proxy (Caddy /
// nginx) silently turns every public request into a loopback hit and the
// bypass exempts the whole internet. gh#553 closed that drift; the rate
// limiter's trusted-proxy + right-to-left XFF rule is the shared truth.
func metricsAuthorized(r *http.Request) bool {
	if isLoopbackPeer(api.ExtractClientIP(r)) {
		return true
	}
	want := strings.TrimSpace(os.Getenv("CREWSHIP_METRICS_TOKEN"))
	if want == "" {
		return false
	}
	got := r.Header.Get("Authorization")
	if !strings.HasPrefix(got, "Bearer ") {
		return false
	}
	gotToken := strings.TrimPrefix(got, "Bearer ")
	// Constant-time compare keeps timing side-channels off the table even
	// though the win against a high-entropy token is tiny.
	return subtle.ConstantTimeCompare([]byte(gotToken), []byte(want)) == 1
}

func isLoopbackPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !metricsAuthorized(r) {
		// 404 rather than 401 to avoid confirming the endpoint exists to
		// an unauthorized scanner. Prometheus scrapers configured for the
		// authorized path won't notice the difference.
		http.NotFound(w, r)
		return
	}

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

	// Domain metrics (assignments, queue depth, pipeline runs, run
	// events, LLM cost, container health, migration version) — see
	// metrics_domain.go. Cached for domainMetricsTTL.
	fmt.Fprint(w, s.domainMetricsBlock(r.Context(), hostname))
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
		"connections": s.wsHub.ConnectionCount(),
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

var sanitizeMetadataAllowed = map[string]struct{}{
	"source":         {},
	"summary":        {},
	"duration":       {},
	"duration_ms":    {},
	"tool_name":      {},
	"num_turns":      {},
	"total_cost_usd": {},
	"usage":          {},
	"model":          {},
	"session_id":     {},
	"exit_code":      {},
}

func sanitizeMetadata(raw any) map[string]interface{} {
	m, ok := raw.(map[string]interface{})
	if !ok || m == nil {
		return nil
	}
	safe := make(map[string]interface{}, len(m))
	for k, v := range m {
		if _, ok := sanitizeMetadataAllowed[k]; ok {
			safe[k] = v
		}
	}
	return safe
}

// handleContainerFileList runs `find` inside a crew's container and returns the file tree.
