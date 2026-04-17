package llmproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// ProviderType identifies an LLM provider (Anthropic, OpenAI, Google).
type ProviderType string

const (
	ProviderAnthropic ProviderType = "ANTHROPIC"
	ProviderOpenAI    ProviderType = "OPENAI"
	ProviderGoogle    ProviderType = "GOOGLE"
)

// ConnectionStatus represents the health state of a provider credential.
type ConnectionStatus string

const (
	StatusActive      ConnectionStatus = "ACTIVE"
	StatusRateLimited ConnectionStatus = "RATE_LIMITED"
	StatusExpired     ConnectionStatus = "EXPIRED"
	StatusRevoked     ConnectionStatus = "REVOKED"
	StatusError       ConnectionStatus = "ERROR"
)

// CredentialType distinguishes between OAuth CLI tokens and static API keys.
type CredentialType string

const (
	TypeAICLIToken CredentialType = "AI_CLI_TOKEN"
	TypeAPIKey     CredentialType = "API_KEY"
)

// ProviderConnection represents a workspace's connection to an LLM provider,
// including the credential, its status, and optional OAuth refresh token.
type ProviderConnection struct {
	ID             string           `json:"id"`
	WorkspaceID    string           `json:"workspace_id"`
	Name           string           `json:"name"`
	Type           CredentialType   `json:"type"`
	Provider       ProviderType     `json:"provider"`
	AccountLabel   string           `json:"account_label"`
	AccessToken    string           `json:"access_token"`
	RefreshToken   string           `json:"refresh_token"`
	TokenExpiresAt *time.Time       `json:"token_expires_at"`
	Status         ConnectionStatus `json:"status"`
}

// rrKey is the composite key for round-robin token selection — used as a map
// key so we don't allocate a concatenated string on every SelectToken call.
type rrKey struct {
	workspaceID string
	provider    ProviderType
}

// TokenPool manages provider connection tokens fetched from Next.js
type TokenPool struct {
	mu          sync.RWMutex
	connections []ProviderConnection
	roundRobin  map[rrKey]int // (workspaceID, provider) -> next index
	logger      *slog.Logger
}

// NewTokenPool creates an empty TokenPool.
func NewTokenPool(logger *slog.Logger) *TokenPool {
	return &TokenPool{
		roundRobin: make(map[rrKey]int),
		logger:     logger,
	}
}

// Update replaces all connections in the pool with the given set.
func (tp *TokenPool) Update(connections []ProviderConnection) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.connections = connections
	tp.logger.Info("token pool updated", "count", len(connections))
}

// SelectToken picks the next active token for a given org+provider using round-robin.
func (tp *TokenPool) SelectToken(workspaceID string, provider ProviderType) *ProviderConnection {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	// First pass: count eligible connections without allocating a candidates
	// slice. Second pass below picks the target directly.
	count := 0
	for i := range tp.connections {
		c := &tp.connections[i]
		if c.WorkspaceID == workspaceID && c.Provider == provider && c.Status == StatusActive {
			count++
		}
	}
	if count == 0 {
		return nil
	}

	key := rrKey{workspaceID: workspaceID, provider: provider}
	target := tp.roundRobin[key] % count
	tp.roundRobin[key] = target + 1

	seen := 0
	for i := range tp.connections {
		c := &tp.connections[i]
		if c.WorkspaceID == workspaceID && c.Provider == provider && c.Status == StatusActive {
			if seen == target {
				result := *c
				return &result
			}
			seen++
		}
	}
	return nil // unreachable: count > 0 guarantees a match above.
}

// MarkStatus updates a connection's status in the pool.
func (tp *TokenPool) MarkStatus(connID string, status ConnectionStatus) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	for i := range tp.connections {
		if tp.connections[i].ID == connID {
			tp.connections[i].Status = status
			break
		}
	}
}

// ActiveCount returns the number of active connections for a workspace/provider pair.
func (tp *TokenPool) ActiveCount(workspaceID string, provider ProviderType) int {
	tp.mu.RLock()
	defer tp.mu.RUnlock()
	count := 0
	for _, conn := range tp.connections {
		if conn.WorkspaceID == workspaceID && conn.Provider == provider && conn.Status == StatusActive {
			count++
		}
	}
	return count
}

// AllConnections returns a copy of all connections in the pool.
func (tp *TokenPool) AllConnections() []ProviderConnection {
	tp.mu.RLock()
	defer tp.mu.RUnlock()
	result := make([]ProviderConnection, len(tp.connections))
	copy(result, tp.connections)
	return result
}

// TokenSyncer periodically fetches tokens from Next.js internal API.
type TokenSyncer struct {
	pool          *TokenPool
	nextjsURL     string
	internalToken string
	interval      time.Duration
	client        *http.Client
	logger        *slog.Logger
}

// NewTokenSyncer creates a TokenSyncer that periodically fetches credentials
// from the internal API and updates the token pool.
func NewTokenSyncer(pool *TokenPool, nextjsURL, internalToken string, interval time.Duration, logger *slog.Logger) *TokenSyncer {
	return &TokenSyncer{
		pool:          pool,
		nextjsURL:     nextjsURL,
		internalToken: internalToken,
		interval:      interval,
		client:        &http.Client{Timeout: 10 * time.Second},
		logger:        logger,
	}
}

// Run starts the sync loop, blocking until ctx is cancelled.
func (ts *TokenSyncer) Run(ctx context.Context) {
	ts.logger.Info("token syncer starting", "interval", ts.interval)

	if err := ts.sync(ctx); err != nil {
		ts.logger.Warn("initial token sync failed", "error", err)
	}

	ticker := time.NewTicker(ts.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			ts.logger.Info("token syncer stopped")
			return
		case <-ticker.C:
			if err := ts.sync(ctx); err != nil {
				ts.logger.Warn("token sync failed", "error", err)
			}
		}
	}
}

// SyncNow immediately fetches and updates credentials from the internal API.
func (ts *TokenSyncer) SyncNow(ctx context.Context) error {
	return ts.sync(ctx)
}

func (ts *TokenSyncer) sync(ctx context.Context) error {
	url := ts.nextjsURL + "/api/v1/internal/credentials"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("X-Internal-Token", ts.internalToken)

	resp, err := ts.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch tokens: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var connections []ProviderConnection
	if err := json.NewDecoder(resp.Body).Decode(&connections); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	ts.pool.Update(connections)
	return nil
}
