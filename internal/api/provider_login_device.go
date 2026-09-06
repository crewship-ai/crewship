package api

// Device-code sign-in to a model provider (docs/prd/provider-logins.md §5.6
// v2, §10.3). The person asks for a code, types it into the provider's
// browser page, and the SERVER — not a codex binary, not the CLI — polls the
// provider until the login is granted, assembles the login file and stores
// it as a credential through the same path POST /api/v1/credentials takes.
//
//	POST /api/v1/provider-logins/device            {provider, mode?}
//	  → {device_id, user_code, verification_url, expires_at, interval_s}
//	GET  /api/v1/provider-logins/device/{deviceId}
//	  → {status: pending|complete|expired|denied, credential_id?}
//
// Why the server polls. The CLI's own request timeout is 30 s and a browser
// step takes minutes; the dashboard closes tabs; both would otherwise have
// to hold the provider's polling contract themselves. One goroutine per
// pending sign-in, persisted in provider_device_logins so a restart resumes
// it rather than losing a code somebody is halfway through typing.
//
// Only OpenAI (Codex) today — codexauth.DeviceClient is the protocol; the
// provider column is the seam the next one plugs into.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/crewship-ai/crewship/internal/codexauth"
)

// deviceAuthorizer is the provider's side of the flow. codexauth.DeviceClient
// is the real one; tests point it at a fake issuer (codexauthtest) so nothing
// here ever dials auth.openai.com from a test.
type deviceAuthorizer interface {
	Start(ctx context.Context) (codexauth.DeviceStart, error)
	Poll(ctx context.Context, deviceAuthID, userCode string) (codexauth.DevicePoll, error)
	Exchange(ctx context.Context, code, verifier string) (codexauth.File, error)
}

const (
	deviceLoginStatusPending  = "pending"
	deviceLoginStatusComplete = "complete"
	deviceLoginStatusExpired  = "expired"
	deviceLoginStatusDenied   = "denied"

	// deviceLoginStartsPer10Min bounds how many sign-ins one user can start:
	// each one is a request to the provider on our behalf and a row to poll.
	deviceLoginStartsPer10Min = 5

	// deviceLoginCallTimeout bounds each call to the provider.
	deviceLoginCallTimeout = 30 * time.Second
)

// ProviderLoginHandler owns the two endpoints and the pollers.
type ProviderLoginHandler struct {
	db     *sql.DB
	logger *slog.Logger
	// creds is the credential create path — the handler behind
	// POST /api/v1/credentials — so a device sign-in produces exactly the
	// row a pasted login would, validation, audit event and all.
	creds  *CredentialHandler
	openai deviceAuthorizer

	limiter *deviceStartLimiter

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	// running is the set of device-login ids with a poller in THIS process,
	// so a boot resume cannot start a second poller for a row that already
	// has one — two pollers would exchange the code twice.
	running map[string]bool

	// pollInterval, when set, replaces the provider's interval (tests).
	pollInterval time.Duration
	now          func() time.Time
}

