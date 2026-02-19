package sidecar

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/scrubber"
)

const DefaultAddr = "127.0.0.1:9119"

// ServerConfig configures the sidecar server.
type ServerConfig struct {
	Addr             string   // listen address (default: 127.0.0.1:9119)
	AllowedDomains   []string // extra allowed domains beyond defaults
	Credentials      []Credential
	Logger           *slog.Logger
}

// Server is the crewship sidecar that runs inside agent containers.
// It provides an HTTP forward proxy with credential injection.
type Server struct {
	httpServer *http.Server
	credStore  *CredStore
	allowlist  *DomainAllowlist
	proxy      *Proxy
	logger     *slog.Logger
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

	return &Server{
		httpServer: &http.Server{
			Addr:              cfg.Addr,
			Handler:           proxy,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		},
		credStore: credStore,
		allowlist: allowlist,
		proxy:     proxy,
		logger:    cfg.Logger,
	}
}

// CredStore returns the credential store for external updates.
func (s *Server) CredStore() *CredStore {
	return s.credStore
}

// Allowlist returns the domain allowlist for external modifications.
func (s *Server) Allowlist() *DomainAllowlist {
	return s.allowlist
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
		if err := s.httpServer.Shutdown(shutCtx); err != nil {
			// Shutdown failed; force close to release the listener
			s.httpServer.Close()
			return fmt.Errorf("sidecar shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		return err
	}
}
