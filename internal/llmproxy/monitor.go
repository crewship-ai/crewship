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

	// Validation endpoints. Fields (not constants) so tests can point them at
	// an httptest server and exercise the real request → status-mapping →
	// persist path instead of asserting on a stubbed-out shape.
	anthropicModelsURL string
	openaiModelsURL    string
	googleModelsURL    string
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
		//
		// "Skip" means exactly that: observe nothing, change nothing. This
		// loop used to flip an EXPIRED sk-ant-oat credential back to ACTIVE
		// here and PATCH that ACTIVE to the database — a process that had
		// just decided it cannot validate the credential writing "healthy"
		// about it anyway. #1277 tried to gate the flip on "has anything
		// validated this and found it dead?", but the only writer of that
		// signal was checkOne, which this branch `continue`s past, so the
		// gate's answer was permanently "nothing validated it" = resurrect.
		// A guard that cannot fire is worse than no guard: it reads as
		// protection. The flip is gone instead.
		//
		// Recovery for a genuinely-healthy OAuth token that was marked
		// EXPIRED is a RE-LINK, and today that is the only path. The
		// PATCH /api/v1/internal/credentials/{id} status endpoint has
		// exactly one caller — persistStatus below — so nothing outside
		// this monitor can move a credential back to ACTIVE. Rows left
		// EXPIRED by an earlier incorrect validation therefore stay
		// EXPIRED instead of silently self-healing, which is why the log
		// below is Info and names the remedy rather than being a Debug
		// line nobody reads.
		if conn.Type == TypeAICLIToken || strings.HasPrefix(conn.AccessToken, "sk-ant-oat") {
			if conn.Status == StatusExpired {
				cm.logger.Info("oauth credential is expired and cannot be re-validated automatically; re-link it to restore access",
					"connection_id", conn.ID, "provider", conn.Provider)
			}
			continue
		}
		cm.checkOne(ctx, conn)
	}
}

func (cm *CredentialMonitor) checkOne(ctx context.Context, conn ProviderConnection) {
	oldStatus := conn.Status
	newStatus, errMsg, validated := cm.validate(ctx, conn)
	if !validated {
		// Unsupported provider: we asked nobody, so we learned nothing. Do
		// not let the echoed-back status masquerade as a verdict.
		return
	}

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
// be mistaken for a verdict on the credential.
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