// NewProviderLoginHandler wires the handler. openai nil means the real
// codexauth.DeviceClient against auth.openai.com.
func NewProviderLoginHandler(db *sql.DB, logger *slog.Logger, creds *CredentialHandler, openai deviceAuthorizer) *ProviderLoginHandler {
	if openai == nil {
		openai = codexauth.NewDeviceClient("", nil)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &ProviderLoginHandler{
		db: db, logger: logger, creds: creds, openai: openai,
		limiter: newDeviceStartLimiter(),
		ctx:     ctx, cancel: cancel,
		running: map[string]bool{},
		now:     func() time.Time { return time.Now().UTC() },
	}
}

// Stop ends every poller and waits for them. A row left pending is resumed
// by the next process.
func (h *ProviderLoginHandler) Stop() {
	h.cancel()
	h.wg.Wait()
}

type deviceLoginStartRequest struct {
	Provider string `json:"provider"`
	Mode     string `json:"mode"`
}

type deviceLoginStartResponse struct {
	DeviceID        string `json:"device_id"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresAt       string `json:"expires_at"`
	IntervalS       int    `json:"interval_s"`
}

type deviceLoginStatusResponse struct {
	Status       string  `json:"status"`
	CredentialID *string `json:"credential_id,omitempty"`
	// The three below are additive to the §10.3 contract: a client that
	// lost its start response (a reopened tab) can still show the code.
	UserCode        string  `json:"user_code"`
	VerificationURL string  `json:"verification_url"`
	ExpiresAt       string  `json:"expires_at"`
	Error           *string `json:"error,omitempty"`
}

// Start begins a device sign-in for the caller in the current workspace.
// POST /api/v1/provider-logins/device
func (h *ProviderLoginHandler) Start(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	// The flow ends in a credential create, so it is gated exactly as the
	// create is — MANAGER+, or a MEMBER holding credential.create.
	if !requireRoleOrCapabilityOrForbid(w, r, h.logger, h.db,
		workspaceID, user.ID, role,
		CapabilityCredentialCreate, "credential.create", "workspace:"+workspaceID,
		"create") {
		return
	}

	var req deviceLoginStartRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	provider := strings.ToUpper(strings.TrimSpace(req.Provider))
	if provider != codexauth.ProviderID {
		replyError(w, http.StatusBadRequest, "device sign-in is available for provider OPENAI only")
		return
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "subscription"
	}
	if mode != "subscription" {
		replyError(w, http.StatusBadRequest, "device sign-in produces a subscription login; for an API key use POST /api/v1/credentials")
		return
	}

	if wait := h.limiter.Reserve(h.now(), user.ID); wait > 0 {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(wait.Seconds())+1))
		replyError(w, http.StatusTooManyRequests, "too many sign-ins started; try again in a minute")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), deviceLoginCallTimeout)
	defer cancel()
	start, err := h.openai.Start(ctx)
	if err != nil {
		if errors.Is(err, codexauth.ErrDeviceNotEnabled) {
			replyError(w, http.StatusBadGateway, "OpenAI does not offer device-code sign-in right now; paste ~/.codex/auth.json instead")
			return
		}
		h.logger.Warn("device login: provider start failed", "provider", provider, "error", err)
		replyError(w, http.StatusBadGateway, "could not start the sign-in with the provider: "+err.Error())
		return
	}

	now := h.now()
	expires := now.Add(codexauth.DeviceFlowTimeout)
	id := generateCUID()
	interval := start.Interval
	if interval <= 0 {
		interval = codexauth.DefaultPollInterval
	}
	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO provider_device_logins
			(id, workspace_id, user_id, provider, mode, device_auth_id, user_code, verification_url,
			 interval_s, status, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		id, workspaceID, user.ID, provider, mode, start.DeviceAuthID, start.UserCode, start.VerificationURL,
		int(interval.Seconds()), now.Format(time.RFC3339), expires.Format(time.RFC3339)); err != nil {
		replyInternalError(w, h.logger, "device login: insert", err)
		return
	}

	auditFromRequest(r, h.db, "provider_login.device.start", "PROVIDER_LOGIN", id, map[string]interface{}{
		"provider": provider, "mode": mode,
	})
	h.spawn(deviceLoginRow{
		ID: id, WorkspaceID: workspaceID, UserID: user.ID, Provider: provider,
		DeviceAuthID: start.DeviceAuthID, UserCode: start.UserCode,
		Interval: interval, ExpiresAt: expires,
	})

	writeJSON(w, http.StatusCreated, deviceLoginStartResponse{
		DeviceID:        id,
		UserCode:        start.UserCode,
		VerificationURL: start.VerificationURL,
		ExpiresAt:       expires.Format(time.RFC3339),
		IntervalS:       int(interval.Seconds()),
	})
}

// Status reports one sign-in. The caller must own the row; anything else is
// a 404, so the endpoint cannot enumerate other people's codes (ids are
// CUIDs, so the 404 leaks nothing a guess could use).
// GET /api/v1/provider-logins/device/{deviceId}
func (h *ProviderLoginHandler) Status(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	id := r.PathValue("deviceId")
	var status, userCode, verificationURL, expiresStr string
	var credentialID, errText sql.NullString
	err := h.db.QueryRowContext(r.Context(), `
		SELECT status, user_code, verification_url, expires_at, credential_id, error_text
		  FROM provider_device_logins WHERE id = ? AND user_id = ?`, id, user.ID).
		Scan(&status, &userCode, &verificationURL, &expiresStr, &credentialID, &errText)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Device sign-in not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "device login: status", err)
		return
	}
	if status == deviceLoginStatusPending {
		if expires, perr := time.Parse(time.RFC3339, expiresStr); perr == nil && h.now().After(expires) {
			// The poller flips this too; flipping it here keeps a status
			// read honest between two polls.
			h.finish(r.Context(), id, deviceLoginStatusExpired, "", "the code expired before it was entered")
			status = deviceLoginStatusExpired
			errText = sql.NullString{String: "the code expired before it was entered", Valid: true}
		}
	}
	resp := deviceLoginStatusResponse{
		Status: status, UserCode: userCode, VerificationURL: verificationURL, ExpiresAt: expiresStr,
	}
	if credentialID.Valid {
		resp.CredentialID = &credentialID.String
	}
	if errText.Valid && errText.String != "" {
		resp.Error = &errText.String
	}
	writeJSON(w, http.StatusOK, resp)
}

// deviceLoginRow is what a poller needs to know about its row.
type deviceLoginRow struct {
	ID, WorkspaceID, UserID, Provider string
	DeviceAuthID, UserCode            string
	Interval                          time.Duration
	ExpiresAt                         time.Time
}

// ResumePending picks up every pending row — after a restart — and expires
// the ones whose window has already closed. Safe to call more than once.
func (h *ProviderLoginHandler) ResumePending(ctx context.Context) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT id, workspace_id, user_id, provider, device_auth_id, user_code, interval_s, expires_at
		  FROM provider_device_logins WHERE status = 'pending' ORDER BY created_at`)
	if err != nil {
		h.logger.Warn("device login: resume query", "error", err)
		return
	}
	defer rows.Close()
	var resumed []deviceLoginRow
	for rows.Next() {
		var row deviceLoginRow
		var intervalS int
		var expiresStr string
		if err := rows.Scan(&row.ID, &row.WorkspaceID, &row.UserID, &row.Provider, &row.DeviceAuthID, &row.UserCode, &intervalS, &expiresStr); err != nil {
			h.logger.Warn("device login: resume scan", "error", err)
			continue
		}
		row.Interval = time.Duration(intervalS) * time.Second
		row.ExpiresAt, _ = time.Parse(time.RFC3339, expiresStr)
		resumed = append(resumed, row)
	}
	for _, row := range resumed {
		if !row.ExpiresAt.IsZero() && h.now().After(row.ExpiresAt) {
			h.finish(ctx, row.ID, deviceLoginStatusExpired, "", "the code expired while the server was down")
			continue
		}
		h.spawn(row)
	}
}

