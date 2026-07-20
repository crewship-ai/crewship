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
	"sync"
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

// validationOutcome records *why* a credential is in the status it is in.
//
// The ConnectionStatus alone cannot answer the only question that matters
// before resurrecting a credential: did anybody actually ask the provider?
// EXPIRED means "not usable right now" and is written by several different
// paths (the OAuth refresh worker, the sidecar's status PATCH, this monitor)
// — some of which are proof the provider rejected the token, and some of
// which are just "we have not validated this recently". Conflating the two is
// what let a revoked token be flipped back to ACTIVE on every tick.
type validationOutcome int

const (
	// outcomeUnknown: this monitor has never validated the credential. Its
	// status came from somewhere else and is not, on its own, proof of death.
	outcomeUnknown validationOutcome = iota
	// outcomeOK: the provider accepted the credential.
	outcomeOK
	// outcomeTransient: we failed to get an answer (DNS, timeout, 5xx, 429).
	// This must never harden into a permanent kill — a flaky network is not a
	// revoked token.
	outcomeTransient
	// outcomeRejected: the provider answered, and the answer was "no"
	// (401/403). This is the terminal signal: the credential is dead until a
	// human or the OAuth flow replaces it.
	outcomeRejected
)

// CredentialMonitor periodically validates provider credentials.
type CredentialMonitor struct {
	pool          *TokenPool
	nextjsURL     string
	internalToken string
	interval      time.Duration
	client        *http.Client
	logger        *slog.Logger
	onChange      atomic.Pointer[onChangeFunc]

	// Validation endpoints. Fields (not constants) so tests can point them at
	// an httptest server and exercise the real request → status-mapping →
	// outcome-recording path instead of asserting on a stubbed-out shape.
	anthropicModelsURL string
	openaiModelsURL    string
	googleModelsURL    string

	// outcomes records the last validationOutcome observed per connection ID.
	// In-memory and process-local by design: it describes what THIS process
	// observed, and a restart correctly resets us to outcomeUnknown (we have
	// not validated anything yet) rather than to a stale verdict.
	outcomesMu sync.Mutex
	outcomes   map[string]validationOutcome
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

		anthropicModelsURL: "https://api.anthropic.com/v1/models",
		openaiModelsURL:    "https://api.openai.com/v1/models",
		googleModelsURL:    "https://generativelanguage.googleapis.com/v1beta/models",

		outcomes: make(map[string]validationOutcome),
	}
}

// recordOutcome stores the outcome implied by a validation result.
func (cm *CredentialMonitor) recordOutcome(connID string, status ConnectionStatus) {
	var outcome validationOutcome
	switch status {
	case StatusActive:
		outcome = outcomeOK
	case StatusExpired, StatusRevoked:
		// The provider authenticated the request and refused it.
		outcome = outcomeRejected
	default:
		// StatusError / StatusRateLimited: we never got a verdict on the
		// credential itself.
		outcome = outcomeTransient
	}
	cm.outcomesMu.Lock()
	cm.outcomes[connID] = outcome
	cm.outcomesMu.Unlock()
}

// NoteAuthFailure records that a provider rejected this credential's
// authentication (401/403) on a live request. Callers on the request path can
// use it so a rejection observed outside the monitor's own polling still
// counts as a validated rejection.
func (cm *CredentialMonitor) NoteAuthFailure(connID string) {
	cm.outcomesMu.Lock()
	cm.outcomes[connID] = outcomeRejected
	cm.outcomesMu.Unlock()
}

