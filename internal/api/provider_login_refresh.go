package api

// Central refresh of provider logins — docs/prd/provider-logins.md §5.3,
// §10.4 (#2428).
//
// The server owns the login and renews it; a container only ever holds an
// access token with an expiry. Refresh tokens ROTATE (each refresh returns a
// new one and kills the old), which is why this is a state machine with a
// lock and not a helper: two refreshes of one login in the same second are
// one working login and one dead one. The rules are CLIProxyAPI's, which
// exist for exactly this race:
//
//   - single-flight per login: provider_login_refresh.in_progress_until is
//     claimed with one conditional UPDATE; a second caller sees 0 rows and
//     backs off (ErrRefreshInFlight, 409 on the API).
//   - proactive: the monitor refreshes ≥ 24 h before expiry; a run start
//     refreshes at < 48 h so a 10-day token never expires mid-run.
//   - backoff: 5 min after a failure, 1 min pending. After MaxFailures
//     consecutive failures — or one the provider calls permanent — the login
//     is needs_relogin, leaves the due scan, and the owner is told through
//     the inbox. The credential row stays ACTIVE and assigned: the verdict
//     is on the login's refresh state, and a re-import clears it.
//   - after a success the file is re-rendered into every running container
//     of a Codex agent the login pays for. Codex reads auth.json at process
//     start, so a running exec finishes on the old token — fine for a
//     10-day token — and the next one starts on the new.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/codexauth"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/providerlogin"
)

// ErrRefreshInFlight: another refresh of this login holds the claim.
var ErrRefreshInFlight = errors.New("a refresh of this login is already in flight")

// ErrRefreshUnsupported: the login has no refresh flow (a setup-token, an
// API key) or no sealed refresh token to run it with.
var ErrRefreshUnsupported = errors.New("this login has no refresh flow")

var errRefreshSuperseded = errors.New("login changed while refresh was in flight; retry with the current login")

// errNeedsRelogin: the login is past repair by refresh.
var errNeedsRelogin = errors.New("login needs a re-login: the stored refresh token no longer works")

// ProviderLoginRefresher runs the refresh state machine for one process.
type ProviderLoginRefresher struct {
	db        *sql.DB
	logger    *slog.Logger
	container provider.ContainerProvider
	now       func() time.Time

	mu       sync.Mutex
	refresh  map[string]providerlogin.TokenRefresher // by provider
	interval time.Duration
}

// NewProviderLoginRefresher wires the production token endpoints. ctr may be
// nil (tests, --no-docker): the re-render then no-ops like the revoke
// reconcile does.
func NewProviderLoginRefresher(db *sql.DB, logger *slog.Logger, ctr provider.ContainerProvider) *ProviderLoginRefresher {
	if logger == nil {
		logger = slog.Default()
	}
	return &ProviderLoginRefresher{
		db: db, logger: logger, container: ctr, now: func() time.Time { return time.Now().UTC() },
		refresh: map[string]providerlogin.TokenRefresher{
			"OPENAI": providerlogin.NewOpenAIRefresher(nil),
			"GOOGLE": providerlogin.NewGoogleRefresher(nil),
		},
		interval: time.Minute,
	}
}

// SetTokenRefresher replaces the endpoint for one provider — tests point it
// at a fake; a future Google refresher registers here.
func (r *ProviderLoginRefresher) SetTokenRefresher(provider string, tr providerlogin.TokenRefresher) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refresh[providerlogin.Canonical(provider)] = tr
}

func (r *ProviderLoginRefresher) refresherFor(provider string) (providerlogin.TokenRefresher, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tr, ok := r.refresh[providerlogin.Canonical(provider)]
	return tr, ok
}

// Run is the standalone loop, for a deployment without a CredentialMonitor
// (LLM proxy off). With the monitor present its tick calls RefreshDue
// instead, so the two never run side by side.
func (r *ProviderLoginRefresher) Run(ctx context.Context) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	r.RefreshDue(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.RefreshDue(ctx)
		}
	}
}

// dueLogin is one row of the due scan.
type dueLogin struct {
	ID        string
	Provider  string
	ExpiresAt string
	Status    sql.NullString
	NextAt    sql.NullString
	InFlight  sql.NullString
}