// spawn starts the poller for one row unless one is already running.
func (h *ProviderLoginHandler) spawn(row deviceLoginRow) {
	h.mu.Lock()
	if h.running[row.ID] {
		h.mu.Unlock()
		return
	}
	h.running[row.ID] = true
	h.mu.Unlock()
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		defer func() {
			h.mu.Lock()
			delete(h.running, row.ID)
			h.mu.Unlock()
		}()
		h.poll(row)
	}()
}

// poll is the provider's polling contract, run to the end of one sign-in:
// wait the interval, ask, and either wait again (with the provider's
// back-off when it asks for one), give up (denied / expired), or finish
// (exchange the code, create the credential).
func (h *ProviderLoginHandler) poll(row deviceLoginRow) {
	interval := row.Interval
	if h.pollInterval > 0 {
		interval = h.pollInterval
	}
	if interval <= 0 {
		interval = codexauth.DefaultPollInterval
	}
	wait := interval
	for {
		select {
		case <-h.ctx.Done():
			return // still pending in the DB; the next process resumes it
		case <-time.After(wait):
		}
		if h.now().After(row.ExpiresAt) {
			h.finish(h.ctx, row.ID, deviceLoginStatusExpired, "", "the code expired before it was entered")
			return
		}
		ctx, cancel := context.WithTimeout(h.ctx, deviceLoginCallTimeout)
		p, err := h.openai.Poll(ctx, row.DeviceAuthID, row.UserCode)
		cancel()
		if err != nil {
			if errors.Is(err, codexauth.ErrDeviceDenied) {
				h.finish(h.ctx, row.ID, deviceLoginStatusDenied, "", "the provider refused the sign-in: "+err.Error())
				return
			}
			// Transport trouble: keep the row, try again next interval.
			h.logger.Warn("device login: poll failed, retrying", "device_login_id", row.ID, "error", err)
			wait = interval
			continue
		}
		if !p.Authorized {
			wait = interval
			if p.RetryAfter > wait {
				wait = p.RetryAfter
			}
			continue
		}
		ctx, cancel = context.WithTimeout(h.ctx, deviceLoginCallTimeout)
		file, err := h.openai.Exchange(ctx, p.Code, p.Verifier)
		cancel()
		if err != nil {
			h.finish(h.ctx, row.ID, deviceLoginStatusDenied, "", "the sign-in was granted but the token exchange failed: "+err.Error())
			return
		}
		credID, err := h.createCredential(h.ctx, row, file)
		if err != nil {
			h.logger.Warn("device login: credential create failed", "device_login_id", row.ID, "error", err)
			h.finish(h.ctx, row.ID, deviceLoginStatusDenied, "", "signed in, but the credential could not be stored: "+err.Error())
			return
		}
		h.finish(h.ctx, row.ID, deviceLoginStatusComplete, credID, "")
		h.logger.Info("device login complete", "device_login_id", row.ID, "credential_id", credID, "provider", row.Provider)
		return
	}
}

