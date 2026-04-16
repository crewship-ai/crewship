package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// DefaultAddr is the default listen address for the sidecar HTTP server inside the container.
const DefaultAddr = "127.0.0.1:9119"

// MemoryConfig configures the sidecar memory engine.
type MemoryConfig struct {
	Enabled        bool   `json:"enabled"`
	BasePath       string `json:"base_path"`
	AgentSlug      string `json:"agent_slug"`
	AgentRole      string `json:"agent_role"`       // "lead" or "agent" — lead owns crew memory index
	CrewMemoryPath string `json:"crew_memory_path"` // path to crew shared memory (e.g. /crew/shared/.memory)
}

// IPCConfig holds the crewshipd internal API address for assignment forwarding.
// Lead agents use this to POST assignment requests through the sidecar to crewshipd.
// ContainerID is the Docker container where this agent is running; forwarded to
// crewshipd so /keeper/execute can exec commands in the correct container.
type IPCConfig struct {
	BaseURL     string `json:"base_url"`
	Token       string `json:"token"`
	AgentID     string `json:"agent_id"`
	AgentSlug   string `json:"agent_slug"`
	CrewID      string `json:"crew_id"`
	WorkspaceID string `json:"workspace_id"`
	ChatID      string `json:"chat_id"`
	ContainerID string `json:"container_id"`
}

// CrewMember describes a crew member accessible for lead assignment routing.
type CrewMember struct {
	ID        string `json:"id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	RoleTitle string `json:"role_title"`
	ChatID    string `json:"chat_id,omitempty"`
}

// NetworkPolicyConfig configures per-crew network access mode.
type NetworkPolicyConfig struct {
	Mode           string   `json:"mode"`            // "free" or "restricted"
	AllowedDomains []string `json:"allowed_domains"` // extra allowed domains for restricted mode
}

// MCPServerInput describes an MCP server to connect to via the gateway.
type MCPServerInput struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	DisplayName string            `json:"display_name"`
	Scope       string            `json:"scope,omitempty"` // "workspace" or "crew"
	Transport   string            `json:"transport"`       // "streamable-http", "sse", or "stdio"
	Endpoint    string            `json:"endpoint,omitempty"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Credential  *MCPCredInput     `json:"credential,omitempty"`
}

// MCPCredInput carries decrypted credential for MCP server authentication.
type MCPCredInput struct {
	Token  string `json:"token"`
	Type   string `json:"type"`             // "bearer", "api_key", "basic"
	Header string `json:"header,omitempty"` // custom header name (for api_key type)
}

// ServerConfig configures the sidecar server.
type ServerConfig struct {
	Addr           string   // listen address (default: 127.0.0.1:9119)
	AllowedDomains []string // extra allowed domains beyond defaults
	Credentials    []Credential
	Memory         *MemoryConfig
	IPC            *IPCConfig
	CrewMembers    []CrewMember
	NetworkPolicy  *NetworkPolicyConfig
	MCPServers     []MCPServerInput
	Logger         *slog.Logger
}

// Server is the crewship sidecar that runs inside agent containers.
// It provides an HTTP forward proxy with credential injection,
// optional memory search API, and assignment routing for lead agents.
type Server struct {
	httpServer       *http.Server
	credStore        *CredStore
	allowlist        *DomainAllowlist
	proxy            *Proxy
	memoryEngine     *memory.Engine
	crewMemoryEngine *memory.Engine // crew shared memory — only initialized for lead agents
	ipc              *IPCConfig
	crewMembers      []CrewMember
	mcpGateway       *MCPGateway
	logger           *slog.Logger
	readyCh          chan struct{} // closed when the TCP listener is bound
}