// lastOutcome returns the last recorded outcome for a connection.
func (cm *CredentialMonitor) lastOutcome(connID string) validationOutcome {
	cm.outcomesMu.Lock()
	defer cm.outcomesMu.Unlock()
	return cm.outcomes[connID]
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
			// from a previous (incorrect) validation, reset it to ACTIVE —
			// but ONLY when nothing has actually validated it and found it
			// dead. See resurrectable: a real 401/403, or an elapsed token
			// expiry, means the token is genuinely gone and flipping it back
			// to ACTIVE would hand a revoked secret to an agent.
			if conn.Status == StatusExpired && strings.HasPrefix(conn.AccessToken, "sk-ant-oat") {
				if !cm.resurrectable(conn) {
					cm.logger.Info("oauth token left expired (validated and rejected)",
						"connection_id", conn.ID, "provider", conn.Provider)
					continue
				}
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

// resurrectable reports whether an EXPIRED OAuth credential may be flipped
// back to ACTIVE. It answers "has anything actually validated this and found
// it dead?" — the distinction the raw ConnectionStatus cannot express.
//
// Not resurrectable when:
//   - a validation (poll or live request) got an authenticated rejection, or
//   - the credential's own recorded expiry has already elapsed.
//
// Resurrectable when nothing has validated it (outcomeUnknown) or the last
// attempt failed transiently — a DNS blip must never become a permanent kill.
func (cm *CredentialMonitor) resurrectable(conn ProviderConnection) bool {
	if cm.lastOutcome(conn.ID) == outcomeRejected {
		return false
	}
	if conn.TokenExpiresAt != nil && !conn.TokenExpiresAt.After(time.Now()) {
		return false
	}
	return true
}

func (cm *CredentialMonitor) checkOne(ctx context.Context, conn ProviderConnection) {
	oldStatus := conn.Status
	newStatus, errMsg, validated := cm.validate(ctx, conn)
	if !validated {
		// Unsupported provider: we asked nobody, so we learned nothing. Do
		// not let the echoed-back status masquerade as a verdict.
		return
	}

	// Record the outcome before the unchanged-status short circuit: a
	// credential that is already EXPIRED and 401s again on every tick still
	// has to count as "validated and rejected", otherwise the rejection is
	// only ever remembered on the single tick where the status flipped.
	cm.recordOutcome(conn.ID, newStatus)

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

// validate probes the provider. The third return value reports whether a
// probe actually happened — false means the provider is unsupported and the
// returned status is just the caller's own status echoed back, which must not
// be recorded as a validation outcome.
func (cm *CredentialMonitor) validate(ctx context.Context, conn ProviderConnection) (ConnectionStatus, string, bool) {
	switch conn.Provider {
	case ProviderAnthropic:
		status, errMsg := cm.validateAnthropic(ctx, conn)
		return status, errMsg, true
	case ProviderOpenAI:
		status, errMsg := cm.validateOpenAI(ctx, conn)
		return status, errMsg, true
	case ProviderGoogle:
		status, errMsg := cm.validateGoogle(ctx, conn)
		return status, errMsg, true
	default:
		return conn.Status, "", false
	}
}

// validateEndpoint issues a GET to a provider's model-listing endpoint and maps
// the HTTP response to a ConnectionStatus. The setAuth callback installs the
// provider-specific authentication (and any other) headers on the request. This
// centralizes the request/timeout/status-mapping logic shared by every provider
// validator; per-provider auth differences live entirely in setAuth.
func (cm *CredentialMonitor) validateEndpoint(ctx context.Context, url string, setAuth func(*http.Request)) (ConnectionStatus, string) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return StatusError, fmt.Sprintf("create request: %v", err)
	}
	setAuth(req)

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

func (cm *CredentialMonitor) validateAnthropic(ctx context.Context, conn ProviderConnection) (ConnectionStatus, string) {
	return cm.validateEndpoint(ctx, cm.anthropicModelsURL, func(req *http.Request) {
		// OAuth tokens (sk-ant-oat*) use Bearer auth regardless of stored type;
		// this handles the case where a user stores an OAuth token as API_KEY.
		if conn.Type == TypeAICLIToken || strings.HasPrefix(conn.AccessToken, "sk-ant-oat") {
			req.Header.Set("Authorization", "Bearer "+conn.AccessToken)
		} else {
			req.Header.Set("x-api-key", conn.AccessToken)
		}
		req.Header.Set("anthropic-version", "2023-06-01")
	})
}

func (cm *CredentialMonitor) validateOpenAI(ctx context.Context, conn ProviderConnection) (ConnectionStatus, string) {
	return cm.validateEndpoint(ctx, cm.openaiModelsURL, func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+conn.AccessToken)
	})
}

func (cm *CredentialMonitor) validateGoogle(ctx context.Context, conn ProviderConnection) (ConnectionStatus, string) {
	// Gemini AI Studio (not Vertex): pass the key via the x-goog-api-key header
	// rather than the ?key= query param to keep it out of the URL/logs.
	return cm.validateEndpoint(ctx, cm.googleModelsURL, func(req *http.Request) {
		req.Header.Set("x-goog-api-key", conn.AccessToken)
	})
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