// finish moves a pending row to a terminal status. The WHERE on 'pending'
// makes it idempotent: a poller and a Status read racing to expire the same
// row cannot flip a completed one.
func (h *ProviderLoginHandler) finish(ctx context.Context, id, status, credentialID, errText string) {
	var credArg, errArg any
	if credentialID != "" {
		credArg = credentialID
	}
	if errText != "" {
		errArg = errText
	}
	if _, err := h.db.ExecContext(ctx, `
		UPDATE provider_device_logins
		   SET status = ?, credential_id = ?, error_text = ?, completed_at = ?
		 WHERE id = ? AND status = 'pending'`,
		status, credArg, errArg, h.now().Format(time.RFC3339), id); err != nil {
		h.logger.Warn("device login: finish", "device_login_id", id, "status", status, "error", err)
	}
}

// createCredential stores the assembled login as the row a pasted one would
// be — by driving CredentialHandler.Create itself, with the caller's user,
// workspace and CURRENT role in the context. The other half of #2428 will
// teach that path to split the file into parts; this inherits it.
func (h *ProviderLoginHandler) createCredential(ctx context.Context, row deviceLoginRow, file codexauth.File) (string, error) {
	raw, err := json.Marshal(file)
	if err != nil {
		return "", err
	}
	value := string(raw)

	var role, email string
	if err := h.db.QueryRowContext(ctx, `
		SELECT wm.role, COALESCE(u.email, '')
		  FROM workspace_members wm JOIN users u ON u.id = wm.user_id
		 WHERE wm.workspace_id = ? AND wm.user_id = ?`, row.WorkspaceID, row.UserID).Scan(&role, &email); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.New("you are no longer a member of the workspace the sign-in was started in")
		}
		return "", err
	}

	plan := codexauth.PlanLabel(value)
	accountEmail := codexauth.Email(value)
	name := deviceLoginCredentialName(plan, accountEmail)
	description := "Signed in with a device code on " + h.now().Format("2006-01-02")

	create := func(name string) (int, credentialResponse, string) {
		body := map[string]any{
			"name":        name,
			"type":        CredTypeProviderLogin,
			"mode":        "subscription",
			"provider":    codexauth.ProviderID,
			"value":       value,
			"description": description,
		}
		if accountEmail != "" {
			body["account_email"] = accountEmail
		}
		bodyRaw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/credentials", strings.NewReader(string(bodyRaw)))
		req.Header.Set("Content-Type", "application/json")
		reqCtx := context.WithValue(ctx, ctxUser, &AuthUser{ID: row.UserID, Email: email})
		reqCtx = context.WithValue(reqCtx, ctxWorkspaceID, row.WorkspaceID)
		reqCtx = context.WithValue(reqCtx, ctxRole, role)
		req = req.WithContext(reqCtx)
		rr := httptest.NewRecorder()
		h.creds.Create(rr, req)
		var created credentialResponse
		if rr.Code == http.StatusCreated {
			_ = json.Unmarshal(rr.Body.Bytes(), &created)
		}
		return rr.Code, created, rr.Body.String()
	}

	code, created, body := create(name)
	if code == http.StatusConflict {
		// A second sign-in with the same account: keep both, tell them apart.
		code, created, body = create(name + " · " + h.now().Format("2006-01-02 15:04"))
	}
	if code != http.StatusCreated {
		return "", fmt.Errorf("credential create answered %d: %s", code, strings.TrimSpace(body))
	}
	return created.ID, nil
}

// deviceLoginCredentialName is "ChatGPT Plus · jana@unify.cz", or the plan
// alone when the token names no email.
func deviceLoginCredentialName(plan, email string) string {
	if email == "" {
		return plan
	}
	return plan + " · " + email
}

// deviceStartLimiter bounds sign-in starts per user in memory: the flow is
// per instance and the count is small, so a table would be ceremony.
type deviceStartLimiter struct {
	mu      sync.Mutex
	buckets map[string]*deviceStartBucket
	swept   time.Time
}

type deviceStartBucket struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

func newDeviceStartLimiter() *deviceStartLimiter {
	return &deviceStartLimiter{buckets: map[string]*deviceStartBucket{}}
}

// Reserve takes one start for key, returning how long the caller must wait
// when the budget is spent (0 = go ahead).
func (l *deviceStartLimiter) Reserve(now time.Time, key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.swept) > time.Hour {
		l.swept = now
		for k, b := range l.buckets {
			if now.Sub(b.lastSeen) > time.Hour {
				delete(l.buckets, k)
			}
		}
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &deviceStartBucket{lim: rate.NewLimiter(rate.Limit(float64(deviceLoginStartsPer10Min)/600.0), deviceLoginStartsPer10Min)}
		l.buckets[key] = b
	}
	b.lastSeen = now
	res := b.lim.ReserveN(now, 1)
	if !res.OK() {
		return 10 * time.Minute
	}
	if d := res.DelayFrom(now); d > 0 {
		res.CancelAt(now)
		return d
	}
	return 0
}
