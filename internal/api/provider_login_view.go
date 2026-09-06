package api

// The `login` object on credential rows — docs/prd/provider-logins.md §10.1.
//
// A PROVIDER_LOGIN row carries it from its parts; an AI_CLI_TOKEN or API_KEY
// row with a model provider carries it DERIVED (mode from the type, plan and
// expiry from the token where the token says, refresh.supported=false), so the
// Providers tab shows the install base's logins without a migration. Nothing
// here returns a value: the plan and the expiry are read from the decrypted
// token inside this process and only those two facts leave it.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/codexauth"
	"github.com/crewship-ai/crewship/internal/providerlogin"
)

// loginView is the wire shape of `login` (§10.1), field for field.
type loginView struct {
	Mode        string                 `json:"mode"`
	Provider    string                 `json:"provider"`
	Plan        *string                `json:"plan"`
	PlanLabel   *string                `json:"plan_label"`
	OwnerUserID *string                `json:"owner_user_id"`
	OwnerEmail  *string                `json:"owner_email"`
	ExpiresAt   *string                `json:"expires_at"`
	Refresh     loginRefreshView       `json:"refresh"`
	Quota       *loginQuotaView        `json:"quota"`
	Delivery    providerlogin.Delivery `json:"delivery"`
	PaysFor     loginPaysFor           `json:"pays_for"`
}

type loginRefreshView struct {
	Supported bool    `json:"supported"`
	Status    string  `json:"status"`
	LastAt    *string `json:"last_at"`
	NextAt    *string `json:"next_at"`
	Error     *string `json:"error"`
}

// loginQuotaView is P-D's seat quota (§10.1). Always null until the sidecar
// reports the 5 h / weekly windows; the shape is fixed now so the client can
// bind to it.
type loginQuotaView struct {
	Window5hPct     int    `json:"window_5h_pct"`
	WindowWeeklyPct int    `json:"window_weekly_pct"`
	ResetsAt        string `json:"resets_at"`
}

type loginPaysFor struct {
	Agents int `json:"agents"`
	Crews  int `json:"crews"`
}

// isLoginRow reports whether a credential row carries a login object: the
// type itself, or a legacy login of a model provider.
func isLoginRow(credType, provider string) bool {
	if credType == CredTypeProviderLogin {
		return true
	}
	return (credType == CredTypeAICLIToken || credType == CredTypeAPIKey) && providerlogin.IsProvider(provider)
}

// loginRowsSQL is the WHERE fragment for ?kind=provider_login — the SQL
// twin of isLoginRow, on the aliased credentials table `c`.
func loginRowsSQL() (string, []any) {
	ps := providerlogin.Providers()
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ps)), ",")
	args := make([]any, 0, len(ps)+1)
	args = append(args, CredTypeProviderLogin)
	for _, p := range ps {
		args = append(args, p)
	}
	return " AND (c.type = ? OR (c.type IN ('AI_CLI_TOKEN','API_KEY') AND UPPER(c.provider) IN (" + marks + ")))", args
}

// loginSource is what the batch loader needs per row to build a loginView.
type loginSource struct {
	ID             string
	Type           string
	Provider       string
	CreatedBy      string
	TokenExpiresAt string
	EncryptedValue string
}

// refreshStateRow mirrors provider_login_refresh.
type refreshStateRow struct {
	Status          string
	LastAt          sql.NullString
	NextAt          sql.NullString
	Error           sql.NullString
	Failures        int
	InProgressUntil sql.NullString
}