// NewServer creates a sidecar server ready to start.
func NewServer(cfg ServerConfig) *Server {
	if cfg.Addr == "" {
		cfg.Addr = DefaultAddr
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	credStore := NewCredStore()
	if len(cfg.Credentials) > 0 {
		credStore.Load(cfg.Credentials)
	}

	domains := make([]string, 0, len(DefaultAllowedDomains)+len(cfg.AllowedDomains))
	domains = append(domains, DefaultAllowedDomains...)
	domains = append(domains, cfg.AllowedDomains...)

	// Determine network mode: "free" means skip allowlist checks.
	// Unknown modes default to restricted (fail closed) to prevent silent egress.
	freeMode := true // default when no policy is set: unrestricted
	if cfg.NetworkPolicy != nil {
		switch cfg.NetworkPolicy.Mode {
		case "free":
			freeMode = true
		case "restricted":
			freeMode = false
			domains = append(domains, cfg.NetworkPolicy.AllowedDomains...)
		default:
			cfg.Logger.Error("unknown network mode, defaulting to restricted", "mode", cfg.NetworkPolicy.Mode)
			freeMode = false
		}
	}

	allowlist := NewDomainAllowlist(domains)

	proxy := NewProxy(ProxyConfig{
		CredStore: credStore,
		Allowlist: allowlist,
		Scrubber:  scrubber.New(),
		Logger:    cfg.Logger,
		FreeMode:  freeMode,
	})

	s := &Server{
		credStore:   credStore,
		allowlist:   allowlist,
		proxy:       proxy,
		ipc:         cfg.IPC,
		crewMembers: cfg.CrewMembers,
		logger:      cfg.Logger,
		readyCh:     make(chan struct{}),
	}

	// Initialize memory engine if configured
	if cfg.Memory != nil && cfg.Memory.Enabled && cfg.Memory.BasePath != "" {
		engine, err := memory.New(cfg.Memory.BasePath, memory.DefaultConfig())
		if err != nil {
			cfg.Logger.Error("failed to init memory engine", "error", err, "path", cfg.Memory.BasePath)
		} else {
			s.memoryEngine = engine
			// Index existing memory files on startup
			if err := engine.Reindex(); err != nil {
				cfg.Logger.Warn("initial memory reindex failed", "error", err)
			}
			cfg.Logger.Info("memory engine initialized", "path", cfg.Memory.BasePath)
		}

		// Initialize crew shared memory engine for lead agents only.
		// The lead sidecar owns the crew FTS5 index — single writer, zero contention.
		if cfg.Memory.AgentRole == "lead" && cfg.Memory.CrewMemoryPath != "" {
			// Ensure crew memory directory exists (may not on first run)
			if err := os.MkdirAll(cfg.Memory.CrewMemoryPath, 0o755); err != nil {
				cfg.Logger.Error("failed to create crew memory dir", "error", err, "path", cfg.Memory.CrewMemoryPath)
			}
			crewEngine, err := memory.New(cfg.Memory.CrewMemoryPath, memory.DefaultConfig())
			if err != nil {
				cfg.Logger.Error("failed to init crew memory engine", "error", err, "path", cfg.Memory.CrewMemoryPath)
			} else {
				s.crewMemoryEngine = crewEngine
				if err := crewEngine.Reindex(); err != nil {
					cfg.Logger.Warn("initial crew memory reindex failed", "error", err)
				}
				cfg.Logger.Info("crew memory engine initialized", "path", cfg.Memory.CrewMemoryPath)
			}
		}
	}

	// Initialize MCP Gateway if servers are configured
	if len(cfg.MCPServers) > 0 {
		s.mcpGateway = NewMCPGateway(cfg.MCPServers, cfg.IPC, cfg.Logger)
		cfg.Logger.Info("MCP gateway initialized", "servers", len(cfg.MCPServers))
	}

	s.httpServer = &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.buildHandler(proxy),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	return s
}

// buildHandler creates the HTTP handler that routes memory API requests
// to dedicated handlers while passing everything else to the forward proxy.
func (s *Server) buildHandler(proxy *Proxy) http.Handler {
	// The forward proxy must remain the top-level handler because it handles
	// both regular HTTP requests (with absolute URLs) and CONNECT tunnels.
	// We intercept only localhost requests to specific paths.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Memory and assignment API routes are only accessible from localhost
		if isLocalhost(r.Host) || isLocalhost(r.URL.Host) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/memory/search":
				s.handleMemorySearch(w, r)
				return
			case r.Method == http.MethodGet && r.URL.Path == "/memory/status":
				s.handleMemoryStatus(w, r)
				return
			case r.Method == http.MethodPost && r.URL.Path == "/memory/reindex":
				s.handleMemoryReindex(w, r)
				return
			case r.Method == http.MethodPost && r.URL.Path == "/assign":
				s.handleAssign(w, r)
				return
			case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/results/"):
				s.handleResults(w, r)
				return
			case r.Method == http.MethodPost && r.URL.Path == "/query":
				s.handleQuery(w, r)
				return
			case r.Method == http.MethodGet && r.URL.Path == "/standup":
				s.handleStandup(w, r)
				return
			case r.Method == http.MethodPost && r.URL.Path == "/escalate":
				s.handleEscalate(w, r)
				return
			case r.Method == http.MethodPost && r.URL.Path == "/report-confidence":
				s.handleReportConfidence(w, r)
				return
			case r.Method == http.MethodPost && r.URL.Path == "/mission/create":
				s.handleMissionCreate(w, r)
				return
			case r.Method == http.MethodGet && r.URL.Path == "/mission/templates":
				s.handleMissionTemplates(w, r)
				return
			case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/mission/") && !strings.Contains(r.URL.Path[len("/mission/"):], "/"):
				s.handleMissionStatus(w, r)
				return
			case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/mission/") && strings.HasSuffix(r.URL.Path, "/start"):
				s.handleMissionStart(w, r)
				return
			case r.Method == http.MethodPost && r.URL.Path == "/keeper/request":
				s.handleKeeperRequest(w, r)
				return
			case r.Method == http.MethodPost && r.URL.Path == "/keeper/execute":
				s.handleKeeperExecute(w, r)
				return
			case r.Method == http.MethodPost && r.URL.Path == "/expose-port":
				s.handleExposePort(w, r)
				return
			case r.Method == http.MethodGet && r.URL.Path == "/crews":
				s.handleListCrews(w, r)
				return
			case r.Method == http.MethodPost && r.URL.Path == "/crew/create":
				s.handleCreateCrew(w, r)
				return
			case r.Method == http.MethodPost && r.URL.Path == "/agent/create":
				s.handleCreateAgent(w, r)
				return
			case r.Method == http.MethodGet && r.URL.Path == "/credentials":
				s.handleListCredentials(w, r)
				return
			case r.Method == http.MethodPost && r.URL.Path == "/agent-credentials":
				s.handleAssignAgentCredential(w, r)
				return
			case r.Method == http.MethodGet && r.URL.Path == "/crew-connections":
				s.handleListCrewConnections(w, r)
				return
			case r.Method == http.MethodPost && r.URL.Path == "/crew-connections":
				s.handleCreateCrewConnection(w, r)
				return
			case r.Method == http.MethodPost && r.URL.Path == "/issue/create":
				s.handleIssueCreate(w, r)
				return
			case r.Method == http.MethodPost && r.URL.Path == "/proposal":
				s.handleCreateProposal(w, r)
				return
			case r.Method == http.MethodGet && r.URL.Path == "/proposals":
				s.handleListProposals(w, r)
				return
			case r.Method == http.MethodGet && r.URL.Path == "/missions/all":
				s.handleListAllMissions(w, r)
				return
			case r.Method == http.MethodGet && r.URL.Path == "/missions/all/summary":
				s.handleAllMissionsSummary(w, r)
				return
			case r.Method == http.MethodGet && r.URL.Path == "/manifest":
				s.handleGetManifest(w, r)
				return
			case r.Method == http.MethodPatch && r.URL.Path == "/manifest":
				s.handleUpdateManifest(w, r)
				return
			// MCP Gateway routes
			case r.Method == http.MethodGet && r.URL.Path == "/mcp/tools":
				s.handleMCPListTools(w, r)
				return
			case r.Method == http.MethodPost && r.URL.Path == "/mcp/call":
				s.handleMCPCallTool(w, r)
				return
			case r.Method == http.MethodGet && r.URL.Path == "/mcp/status":
				s.handleMCPStatus(w, r)
				return
			// Cross-crew connection routes
			case r.Method == http.MethodGet && r.URL.Path == "/connections":
				s.handleConnectionsList(w, r)
				return
			case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/connections/") && strings.HasSuffix(r.URL.Path, "/message"):
				s.handleConnectionSendMessage(w, r)
				return
			case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/connections/") && strings.HasSuffix(r.URL.Path, "/messages"):
				s.handleConnectionListMessages(w, r)
				return
			case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/connections/") && strings.HasSuffix(r.URL.Path, "/files"):
				s.handleConnectionReadFiles(w, r)
				return
			case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/connections/") && strings.HasSuffix(r.URL.Path, "/files"):
				s.handleConnectionWriteFiles(w, r)
				return
			}
		}
		proxy.ServeHTTP(w, r)
	})
}

