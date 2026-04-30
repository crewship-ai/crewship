package llmproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// StatusUpdate is sent to Next.js internal API to persist status changes.
type StatusUpdate struct {
	Status    ConnectionStatus `json:"status"`
	LastError *string          `json:"last_error"`
}

// onChangeFunc captures the registered status-change callback so we can
// store/load it atomically — a plain function field would race between
// the goroutine running Run() and any caller invoking SetOnChange after
// monitoring starts.
type onChangeFunc func(connID string, oldStatus, newStatus ConnectionStatus)

// CredentialMonitor periodically validates provider credentials.
type CredentialMonitor struct {
	pool          *TokenPool
	nextjsURL     string
	internalToken string
	interval      time.Duration
	client        *http.Client
	logger        *slog.Logger
	onChange      atomic.Pointer[onChangeFunc]
}

// NewCredentialMonitor creates a monitor that periodically validates provider
// credentials and updates their status in the pool and database.
func NewCredentialMonitor(
	pool *TokenPool,
	nextjsURL, internalToken string,
	interval time.Duration,
	logger *slog.Logger,
) *CredentialMonitor {
	return &CredentialMonitor{
		pool:          pool,
		nextjsURL:     nextjsURL,
		internalToken: internalToken,
		interval:      interval,
		client:        &http.Client{Timeout: 15 * time.Second},
		logger:        logger,
	}
}

// SetOnChange registers a callback invoked when a credential's status changes.
// Safe to call before or after Run() has started; the swap is atomic.
func (cm *CredentialMonitor) SetOnChange(fn func(connID string, oldStatus, newStatus ConnectionStatus)) {
	if fn == nil {
		cm.onChange.Store(nil)
		return
	}
	wrapped := onChangeFunc(fn)
	cm.onChange.Store(&wrapped)
}

// Run starts the credential validation loop, blocking until ctx is cancelled.
func (cm *CredentialMonitor) Run(ctx context.Context) {
	cm.logger.Info("credential monitor starting", "interval", cm.interval)

	ticker := time.NewTicker(cm.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			cm.logger.Info("credential monitor stopped")
			return
		case <-ticker.C:
			cm.checkAll(ctx)
		}
	}
}

func (cm *CredentialMonitor) checkAll(ctx context.Context) {
	connections := cm.pool.AllConnections()
	for _, conn := range connections {
		if conn.Status == StatusRevoked {
			continue
		}
		// Skip health checks for OAuth tokens -- they cannot be validated
		// via standard API endpoints (/v1/models). Detect by type or prefix.
		if conn.Type == TypeAICLIToken || strings.HasPrefix(conn.AccessToken, "sk-ant-oat") {
			// If an OAuth token is stored as API_KEY and currently EXPIRED
			// from a previous (incorrect) validation, reset it to ACTIVE.
			if conn.Status == StatusExpired && strings.HasPrefix(conn.AccessToken, "sk-ant-oat") {
				cm.pool.MarkStatus(conn.ID, StatusActive)
				cm.persistStatus(ctx, conn.ID, StatusActive, "")
				cm.logger.Info("oauth token reset to active (cannot validate via API)",
					"connection_id", conn.ID, "provider", conn.Provider)
			}
			continue
		}
		cm.checkOne(ctx, conn)
	}
}

func (cm *CredentialMonitor) checkOne(ctx context.Context, conn ProviderConnection) {
	oldStatus := conn.Status
	newStatus, errMsg := cm.validate(ctx, conn)

	if newStatus == oldStatus {
		return
	}

	cm.pool.MarkStatus(conn.ID, newStatus)
	cm.logger.Info("credential status changed",
		"connection_id", conn.ID,
		"provider", conn.Provider,
		"old_status", oldStatus,
		"new_status", newStatus,
	)

	cm.persistStatus(ctx, conn.ID, newStatus, errMsg)

	if cb := cm.onChange.Load(); cb != nil {
		(*cb)(conn.ID, oldStatus, newStatus)
	}
}

func (cm *CredentialMonitor) validate(ctx context.Context, conn ProviderConnection) (ConnectionStatus, string) {
	switch conn.Provider {
	case ProviderAnthropic:
		return cm.validateAnthropic(ctx, conn)
	default:
		return conn.Status, ""
	}
}

func (cm *CredentialMonitor) validateAnthropic(ctx context.Context, conn ProviderConnection) (ConnectionStatus, string) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		return StatusError, fmt.Sprintf("create request: %v", err)
	}
	// OAuth tokens (sk-ant-oat*) use Bearer auth regardless of stored type;
	// this handles the case where a user stores an OAuth token as API_KEY.
	if conn.Type == TypeAICLIToken || strings.HasPrefix(conn.AccessToken, "sk-ant-oat") {
		req.Header.Set("Authorization", "Bearer "+conn.AccessToken)
	} else {
		req.Header.Set("x-api-key", conn.AccessToken)
	}
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := cm.client.Do(req)
	if err != nil {
		return StatusError, fmt.Sprintf("request failed: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		return StatusActive, ""
	case http.StatusUnauthorized:
		return StatusExpired, "Authentication failed (401)"
	case http.StatusForbidden:
		return StatusRevoked, "Access revoked (403)"
	case http.StatusTooManyRequests:
		return StatusRateLimited, "Rate limited (429)"
	default:
		return StatusError, fmt.Sprintf("Unexpected status: %d", resp.StatusCode)
	}
}

func (cm *CredentialMonitor) persistStatus(ctx context.Context, connID string, status ConnectionStatus, errMsg string) {
	update := StatusUpdate{Status: status}
	if errMsg != "" {
		update.LastError = &errMsg
	}

	body, err := json.Marshal(update)
	if err != nil {
		cm.logger.Error("marshal status update failed", "error", err)
		return
	}
	url := fmt.Sprintf("%s/api/v1/internal/credentials/%s", cm.nextjsURL, connID)

	req, err := http.NewRequestWithContext(ctx, "PATCH", url, bytes.NewReader(body))
	if err != nil {
		cm.logger.Error("create status update request failed", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", cm.internalToken)

	resp, err := cm.client.Do(req)
	if err != nil {
		cm.logger.Error("status update request failed", "error", err)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		cm.logger.Warn("status update returned non-200", "status", resp.StatusCode, "connection_id", connID)
	}
}