// attachLoginViews fills Login on every row that carries one, in place, with
// four batched queries for the whole page: parts, refresh state, owners,
// pays-for counts. The decrypt happens only for the legacy rows whose plan
// or expiry can only be read from the token.
func attachLoginViews(ctx context.Context, db *sql.DB, logger *slog.Logger, rows []credentialResponse, sources map[string]loginSource) {
	var ids []string
	for i := range rows {
		if isLoginRow(rows[i].Type, rows[i].Provider) {
			ids = append(ids, rows[i].ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	parts, err := loadLoginParts(ctx, db, ids)
	if err != nil {
		logger.Warn("provider login parts", "error", err)
	}
	states, err := loadRefreshStates(ctx, db, ids)
	if err != nil {
		logger.Warn("provider login refresh state", "error", err)
	}
	paysFor, err := loadLoginPaysFor(ctx, db, ids)
	if err != nil {
		logger.Warn("provider login pays_for", "error", err)
	}
	owners := map[string]string{}
	var ownerIDs []string
	for _, id := range ids {
		if s, ok := sources[id]; ok && s.CreatedBy != "" {
			ownerIDs = append(ownerIDs, s.CreatedBy)
		}
	}
	if len(ownerIDs) > 0 {
		if m, err := loadUserEmails(ctx, db, ownerIDs); err != nil {
			logger.Warn("provider login owners", "error", err)
		} else {
			owners = m
		}
	}
	for i := range rows {
		if !isLoginRow(rows[i].Type, rows[i].Provider) {
			continue
		}
		src := sources[rows[i].ID]
		src.ID, src.Type, src.Provider = rows[i].ID, rows[i].Type, rows[i].Provider
		v := buildLoginView(src, parts[rows[i].ID], states[rows[i].ID], owners[src.CreatedBy], paysFor[rows[i].ID], logger)
		rows[i].Login = &v
	}
}

// buildLoginView derives one login object. parts is the row's mode/plan/
// expires_at/has-refresh-token facts; state may be nil.
func buildLoginView(src loginSource, parts map[string]string, state *refreshStateRow, ownerEmail string, pays loginPaysFor, logger *slog.Logger) loginView {
	provider := providerlogin.Canonical(src.Provider)
	v := loginView{Provider: provider, PaysFor: pays}
	if src.CreatedBy != "" {
		owner := src.CreatedBy
		v.OwnerUserID = &owner
		if ownerEmail != "" {
			email := ownerEmail
			v.OwnerEmail = &email
		}
	}

	var plan, expires string
	hasRefreshToken := false
	switch src.Type {
	case CredTypeProviderLogin:
		v.Mode = parts[providerlogin.PartMode]
		if v.Mode == "" {
			v.Mode = providerlogin.ModeSubscription
		}
		plan = parts[providerlogin.PartPlan]
		expires = parts[providerlogin.PartExpiresAt]
		_, hasRefreshToken = parts[providerlogin.PartRefreshToken]
	case CredTypeAICLIToken:
		v.Mode = providerlogin.ModeSubscription
		// A legacy Codex blob says its plan and expiry in the token; a Claude
		// setup-token says neither. Decrypt only for the one that can answer.
		if provider == codexauth.ProviderID && src.EncryptedValue != "" {
			if dec, err := decryptCredential(src.EncryptedValue); err == nil && !isPendingSentinel(dec) {
				plan = codexauth.PlanType(dec)
				if exp, ok := codexauth.AccessTokenExpiry(dec); ok {
					expires = exp.UTC().Format(time.RFC3339)
				}
			} else if err != nil && logger != nil {
				logger.Warn("provider login: decrypt legacy login for plan", "credential_id", src.ID, "error", err)
			}
		}
	default: // API_KEY
		v.Mode = providerlogin.ModeAPIKey
	}
	if expires == "" && src.TokenExpiresAt != "" {
		expires = src.TokenExpiresAt
	}
	if plan != "" {
		p := plan
		v.Plan = &p
	}
	if v.Mode == providerlogin.ModeSubscription {
		label := providerlogin.PlanLabel(provider, plan)
		if provider == "ANTHROPIC" && plan == "" {
			// The label the Paymaster has always shown for a setup-token.
			label = "Anthropic Max"
		}
		v.PlanLabel = &label
	}
	if expires != "" {
		e := expires
		v.ExpiresAt = &e
	}
	v.Delivery = providerlogin.DeliveryFor(provider, v.Mode)

	v.Refresh = loginRefreshView{Status: providerlogin.StatusNone}
	if src.Type == CredTypeProviderLogin && v.Mode == providerlogin.ModeSubscription && hasRefreshToken {
		v.Refresh.Supported = true
		v.Refresh.Status = providerlogin.StatusOK
		if state != nil {
			v.Refresh.Status = state.Status
			v.Refresh.LastAt = loginNullStr(state.LastAt)
			v.Refresh.NextAt = loginNullStr(state.NextAt)
			v.Refresh.Error = loginNullStr(state.Error)
			if state.InProgressUntil.Valid && state.Status == providerlogin.StatusOK {
				if until, err := time.Parse(time.RFC3339, state.InProgressUntil.String); err == nil && until.After(time.Now()) {
					v.Refresh.Status = providerlogin.StatusPending
				}
			}
		}
	}
	return v
}

func loginNullStr(s sql.NullString) *string {
	if !s.Valid || s.String == "" {
		return nil
	}
	v := s.String
	return &v
}

func inMarks(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func idsAsArgs(ids []string) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// loadLoginParts returns the NON-SECRET parts' values plus the presence of
// the secret ones (value "" — the ciphertext is not selected).
func loadLoginParts(ctx context.Context, db *sql.DB, ids []string) (map[string]map[string]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT credential_id, key, COALESCE(value, '') FROM credential_fields WHERE credential_id IN (`+inMarks(len(ids))+`)`,
		idsAsArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]string{}
	for rows.Next() {
		var id, key, val string
		if err := rows.Scan(&id, &key, &val); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[string]string{}
		}
		out[id][key] = val
	}
	return out, rows.Err()
}

func loadRefreshStates(ctx context.Context, db *sql.DB, ids []string) (map[string]*refreshStateRow, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT credential_id, status, last_at, next_at, error, failures, in_progress_until
		 FROM provider_login_refresh WHERE credential_id IN (`+inMarks(len(ids))+`)`,
		idsAsArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*refreshStateRow{}
	for rows.Next() {
		var id string
		var s refreshStateRow
		if err := rows.Scan(&id, &s.Status, &s.LastAt, &s.NextAt, &s.Error, &s.Failures, &s.InProgressUntil); err != nil {
			return nil, err
		}
		out[id] = &s
	}
	return out, rows.Err()
}

func loadUserEmails(ctx context.Context, db *sql.DB, ids []string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, COALESCE(email, '') FROM users WHERE id IN (`+inMarks(len(ids))+`)`, idsAsArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, email string
		if err := rows.Scan(&id, &email); err != nil {
			return nil, err
		}
		out[id] = email
	}
	return out, rows.Err()
}

// loadLoginPaysFor counts what each login pays for: the distinct agents it
// reaches (explicit grants, AGENT bindings, members of CREW-bound crews, and
// every agent for a WORKSPACE binding) and the distinct crews (CREW bindings
// and the legacy crew link). Same sources as agentDeliveredCredentialsSQL,
// counted from the credential's side.
func loadLoginPaysFor(ctx context.Context, db *sql.DB, ids []string) (map[string]loginPaysFor, error) {
	marks := inMarks(len(ids))
	args := append(idsAsArgs(ids), idsAsArgs(ids)...)
	args = append(args, idsAsArgs(ids)...)
	args = append(args, idsAsArgs(ids)...)
	out := map[string]loginPaysFor{}
	agentRows, err := db.QueryContext(ctx, `
		SELECT credential_id, COUNT(DISTINCT agent_id) FROM (
			SELECT ac.credential_id AS credential_id, ac.agent_id AS agent_id
			FROM agent_credentials ac JOIN agents a ON a.id = ac.agent_id AND a.deleted_at IS NULL
			WHERE ac.credential_id IN (`+marks+`)
			UNION ALL
			SELECT b.credential_id, b.agent_id FROM credential_bindings b
			JOIN agents a ON a.id = b.agent_id AND a.deleted_at IS NULL
			WHERE b.scope = 'AGENT' AND b.credential_id IN (`+marks+`)
			UNION ALL
			SELECT b.credential_id, a.id FROM credential_bindings b
			JOIN agents a ON a.crew_id = b.crew_id AND a.deleted_at IS NULL
			WHERE b.scope = 'CREW' AND b.credential_id IN (`+marks+`)
			UNION ALL
			SELECT b.credential_id, a.id FROM credential_bindings b
			JOIN agents a ON a.workspace_id = b.workspace_id AND a.deleted_at IS NULL
			WHERE b.scope = 'WORKSPACE' AND b.credential_id IN (`+marks+`)
		) GROUP BY credential_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("pays_for agents: %w", err)
	}
	for agentRows.Next() {
		var id string
		var n int
		if err := agentRows.Scan(&id, &n); err != nil {
			agentRows.Close()
			return nil, err
		}
		p := out[id]
		p.Agents = n
		out[id] = p
	}
	agentRows.Close()

	crewRows, err := db.QueryContext(ctx, `
		SELECT credential_id, COUNT(DISTINCT crew_id) FROM (
			SELECT b.credential_id AS credential_id, b.crew_id AS crew_id FROM credential_bindings b
			JOIN crews cr ON cr.id = b.crew_id AND cr.deleted_at IS NULL
			WHERE b.scope = 'CREW' AND b.credential_id IN (`+marks+`)
			UNION ALL
			SELECT cc.credential_id, cc.crew_id FROM credential_crews cc
			JOIN crews cr ON cr.id = cc.crew_id AND cr.deleted_at IS NULL
			WHERE cc.credential_id IN (`+marks+`)
		) GROUP BY credential_id`, append(idsAsArgs(ids), idsAsArgs(ids)...)...)
	if err != nil {
		return nil, fmt.Errorf("pays_for crews: %w", err)
	}
	defer crewRows.Close()
	for crewRows.Next() {
		var id string
		var n int
		if err := crewRows.Scan(&id, &n); err != nil {
			return nil, err
		}
		p := out[id]
		p.Crews = n
		out[id] = p
	}
	return out, crewRows.Err()
}

// providerLoginRow is one seat as the Paymaster lists it (§5.5): the
// credential's identity plus its login object.
type providerLoginRow struct {
	CredentialID string    `json:"credential_id"`
	Name         string    `json:"name"`
	Type         string    `json:"type"`
	Status       string    `json:"status"`
	Login        loginView `json:"login"`
}

// listProviderLogins returns every login row in a workspace, newest first.
// Non-nil so a workspace with none serialises as [].
func listProviderLogins(ctx context.Context, db *sql.DB, logger *slog.Logger, workspaceID string) ([]providerLoginRow, error) {
	frag, args := loginRowsSQL()
	rows, err := db.QueryContext(ctx, `
		SELECT c.id, c.name, c.type, COALESCE(c.provider,''), c.status, COALESCE(c.created_by,''), COALESCE(c.token_expires_at,''), c.encrypted_value
		FROM credentials c WHERE c.workspace_id = ? AND c.deleted_at IS NULL`+frag+`
		ORDER BY c.created_at DESC, c.id DESC`, append([]any{workspaceID}, args...)...)
	if err != nil {
		return []providerLoginRow{}, err
	}
	var creds []credentialResponse
	sources := map[string]loginSource{}
	statuses := map[string]string{}
	for rows.Next() {
		var c credentialResponse
		var src loginSource
		var status string
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.Provider, &status, &src.CreatedBy, &src.TokenExpiresAt, &src.EncryptedValue); err != nil {
			rows.Close()
			return []providerLoginRow{}, err
		}
		sources[c.ID] = src
		statuses[c.ID] = status
		creds = append(creds, c)
	}
	rows.Close()
	attachLoginViews(ctx, db, logger, creds, sources)
	out := make([]providerLoginRow, 0, len(creds))
	for _, c := range creds {
		if c.Login == nil {
			continue
		}
		out = append(out, providerLoginRow{CredentialID: c.ID, Name: c.Name, Type: c.Type, Status: statuses[c.ID], Login: *c.Login})
	}
	return out, nil
}

// loadLoginView builds the login object for ONE credential by id, for the
// paths that answer about a single row (create, refresh, pays_with). Returns
// nil when the row carries no login.
func loadLoginView(ctx context.Context, db *sql.DB, logger *slog.Logger, credID string) (*loginView, string, error) {
	var src loginSource
	var name string
	var tokenExp, createdBy sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT id, name, type, COALESCE(provider,''), created_by, token_expires_at, encrypted_value
		 FROM credentials WHERE id = ? AND deleted_at IS NULL`, credID).
		Scan(&src.ID, &name, &src.Type, &src.Provider, &createdBy, &tokenExp, &src.EncryptedValue)
	if err != nil {
		return nil, "", err
	}
	src.CreatedBy, src.TokenExpiresAt = createdBy.String, tokenExp.String
	if !isLoginRow(src.Type, src.Provider) {
		return nil, name, nil
	}
	rows := []credentialResponse{{ID: src.ID, Type: src.Type, Provider: src.Provider}}
	attachLoginViews(ctx, db, logger, rows, map[string]loginSource{src.ID: src})
	return rows[0].Login, name, nil
}
