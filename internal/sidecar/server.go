package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

const DefaultAddr = "127.0.0.1:9119"

// MemoryConfig configures the sidecar memory engine.
type MemoryConfig struct {
	Enabled   bool   `json:"enabled"`
	BasePath  string `json:"base_path"`
	AgentSlug string `json:"agent_slug"`
}

// IPCConfig holds the crewshipd internal API address for assignment forwarding.
// Lead agents use this to POST assignment requests through the sidecar to crewshipd.
type IPCConfig struct {
	BaseURL     string `json:"base_url"`
	Token       string `json:"token"`
	CrewID      string `json:"crew_id"`
	WorkspaceID string `json:"workspace_id"`
	ChatID      string `json:"chat_id"`
}

// CrewMember describes a crew member accessible for lead assignment routing.
type CrewMember struct {
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	RoleTitle string `json:"role_title"`
}

// ServerConfig configures the sidecar server.
type ServerConfig struct {
	Addr           string   // listen address (default: 127.0.0.1:9119)
	AllowedDomains []string // extra allowed domains beyond defaults
	Credentials    []Credential
	Memory         *MemoryConfig
	IPC            *IPCConfig
	CrewMembers    []CrewMember
	Logger         *slog.Logger
}

// Server is the crewship sidecar that runs inside agent containers.
// It provides an HTTP forward proxy with credential injection,
// optional memory search API, and assignment routing for lead agents.
type Server struct {
	httpServer   *http.Server
	credStore    *CredStore
	allowlist    *DomainAllowlist
	proxy        *Proxy
	memoryEngine *memory.Engine
	ipc          *IPCConfig
	crewMembers  []CrewMember
	logger       *slog.Logger
	readyCh      chan struct{} // closed when the TCP listener is bound
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
	allowlist := NewDomainAllowlist(domains)

	proxy := NewProxy(ProxyConfig{
		CredStore: credStore,
		Allowlist: allowlist,
		Scrubber:  scrubber.New(),
		Logger:    cfg.Logger,
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
	}

	// Use ServeMux to route local API requests while keeping the
	// forward proxy as the default handler for external traffic.
	// The proxy's handleLocal already handles /health, but we register
	// explicit routes here for the memory API.
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

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if s.memoryEngine != nil {
			s.memoryEngine.Close()
		}
		if err := s.httpServer.Shutdown(shutCtx); err != nil {
			// Shutdown failed; force close to release the listener
			s.httpServer.Close()
			return fmt.Errorf("sidecar shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		if s.memoryEngine != nil {
			s.memoryEngine.Close()
		}
		return err
	}
}