func writeJSONResponse(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// proxyIPCJSON forwards a request to crewshipd over IPC with a 15s timeout,
// decodes the JSON response, and writes it back to the client. The label is
// used in error messages (e.g. "issue create" → "issue create request failed").
// If body is nil, no request body is sent.
func (s *Server) proxyIPCJSON(w http.ResponseWriter, r *http.Request, method, path, label string, body []byte) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, s.ipc.BaseURL+path, reqBody)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to create request"})
		return
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	httpReq.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(httpReq)
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{
			"error": fmt.Sprintf("%s request failed: %v", label, err),
		})
		return
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "invalid response from crewshipd"})
		return
	}

	writeJSONResponse(w, resp.StatusCode, result)
}

// CredStore returns the credential store for external updates.
func (s *Server) CredStore() *CredStore {
	return s.credStore
}

// Allowlist returns the domain allowlist for external modifications.
func (s *Server) Allowlist() *DomainAllowlist {
	return s.allowlist
}

// Ready returns a channel that is closed once the TCP listener is bound
// and the server is accepting connections. Use this to gate readiness signals.
func (s *Server) Ready() <-chan struct{} {
	return s.readyCh
}

// Start begins listening. Blocks until context is cancelled or an error occurs.
// The listener is always closed: either via Shutdown (context cancel) or on Serve error.
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("sidecar listen: %w", err)
	}

	// Update Addr to reflect the actual port (useful when Addr was ":0")
	s.httpServer.Addr = ln.Addr().String()

	// Signal that the listener is bound and we're ready to accept connections.
	close(s.readyCh)

	s.logger.Info("sidecar proxy started",
		"addr", s.httpServer.Addr,
		"anthropic_creds", s.credStore.Count(ProviderAnthropic),
		"openai_creds", s.credStore.Count(ProviderOpenAI),
	)

	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.Serve(ln); err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	// Periodic reindex for crew shared memory — catches updates from other agents
	// writing to /crew/shared/.memory/ via filesystem. 60s interval balances freshness
	// vs SQLite write cost. Only the lead sidecar runs this (owns the index).
	var crewReindexDone chan struct{}
	if s.crewMemoryEngine != nil {
		crewReindexDone = make(chan struct{})
		go func() {
			defer close(crewReindexDone)
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := s.crewMemoryEngine.Reindex(); err != nil {
						s.logger.Warn("crew memory periodic reindex failed", "error", err)
					}
				}
			}
		}()
	}

	// Connect to MCP servers in the background (don't block startup)
	if s.mcpGateway != nil {
		go func() {
			startupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if err := s.mcpGateway.Connect(startupCtx); err != nil {
				s.logger.Warn("MCP gateway partial connect failure", "error", err)
			}
			if _, err := s.mcpGateway.DiscoverTools(startupCtx); err != nil {
				s.logger.Warn("MCP gateway tool discovery failed", "error", err)
			}
		}()
	}

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if s.mcpGateway != nil {
			s.mcpGateway.Close()
		}
		if s.memoryEngine != nil {
			s.memoryEngine.Close()
		}
		if crewReindexDone != nil {
			<-crewReindexDone // wait for reindex goroutine to finish
		}
		if s.crewMemoryEngine != nil {
			s.crewMemoryEngine.Close()
		}
		if err := s.httpServer.Shutdown(shutCtx); err != nil {
			s.httpServer.Close()
			return fmt.Errorf("sidecar shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		if s.mcpGateway != nil {
			s.mcpGateway.Close()
		}
		if s.memoryEngine != nil {
			s.memoryEngine.Close()
		}
		if crewReindexDone != nil {
			<-crewReindexDone // wait for reindex goroutine to finish
		}
		if s.crewMemoryEngine != nil {
			s.crewMemoryEngine.Close()
		}
		return err
	}
}
