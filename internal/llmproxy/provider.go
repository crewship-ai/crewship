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

type ProviderType string

const (
	ProviderAnthropic ProviderType = "ANTHROPIC"
	ProviderOpenAI    ProviderType = "OPENAI"
	ProviderGoogle    ProviderType = "GOOGLE"
)

type ConnectionStatus string

const (
	StatusActive      ConnectionStatus = "ACTIVE"
	StatusRateLimited ConnectionStatus = "RATE_LIMITED"
	StatusExpired     ConnectionStatus = "EXPIRED"
	StatusRevoked     ConnectionStatus = "REVOKED"
	StatusError       ConnectionStatus = "ERROR"
)

type CredentialType string

const (
	TypeAICLIToken CredentialType = "AI_CLI_TOKEN"
	TypeAPIKey     CredentialType = "API_KEY"
)

type ProviderConnection struct {
	ID             string           `json:"id"`
	WorkspaceID          string           `json:"workspace_id"`
	Name           string           `json:"name"`
	Type           CredentialType   `json:"type"`
	Provider       ProviderType     `json:"provider"`
	AccountLabel   string           `json:"account_label"`
	AccessToken    string           `json:"access_token"`
	RefreshToken   string           `json:"refresh_token"`
	TokenExpiresAt *time.Time       `json:"token_expires_at"`
	Status         ConnectionStatus `json:"status"`
}

// TokenPool manages provider connection tokens fetched from Next.js
type TokenPool struct {
	mu          sync.RWMutex
	connections []ProviderConnection
	roundRobin  map[string]int // workspaceID+provider -> index
	logger      *slog.Logger
}

func NewTokenPool(logger *slog.Logger) *TokenPool {
	return &TokenPool{
		roundRobin: make(map[string]int),
		logger:     logger,
	}
}

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

	key := workspaceID + ":" + string(provider)
	var candidates []int
	for i, conn := range tp.connections {
		if conn.WorkspaceID == workspaceID && conn.Provider == provider && conn.Status == StatusActive {
			candidates = append(candidates, i)
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	idx := tp.roundRobin[key] % len(candidates)
	tp.roundRobin[key] = idx + 1

	result := tp.connections[candidates[idx]]
	return &result
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