// RefreshDue refreshes every login whose access token is inside RefreshLead
// of expiry (or whose expiry is unknown), honouring backoff, single-flight
// and needs_relogin. One pass; errors are recorded per login, never fatal.
func (r *ProviderLoginRefresher) RefreshDue(ctx context.Context) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT c.id, c.provider,
		       COALESCE((SELECT value FROM credential_fields f WHERE f.credential_id = c.id AND f.key = ?), ''),
		       s.status, s.next_at, s.in_progress_until
		FROM credentials c
		JOIN credential_fields m ON m.credential_id = c.id AND m.key = ? AND m.value = ?
		JOIN credential_fields rt ON rt.credential_id = c.id AND rt.key = ?
		LEFT JOIN provider_login_refresh s ON s.credential_id = c.id
		WHERE c.type = ? AND c.deleted_at IS NULL AND c.status = 'ACTIVE'`,
		providerlogin.PartExpiresAt, providerlogin.PartMode, providerlogin.ModeSubscription,
		providerlogin.PartRefreshToken, CredTypeProviderLogin)
	if err != nil {
		r.logger.Warn("provider login refresh: scan", "error", err)
		return
	}
	var due []dueLogin
	for rows.Next() {
		var d dueLogin
		if err := rows.Scan(&d.ID, &d.Provider, &d.ExpiresAt, &d.Status, &d.NextAt, &d.InFlight); err != nil {
			rows.Close()
			r.logger.Warn("provider login refresh: scan row", "error", err)
			return
		}
		due = append(due, d)
	}
	rows.Close()

	now := r.now()
	for _, d := range due {
		if d.Status.Valid && d.Status.String == providerlogin.StatusNeedsRelogin {
			continue
		}
		if _, ok := r.refresherFor(d.Provider); !ok {
			continue
		}
		var exp time.Time
		if d.ExpiresAt != "" {
			exp, _ = time.Parse(time.RFC3339, d.ExpiresAt)
		}
		if !providerlogin.Due(exp, now, providerlogin.RefreshLeadFor(d.Provider)) {
			continue
		}
		if d.Status.Valid && d.Status.String == providerlogin.StatusFailed {
			if next, ok := parseNullTime(d.NextAt); ok && next.After(now) {
				continue // backoff after a failure
			}
		}
		if _, err := r.Refresh(ctx, d.ID, false); err != nil && !errors.Is(err, ErrRefreshInFlight) {
			r.logger.Warn("provider login refresh failed", "credential_id", d.ID, "provider", d.Provider, "error", err)
		}
	}
}

func parseNullTime(s sql.NullString) (time.Time, bool) {
	if !s.Valid || s.String == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s.String)
	return t, err == nil
}

// EnsureFreshForRun is the run-start hook (§10.4): refresh when less than
// RunStartLead remains. Returns the NEW ciphertext of the access token when a
// refresh happened, so the caller can deliver it without a second read. A
// transient failure allows a still-valid token; expired tokens, unknown
// expiry on failed refresh, and needs_relogin fail closed.
func (r *ProviderLoginRefresher) EnsureFreshForRun(ctx context.Context, credID string) (string, error) {
	var provider, expiresAt string
	var status sql.NullString
	err := r.db.QueryRowContext(ctx, `
		SELECT c.provider,
		       COALESCE((SELECT value FROM credential_fields f WHERE f.credential_id = c.id AND f.key = ?), ''),
		       s.status
		FROM credentials c
		JOIN credential_fields m ON m.credential_id = c.id AND m.key = ? AND m.value = ?
		JOIN credential_fields rt ON rt.credential_id = c.id AND rt.key = ?
		LEFT JOIN provider_login_refresh s ON s.credential_id = c.id
		WHERE c.id = ? AND c.type = ? AND c.deleted_at IS NULL`,
		providerlogin.PartExpiresAt, providerlogin.PartMode, providerlogin.ModeSubscription,
		providerlogin.PartRefreshToken, credID, CredTypeProviderLogin).Scan(&provider, &expiresAt, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil // not a refreshable login, or gone
	}
	if err != nil {
		return "", fmt.Errorf("check provider login freshness: %w", err)
	}
	if status.Valid && status.String == providerlogin.StatusNeedsRelogin {
		return "", errNeedsRelogin
	}
	var exp time.Time
	if expiresAt != "" {
		exp, _ = time.Parse(time.RFC3339, expiresAt)
	}
	if !providerlogin.Due(exp, r.now(), providerlogin.RunStartLeadFor(provider)) {
		return "", nil
	}
	enc, err := r.Refresh(ctx, credID, false)
	if err != nil {
		if exp.IsZero() || !exp.After(r.now()) {
			return "", fmt.Errorf("provider login cannot be renewed before run: %w", err)
		}
		if !errors.Is(err, ErrRefreshInFlight) {
			r.logger.Warn("provider login refresh before run start failed; starting on the stored token",
				"credential_id", credID, "error", err)
		}
		return "", nil
	}
	return enc, nil
}

// Refresh renews one login now. force skips the failure backoff (an operator
// clicked Refresh); it never skips the single-flight claim. Returns the new
// access token's ciphertext.
func (r *ProviderLoginRefresher) Refresh(ctx context.Context, credID string, force bool) (string, error) {
	// Once a provider rotates a token, cancellation of the initiating HTTP
	// request must not discard it. Bound the whole operation below the lease.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancel()
	now := r.now()

	var provider, createdBy, wsID, name string
	var refreshEnc sql.NullString
	err := r.db.QueryRowContext(ctx, `
		SELECT c.provider, COALESCE(c.created_by,''), c.workspace_id, c.name,
		       (SELECT encrypted_value FROM credential_fields f WHERE f.credential_id = c.id AND f.key = ?)
		FROM credentials c
		WHERE c.id = ? AND c.type = ? AND c.deleted_at IS NULL
		  AND EXISTS (SELECT 1 FROM credential_fields m WHERE m.credential_id = c.id AND m.key = ? AND m.value = ?)`,
		providerlogin.PartRefreshToken, credID, CredTypeProviderLogin, providerlogin.PartMode, providerlogin.ModeSubscription).
		Scan(&provider, &createdBy, &wsID, &name, &refreshEnc)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (!refreshEnc.Valid || refreshEnc.String == "")) {
		return "", ErrRefreshUnsupported
	}
	if err != nil {
		return "", fmt.Errorf("load login: %w", err)
	}
	tr, ok := r.refresherFor(provider)
	if !ok {
		return "", ErrRefreshUnsupported
	}

	// The claim. One statement decides who refreshes: the row is created if
	// missing, and the UPDATE only lands when nobody holds it and — unless
	// forced — the backoff has passed.
	if _, err := r.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO provider_login_refresh (credential_id, status) VALUES (?, ?)`, credID, providerlogin.StatusOK); err != nil {
		return "", fmt.Errorf("refresh state: %w", err)
	}
	nowS := now.Format(time.RFC3339)
	claimUntil := now.Add(providerlogin.PendingBackoff).Format(time.RFC3339)
	// next_at is a schedule on a healthy login and a BACKOFF after a
	// failure; only the second holds an unforced refresh back.
	backoffClause := ""
	if !force {
		backoffClause = " AND (status != 'failed' OR next_at IS NULL OR next_at <= ?)"
	}
	args := []any{claimUntil, nowS, credID, nowS, force}
	if !force {
		args = append(args, nowS)
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE provider_login_refresh SET in_progress_until = ?, updated_at = ?
		 WHERE credential_id = ? AND (in_progress_until IS NULL OR in_progress_until < ?)
		   AND (status != 'needs_relogin' OR ?)`+backoffClause, args...)
	if err != nil {
		return "", fmt.Errorf("claim refresh: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var status string
		var inFlight, nextAt sql.NullString
		_ = r.db.QueryRowContext(ctx, `SELECT status, in_progress_until, next_at FROM provider_login_refresh WHERE credential_id = ?`, credID).
			Scan(&status, &inFlight, &nextAt)
		if status == providerlogin.StatusNeedsRelogin && !force {
			return "", errNeedsRelogin
		}
		if until, ok := parseNullTime(inFlight); ok && until.After(now) {
			return "", ErrRefreshInFlight
		}
		return "", fmt.Errorf("refresh is backing off until %s", nextAt.String)
	}
	release := func() {
		_, _ = r.db.ExecContext(context.WithoutCancel(ctx),
			`UPDATE provider_login_refresh SET in_progress_until = NULL WHERE credential_id = ? AND in_progress_until = ?`, credID, claimUntil)
	}

	// Read rotating material AFTER winning the claim: a previous holder may
	// have replaced it since the eligibility query above.
	if err := r.db.QueryRowContext(ctx, `SELECT encrypted_value FROM credential_fields WHERE credential_id = ? AND key = ?`,
		credID, providerlogin.PartRefreshToken).Scan(&refreshEnc); err != nil {
		release()
		return "", fmt.Errorf("load claimed refresh token: %w", err)
	}
	refreshToken, err := encryption.Decrypt(refreshEnc.String)
	if err != nil {
		release()
		return "", fmt.Errorf("decrypt refresh token: %w", err)
	}
	result, rerr := tr.Refresh(ctx, refreshToken)
	if rerr != nil {
		r.recordFailure(ctx, credID, wsID, name, createdBy, refreshEnc.String, rerr, now)
		release()
		return "", rerr
	}

	// The expiry the token itself states wins over anything the endpoint
	// said; an endpoint that said nothing still yields a schedule.
	if result.ExpiresAt.IsZero() {
		if exp, ok := codexauth.AccessTokenExpiry(result.AccessToken); ok {
			result.ExpiresAt = exp
		}
	}
	enc, err := r.storeRotated(ctx, credID, refreshEnc.String, result, now)
	if err != nil {
		release()
		return "", err
	}
	release()
	r.logger.Info("provider login refreshed", "credential_id", credID, "provider", provider,
		"expires_at", result.ExpiresAt.Format(time.RFC3339))
	r.reRender(ctx, credID, wsID)
	return enc, nil
}

// storeRotated writes the new access / refresh / id tokens and the expiry in
// one transaction, and clears the failure state. The old refresh token is
// dead the moment the endpoint answered; a half-written row is a login that
// can never refresh again, so all-or-nothing is the only acceptable shape.
func (r *ProviderLoginRefresher) storeRotated(ctx context.Context, credID, expectedRefreshEnc string, res providerlogin.RefreshResult, now time.Time) (string, error) {
	accessEnc, err := encryption.Encrypt(res.AccessToken)
	if err != nil {
		return "", fmt.Errorf("encrypt access token: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	nowS := now.Format(time.RFC3339)
	expS := ""
	if !res.ExpiresAt.IsZero() {
		expS = res.ExpiresAt.UTC().Format(time.RFC3339)
	}
	// The operator may have re-imported or revoked this login during the
	// remote call. Ciphertext identity is an exact generation check, unlike
	// a second-resolution updated_at timestamp.
	updated, err := tx.ExecContext(ctx,
		`UPDATE credentials SET encrypted_value = ?, token_expires_at = NULLIF(?, ''), updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL
		 AND EXISTS (SELECT 1 FROM credential_fields f WHERE f.credential_id = credentials.id AND f.key = ? AND f.encrypted_value = ?)`,
		accessEnc, expS, nowS, credID, providerlogin.PartRefreshToken, expectedRefreshEnc)
	if err != nil {
		return "", fmt.Errorf("store access token: %w", err)
	}
	if n, err := updated.RowsAffected(); err != nil || n != 1 {
		return "", errRefreshSuperseded
	}
	upsertSecret := func(key, value string) error {
		if value == "" {
			return nil
		}
		enc, err := encryption.Encrypt(value)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO credential_fields (credential_id, key, value, encrypted_value, is_secret, ordinal)
			VALUES (?, ?, NULL, ?, 1, COALESCE((SELECT MAX(ordinal)+1 FROM credential_fields WHERE credential_id = ?), 0))
			ON CONFLICT(credential_id, key) DO UPDATE SET value = NULL, encrypted_value = excluded.encrypted_value, is_secret = 1,
				updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`, credID, key, enc, credID)
		return err
	}
	if err := upsertSecret(providerlogin.PartRefreshToken, res.RefreshToken); err != nil {
		return "", fmt.Errorf("store refresh token: %w", err)
	}
	if err := upsertSecret(providerlogin.PartIDToken, res.IDToken); err != nil {
		return "", fmt.Errorf("store id token: %w", err)
	}
	if expS != "" {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO credential_fields (credential_id, key, value, encrypted_value, is_secret, ordinal)
			VALUES (?, ?, ?, NULL, 0, COALESCE((SELECT MAX(ordinal)+1 FROM credential_fields WHERE credential_id = ?), 0))
			ON CONFLICT(credential_id, key) DO UPDATE SET value = excluded.value, encrypted_value = NULL, is_secret = 0,
				updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`, credID, providerlogin.PartExpiresAt, expS, credID); err != nil {
			return "", fmt.Errorf("store expiry: %w", err)
		}
	}
	nextAt := ""
	if !res.ExpiresAt.IsZero() {
		var p string
		if err := tx.QueryRowContext(ctx, `SELECT provider FROM credentials WHERE id = ?`, credID).Scan(&p); err != nil {
			return "", err
		}
		nextAt = res.ExpiresAt.Add(-providerlogin.RefreshLeadFor(p)).UTC().Format(time.RFC3339)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE provider_login_refresh SET status = ?, last_at = ?, next_at = NULLIF(?, ''), error = NULL, failures = 0, updated_at = ?
		WHERE credential_id = ?`, providerlogin.StatusOK, nowS, nextAt, nowS, credID); err != nil {
		return "", fmt.Errorf("store refresh state: %w", err)
	}
	if err := RecordCredentialEventTx(ctx, tx, credID, AuditEventRefresh, "", "",
		map[string]any{"outcome": "ok", "expires_at": expS}); err != nil {
		return "", fmt.Errorf("audit refresh: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return accessEnc, nil
}

// recordFailure bumps the failure count, sets the backoff, and after
// MaxFailures — or a permanent error — marks the login needs_relogin and
// tells the owner.
func (r *ProviderLoginRefresher) recordFailure(ctx context.Context, credID, wsID, name, ownerID, expectedRefreshEnc string, rerr error, now time.Time) {
	msg := rerr.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	nowS := now.Format(time.RFC3339)
	nextAt := now.Add(providerlogin.FailureBackoff).Format(time.RFC3339)
	var failures int
	_ = r.db.QueryRowContext(ctx, `SELECT failures FROM provider_login_refresh WHERE credential_id = ?`, credID).Scan(&failures)
	failures++
	status := providerlogin.StatusFailed
	if failures >= providerlogin.MaxFailures || providerlogin.IsPermanent(rerr) {
		status = providerlogin.StatusNeedsRelogin
	}
	updated, err := r.db.ExecContext(ctx, `
		UPDATE provider_login_refresh SET status = ?, next_at = ?, error = ?, failures = ?, updated_at = ?
		WHERE credential_id = ? AND EXISTS (
		 SELECT 1 FROM credential_fields f JOIN credentials c ON c.id = f.credential_id
		 WHERE f.credential_id = ? AND f.key = ? AND f.encrypted_value = ? AND c.deleted_at IS NULL
		)`, status, nextAt, msg, failures, nowS, credID, credID, providerlogin.PartRefreshToken, expectedRefreshEnc)
	if err != nil {
		r.logger.Warn("provider login refresh: record failure", "credential_id", credID, "error", err)
		return
	}
	if n, err := updated.RowsAffected(); err != nil || n != 1 {
		return // a stale failure must not poison a newly imported login
	}
	recordCredentialEventBestEffort(ctx, r.db, r.logger, credID, AuditEventRefresh, "", "",
		map[string]any{"outcome": status, "failures": failures, "error": msg})
	if status != providerlogin.StatusNeedsRelogin {
		return
	}
	r.logger.Warn("provider login needs a re-login", "credential_id", credID, "failures", failures, "error", msg)
	item := inbox.Item{
		WorkspaceID:    wsID,
		Kind:           inbox.KindMessage,
		SourceID:       "provider-login-relogin:" + credID,
		TargetUserID:   ownerID,
		Title:          "Provider login \"" + name + "\" needs a re-login",
		BodyMD:         "Crewship could not renew the access token of **" + name + "** (" + msg + "). Agents paying with this login will stop authenticating when the current token expires. Re-import the login under Credentials → Providers.",
		SenderType:     "system",
		SenderName:     "Crewship",
		Priority:       "high",
		AttentionClass: inbox.AttentionRepair,
		Payload:        map[string]interface{}{"credential_id": credID, "action": "relogin"},
	}
	if ownerID == "" {
		item.TargetRole = "MANAGER"
	}
	if err := inbox.Upsert(ctx, r.db, r.logger, item); err != nil {
		r.logger.Warn("provider login: notify owner", "credential_id", credID, "error", err)
	}
}

// reRender writes the refreshed file into the running container of every
// Codex agent the login pays for. Best-effort, like the revoke reconcile: a
// stopped container gets the file at its next run start anyway.
func (r *ProviderLoginRefresher) reRender(ctx context.Context, credID, wsID string) {
	if r.container == nil {
		return
	}
	// Every agent the login reaches, by the same sources the delivery query
	// uses, narrowed to the one adapter that reads a file.
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT a.id, a.slug, cr.id, cr.slug, a.cli_adapter FROM agents a
		JOIN crews cr ON cr.id = a.crew_id AND cr.deleted_at IS NULL
		WHERE a.workspace_id = ? AND a.deleted_at IS NULL AND a.cli_adapter IN ('CODEX_CLI', 'GEMINI_CLI')
		  AND (
		    EXISTS (SELECT 1 FROM agent_credentials ac WHERE ac.agent_id = a.id AND ac.credential_id = ?)
		    OR EXISTS (SELECT 1 FROM credential_bindings b WHERE b.credential_id = ? AND
		               ((b.scope = 'AGENT' AND b.agent_id = a.id) OR (b.scope = 'CREW' AND b.crew_id = a.crew_id) OR b.scope = 'WORKSPACE'))
		    OR EXISTS (SELECT 1 FROM credential_crews cc WHERE cc.credential_id = ? AND cc.crew_id = a.crew_id)
		  )`, wsID, credID, credID, credID)
	if err != nil {
		r.logger.Warn("provider login re-render: query agents", "credential_id", credID, "error", err)
		return
	}
	type target struct{ agentID, agentSlug, crewID, crewSlug, adapter string }
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.agentID, &t.agentSlug, &t.crewID, &t.crewSlug, &t.adapter); err != nil {
			rows.Close()
			return
		}
		targets = append(targets, t)
	}
	rows.Close()
	if len(targets) == 0 {
		return
	}
	for _, t := range targets {
		// The agent's own delivery, so the file matches what its next run
		// start would write (slot, parts, lease) rather than a re-derivation.
		delivered, _, err := loadDeliveredCredentials(ctx, r.db, t.agentID)
		if err != nil {
			r.logger.Warn("provider login re-render: delivery", "agent_id", t.agentID, "error", err)
			continue
		}
		var login *orchestrator.Credential
		for _, d := range delivered {
			if d.ID != credID || d.HandleOnly {
				continue
			}
			dec, err := encryption.Decrypt(d.EncryptedValue)
			if err != nil {
				break
			}
			c := orchestrator.Credential{ID: d.ID, EnvVarName: d.EnvVar, PlainValue: dec, Type: d.Type, Provider: d.Provider}
			fields, err := decryptDeliveredFields(d, encryption.Decrypt)
			if err != nil {
				break
			}
			for _, f := range fields {
				c.Fields = append(c.Fields, orchestrator.CredentialField{Key: f.Key, EnvVar: f.EnvVar, Value: f.Value, IsSecret: f.IsSecret})
			}
			login = &c
			break
		}
		if login == nil {
			continue
		}
		containerID := r.container.CrewContainerName(t.crewID, t.crewSlug)
		if err := orchestrator.DeliverProviderLogin(ctx, r.container, containerID, t.agentSlug, t.adapter, *login, r.logger); err != nil {
			// Overwhelmingly "container not running": the next run start
			// writes the file. Debug, like the revoke reconcile.
			r.logger.Debug("provider login re-render skipped", "agent_slug", t.agentSlug, "crew_id", t.crewID, "error", err)
		}
	}
}

// ---------------------------------------------------------------------------
// Run-start hook (§10.4): loadDeliveredCredentials asks it about every
// PROVIDER_LOGIN row it is about to deliver.
// ---------------------------------------------------------------------------

// runStartRefresher is the process-wide hook, set once at boot before the
// server accepts traffic (the same shape as inbox's external notifier). nil
// means "deliver what is stored", which is what every test and `crewship
// seed` gets.
type runStartRefresher interface {
	EnsureFreshForRun(ctx context.Context, credID string) (string, error)
}

var runStartLoginRefresher runStartRefresher

// SetRunStartLoginRefresher wires the production refresher.
func SetRunStartLoginRefresher(r *ProviderLoginRefresher) {
	if r == nil {
		runStartLoginRefresher = nil
		return
	}
	runStartLoginRefresher = r
}

// SetRunStartLoginRefresherForTesting swaps the hook and returns a restore.
func SetRunStartLoginRefresherForTesting(r runStartRefresher) func() {
	prev := runStartLoginRefresher
	runStartLoginRefresher = r
	return func() { runStartLoginRefresher = prev }
}

// loadDeliveredCredentialsForRun is the mutating boot/delegation loader.
// Metadata views and refresh-file reconciliation use the read-only loader;
// opening a Providers tab must never rotate credentials.
func loadDeliveredCredentialsForRun(ctx context.Context, db *sql.DB, agentID string) ([]deliveredCredential, []deliveredSlotNotice, error) {
	delivered, notices, err := loadDeliveredCredentials(ctx, db, agentID)
	if err != nil {
		return nil, nil, err
	}
	hook := runStartLoginRefresher
	if hook == nil {
		return delivered, notices, nil
	}
	seen := map[string]bool{}
	for i := range delivered {
		if delivered[i].Type != CredTypeProviderLogin || delivered[i].HandleOnly {
			continue
		}
		if !seen[delivered[i].ID] {
			if _, err := hook.EnsureFreshForRun(ctx, delivered[i].ID); err != nil {
				return nil, nil, fmt.Errorf("credential %s: %w", delivered[i].ID, err)
			}
			seen[delivered[i].ID] = true
		}
	}
	// Refresh rotates multiple parts atomically. Re-read the complete set,
	// including access ciphertext, instead of replacing just one field.
	return loadDeliveredCredentials(ctx, db, agentID)
}

// ---------------------------------------------------------------------------
// POST /api/v1/credentials/{credentialId}/refresh (§10.3)
// ---------------------------------------------------------------------------

// SetLoginRefresher wires the refresher the Refresh route uses.
func (h *CredentialHandler) SetLoginRefresher(r *ProviderLoginRefresher) { h.loginRefresher = r }

// Refresh forces a refresh now. 409 while another is in flight; 400 for a
// login with no refresh flow. Role floor is update, like test-stored: a
// refresh rotates the stored secret.
func (h *CredentialHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("credentialId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	user := UserFromContext(r.Context())
	if !canRole(role, "update") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	visFilter, visArgs := credentialVisibilityFilter(role, user)
	args := append([]any{credID, workspaceID}, visArgs...)
	var one int
	err := h.db.QueryRowContext(r.Context(),
		`SELECT 1 FROM credentials c WHERE c.id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL `+visFilter, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, msgCredentialNotFound)
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "refresh: lookup", err)
		return
	}
	if h.loginRefresher == nil {
		replyError(w, http.StatusServiceUnavailable, "provider login refresh is not available on this server")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	_, err = h.loginRefresher.Refresh(ctx, credID, true)
	switch {
	case err == nil:
	case errors.Is(err, ErrRefreshInFlight), errors.Is(err, errRefreshSuperseded):
		replyError(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, ErrRefreshUnsupported):
		replyError(w, http.StatusBadRequest, "this login has no refresh flow: only a supported subscription login with a stored refresh token can be refreshed")
		return
	default:
		// The failure is recorded on the login; the response says so and
		// carries the login so the client shows the new state.
		h.logger.Warn("provider login refresh requested and failed", "credential_id", credID, "error", err)
	}
	login, _, lerr := loadLoginView(r.Context(), h.db, h.logger, credID)
	if lerr != nil {
		replyInternalError(w, h.logger, "refresh: reload login", lerr)
		return
	}
	auditFromRequest(r, h.db, "credential.refresh", "CREDENTIAL", credID, map[string]interface{}{
		"outcome": map[bool]string{true: "ok", false: "failed"}[err == nil],
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "refresh failed: " + strings.TrimSpace(err.Error()), "login": login})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"login": login})
}
