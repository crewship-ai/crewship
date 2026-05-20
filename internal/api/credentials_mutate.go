package api

// Credential write paths — Create + Update. Each carries provider-
// specific validation and encrypted-storage setup, so they're large
// enough to deserve their own file. Extracted from credentials.go.

import (
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// provisionedForServiceRe enforces the canonical `<crew>/<service>`
// shape: two non-empty DNS-label-safe segments separated by exactly
// one slash. Mirrors the crew + service name regex
// internal/manifest/validate.go uses on the manifest side. Without
// this gate, callers could write whitespace, malformed strings, or
// multi-slash tags that break the cross-crew collision detection
// (which keys on the literal value).
var provisionedForServiceRe = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?/[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

type createCredentialRequest struct {
	Name          string   `json:"name"`
	Description   *string  `json:"description"`
	Value         string   `json:"value"`
	Type          string   `json:"type"`
	Provider      string   `json:"provider"`
	Scope         string   `json:"scope"`
	CrewID        *string  `json:"crew_id"`
	CrewIDs       []string `json:"crew_ids"`
	Tags          []string `json:"tags"`
	AccountLabel  *string  `json:"account_label"`
	AccountEmail  *string  `json:"account_email"`
	RefreshToken  *string  `json:"refresh_token"`
	TokenExpires  *string  `json:"token_expires_at"`
	SecurityLevel *int     `json:"security_level"`
	// Attribution (v98). Together these answer "who created this
	// credential row?" — the existing `created_by` column captures
	// the calling user, but Crewship now also writes credentials on
	// behalf of agents (sidecar auto-managed passwords) and system
	// processes (seed / backup restore). When CreatedByActorType is
	// 'agent' the caller MUST be OWNER or ADMIN — anyone with lower
	// role lacks the authority to attribute a workspace mutation to
	// a non-self actor. Default 'user' with the authenticated user.
	CreatedByActorType *string `json:"created_by_actor_type,omitempty"`
	CreatedByActorID   *string `json:"created_by_actor_id,omitempty"`
	// ProvisionedForService marks the row as owned by a specific
	// sidecar service declaration in `<crew-slug>/<service-name>`
	// form. Non-empty rows are treated as Crewship-managed in the UI:
	// reveal / edit actions are hidden, rotate is the only mutation.
	// Manifest apply sets this for AUTO_MANAGED rows.
	ProvisionedForService *string `json:"provisioned_for_service,omitempty"`
	// USERPASS: cleartext identifier half (e.g. "user@gmail.com").
	// Stored unencrypted in credentials.username because usernames are
	// identifiers, not secrets — mirrors Bitwarden's login.username
	// shape. The password lives in the existing encrypted Value field.
	Username *string `json:"username"`
	// OAuth 2.0 fields (used when type = OAUTH2)
	OAuthClientID     *string `json:"oauth_client_id"`
	OAuthClientSecret *string `json:"oauth_client_secret"`
	OAuthAuthURL      *string `json:"oauth_auth_url"`
	OAuthTokenURL     *string `json:"oauth_token_url"`
	OAuthScopes       *string `json:"oauth_scopes"`
	// Pending, when true, creates a placeholder credential without a real
	// value — the row's status is set to PENDING and the encrypted_value
	// holds a sentinel. Used by `crewship apply -f` so a manifest can
	// declare credential slots that the user fills in later through the
	// UI or `crewship credential set`. Mirrors the OAuth-pending path
	// (value="pending_oauth") which is already wired into the resolver,
	// so no orchestrator changes are needed: pending creds simply fail
	// agent runs with a "credential not configured" error until the user
	// supplies a real value.
	Pending bool `json:"pending"`
}

// attributionError is the shape resolveCreateAttribution returns
// on rejection — wraps the HTTP status + message so the caller
// (Create) just maps to replyError. Kept private to this file
// because no other handler needs this exact pair.
type attributionError struct {
	status  int
	message string
}

// resolveCreateAttribution implements the (actor_type, actor_id)
// resolution rules described above the Create handler's call site.
// Returns the resolved (type, id, nil) on success, or
// (zero, nil, *attributionError) when the request shape is
// malformed or the caller's role doesn't authorise the requested
// attribution.
//
// The function is intentionally outside the Create method body so
// it can be unit-tested without spinning up an HTTP request +
// authed context — see credentials_mutate_attribution_test.go.
func resolveCreateAttribution(req createCredentialRequest, user *AuthUser, role string) (string, *string, *attributionError) {
	actorType := "user"
	if req.CreatedByActorType != nil && *req.CreatedByActorType != "" {
		actorType = *req.CreatedByActorType
	}
	switch actorType {
	case "user", "agent", "system":
		// valid
	default:
		return "", nil, &attributionError{http.StatusBadRequest, "created_by_actor_type must be user|agent|system"}
	}

	// OWNER/ADMIN gate for any non-default actor type. Lower roles
	// can only attribute to themselves.
	isPrivileged := role == "OWNER" || role == "ADMIN"
	if actorType != "user" && !isPrivileged {
		return "", nil, &attributionError{http.StatusForbidden, "non-self actor attribution requires OWNER or ADMIN role"}
	}

	var actorID *string
	switch actorType {
	case "user":
		// Default to self. Allow overriding to another user.id ONLY
		// for OWNER/ADMIN (e.g. admin migrating ownership); a
		// regular user that supplies a foreign id is a spoof attempt.
		id := user.ID
		if req.CreatedByActorID != nil && *req.CreatedByActorID != "" && *req.CreatedByActorID != user.ID {
			if !isPrivileged {
				return "", nil, &attributionError{http.StatusForbidden, "created_by_actor_id must match authenticated user"}
			}
			id = *req.CreatedByActorID
		}
		actorID = &id
	case "agent":
		// Agent attribution MUST carry an explicit agent id — we
		// don't silently fall back to the caller (that's exactly the
		// spoof shape we're protecting against).
		if req.CreatedByActorID == nil || *req.CreatedByActorID == "" {
			return "", nil, &attributionError{http.StatusBadRequest, "created_by_actor_id is required when created_by_actor_type=agent"}
		}
		id := *req.CreatedByActorID
		actorID = &id
	case "system":
		// System actors have no natural id. A caller-supplied id is
		// rejected to keep the row's actor_id NULL — querying for
		// "system rows" stays a simple actor_type filter.
		if req.CreatedByActorID != nil && *req.CreatedByActorID != "" {
			return "", nil, &attributionError{http.StatusBadRequest, "created_by_actor_id must be empty when created_by_actor_type=system"}
		}
	}
	return actorType, actorID, nil
}

// Create stores a new encrypted credential in the workspace.
// POST /api/v1/credentials

func (h *CredentialHandler) Create(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	user := UserFromContext(r.Context())

	// MANAGER tier can create credentials — the FE CASL ability mirrors
	// this. "manage" was historically too tight (OWNER+ADMIN only) and
	// caused 403s for the Add flow even though the button rendered.
	if !canRole(role, "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	// Defence in depth: authed middleware should always populate user,
	// but a future middleware reorder bug should not crash the write
	// path. Other call sites in this file already guard for nil; mirror
	// the pattern here.
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var req createCredentialRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	if req.Name == "" || len(req.Name) < 1 || len(req.Name) > 255 {
		replyError(w, http.StatusBadRequest, "name is required")
		return
	}
	// Pending credentials (manifest slot path) bypass the value check —
	// the whole point is "create the row now, fill the value later". The
	// sentinel mirrors the OAuth-pending pattern so the resolver path
	// already handles it: agent runs fail with "credential not configured"
	// instead of leaking the placeholder to the LLM.
	manifestPending := false
	if req.Pending && req.Value == "" {
		req.Value = pendingSentinelManifest
		manifestPending = true
	}
	if req.Value == "" && req.Type != "OAUTH2" {
		replyError(w, http.StatusBadRequest, "value is required")
		return
	}
	oauthPending := false
	if req.Value == "" && req.Type == "OAUTH2" {
		req.Value = pendingSentinelOAuth // placeholder until OAuth flow completes
		oauthPending = true
	}

	if req.Type == "" {
		req.Type = "SECRET"
	}
	if req.Provider == "" {
		req.Provider = "NONE"
	}
	if req.Scope == "" {
		req.Scope = "WORKSPACE"
	}

	// Per-type validation. Pending manifest slots skip the value-shape
	// checks (PEM markers, USERPASS-username pairing) since the value
	// is intentionally a placeholder until the user fills it in — but
	// the closed type enum is still enforced so an unknown type cannot
	// slip through as a "pending" of something we don't support.
	if manifestPending {
		if msg := validateCredentialType(req.Type); msg != "" {
			replyError(w, http.StatusBadRequest, msg)
			return
		}
	} else if msg := validateCredentialPayload(&req); msg != "" {
		replyError(w, http.StatusBadRequest, msg)
		return
	}

	// Merge crew_ids and legacy crew_id into a single list
	crewIDs := req.CrewIDs
	if req.CrewID != nil && *req.CrewID != "" {
		found := false
		for _, id := range crewIDs {
			if id == *req.CrewID {
				found = true
				break
			}
		}
		if !found {
			crewIDs = append(crewIDs, *req.CrewID)
		}
	}

	// Auto-set scope when crews are provided
	if len(crewIDs) > 0 {
		req.Scope = "CREW"
	}

	// Validate all crew IDs
	for _, cid := range crewIDs {
		crewFound, err := crewExists(r.Context(), h.db, cid, workspaceID)
		if err != nil {
			h.logger.Error("crew exists check", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		if !crewFound {
			replyError(w, http.StatusBadRequest, fmt.Sprintf("Invalid crew_id: %s", cid))
			return
		}
	}

	// Keep legacy crew_id field pointing to first crew for backwards compat
	var legacyCrewID *string
	if len(crewIDs) > 0 {
		legacyCrewID = &crewIDs[0]
	}

	// Remove soft-deleted credential with same name so the INSERT doesn't hit a unique constraint.
	if _, err := h.db.ExecContext(r.Context(),
		"DELETE FROM credentials WHERE workspace_id = ? AND name = ? AND deleted_at IS NOT NULL",
		workspaceID, req.Name); err != nil {
		h.logger.Warn("cleanup soft-deleted credential", "name", req.Name, "error", err)
	}

	// Resolve attribution (v98). Done BEFORE BeginTx so a malformed
	// request doesn't open and abandon a transaction — see the
	// "transaction leak" trail at credentials_mutate_test.go.
	//
	// Three valid (actor_type, actor_id) shapes:
	//
	//   ('user',   <user-id>)   — default. The caller can override
	//                              actor_id only as OWNER/ADMIN (e.g.
	//                              an admin auditing-in another user's
	//                              ownership); a regular user that
	//                              specifies a non-self actor_id is
	//                              treated as a spoof attempt and
	//                              rejected.
	//   ('agent',  <agent-id>)  — requires OWNER/ADMIN (manifest apply
	//                              dispatch runs as the workspace
	//                              owner). actor_id is REQUIRED — a
	//                              nil/empty payload is rejected so
	//                              we never silently fall back to
	//                              "self as agent."
	//   ('system', <empty>)     — server-side machinery (manifest
	//                              dispatch for v98 AUTO_MANAGED).
	//                              actor_id is intentionally nil; no
	//                              one specific natural identity
	//                              maps to "system." Requires
	//                              OWNER/ADMIN — same reasoning.
	actorType, actorID, attrErr := resolveCreateAttribution(req, user, role)
	if attrErr != nil {
		replyError(w, attrErr.status, attrErr.message)
		return
	}
	// provisioned_for_service marks the row as Crewship-owned (the UI
	// hides reveal / edit / delete on these). Two layers of gating:
	//
	//   1. Provenance gate — the legitimate stamping path is exactly
	//      one shape: manifest apply's auto-managed dispatch posts
	//      (provider=AUTO_MANAGED, actor_type=system,
	//      provisioned_for_service=<crew>/<svc>). Any other combo is
	//      a spoof attempt; the UI badge must not be opportunistic.
	//
	//   2. Shape gate — even with the right provenance, the value
	//      itself has to be canonical `<crew>/<service>` with two
	//      non-empty DNS-label segments. The cross-crew collision
	//      detection in the manifest dispatch keys on this exact
	//      string; whitespace, missing slash, or multi-slash junk
	//      would silently break that check downstream.
	//
	// Empty / whitespace-only is treated as "not stamped" and falls
	// through to the nil branch — same as omitting the field.
	var provisionedForService *string
	if req.ProvisionedForService != nil {
		canonical := strings.TrimSpace(*req.ProvisionedForService)
		if canonical != "" {
			if req.Provider != "AUTO_MANAGED" || actorType != "system" {
				replyError(w, http.StatusBadRequest,
					"provisioned_for_service is reserved for AUTO_MANAGED system credentials (set provider=AUTO_MANAGED and created_by_actor_type=system, or omit this field)")
				return
			}
			if !provisionedForServiceRe.MatchString(canonical) {
				replyError(w, http.StatusBadRequest,
					"provisioned_for_service must be canonical <crew>/<service> with two non-empty DNS-label segments")
				return
			}
			provisionedForService = &canonical
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	credID := generateCUID()

	encryptedValue, err := encryption.Encrypt(req.Value)
	if err != nil {
		h.logger.Error("encrypt credential", "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to encrypt credential")
		return
	}

	secLevel := 1
	if req.SecurityLevel != nil && *req.SecurityLevel >= 1 && *req.SecurityLevel <= 3 {
		secLevel = *req.SecurityLevel
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	credStatus := "ACTIVE"
	if oauthPending || manifestPending {
		credStatus = "PENDING"
	}

	var tagsArg any
	if encoded, ok := encodeTagsJSON(req.Tags); ok {
		tagsArg = encoded
	}

	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO credentials (id, workspace_id, name, description, encrypted_value,
			type, provider, scope, crew_id, account_label, account_email, username,
			token_expires_at, security_level, status, tags, created_by, created_at, updated_at,
			created_by_actor_type, created_by_actor_id, provisioned_for_service)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		credID, workspaceID, req.Name, req.Description, encryptedValue,
		req.Type, req.Provider, req.Scope, legacyCrewID, req.AccountLabel, req.AccountEmail, req.Username,
		req.TokenExpires, secLevel, credStatus, tagsArg, user.ID, now, now,
		actorType, actorID, provisionedForService)
	if err != nil {
		tx.Rollback()
		if strings.Contains(err.Error(), "UNIQUE") {
			replyError(w, http.StatusConflict, "Credential with this name already exists")
			return
		}
		h.logger.Error("insert credential", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Store OAuth fields if type is OAUTH2
	if req.Type == "OAUTH2" && req.OAuthClientID != nil {
		var encClientSecret string
		if req.OAuthClientSecret != nil && *req.OAuthClientSecret != "" {
			encClientSecret, err = encryption.Encrypt(*req.OAuthClientSecret)
			if err != nil {
				tx.Rollback()
				h.logger.Error("encrypt OAuth client secret", "error", err)
				replyError(w, http.StatusInternalServerError, "Internal server error")
				return
			}
		}
		if _, err := tx.ExecContext(r.Context(), `
			UPDATE credentials SET oauth_client_id = ?, oauth_client_secret_enc = ?,
				oauth_auth_url = ?, oauth_token_url = ?, oauth_scopes = ?
			WHERE id = ?`,
			req.OAuthClientID, encClientSecret,
			req.OAuthAuthURL, req.OAuthTokenURL, req.OAuthScopes,
			credID); err != nil {
			tx.Rollback()
			h.logger.Error("store OAuth fields", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	if err := h.setCrewIDs(r.Context(), tx, credID, crewIDs); err != nil {
		tx.Rollback()
		h.logger.Error("set credential crews", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit credential", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Stamp the timeline so the detail-sheet Audit tab shows when the
	// credential first appeared. Outside the create tx — best-effort.
	if recErr := RecordCredentialEvent(r.Context(), h.db, h.logger, credID, AuditEventCreated, "", clientIP(r),
		map[string]any{"created_by": user.ID, "provider": req.Provider, "type": req.Type}); recErr != nil {
		// TODO(metrics): when an OpenTelemetry counter is wired into
		// this package, increment credential_audit_record_failures
		// here so ops can alarm on lost compliance events.
		h.logger.Warn("record CREATED audit event", "error", recErr, "credential_id", credID)
	}

	respCrewIDs := crewIDs
	if respCrewIDs == nil {
		respCrewIDs = []string{}
	}
	respTags := normaliseTags(req.Tags)
	if respTags == nil {
		respTags = []string{}
	}

	// v98 attribution echoes back to the caller so the UI can render
	// the "Crewship-managed" badge immediately after create. Pre-fix
	// the response builder dropped these three fields, so the UI saw
	// a normal credential with null provenance until the next list
	// refresh — at which point the SELECT picked them up from the
	// row that had been carrying them all along.
	actorTypeResp := &actorType
	writeJSON(w, http.StatusCreated, credentialResponse{
		ID:                    credID,
		Name:                  req.Name,
		Description:           req.Description,
		Type:                  req.Type,
		Provider:              req.Provider,
		Status:                credStatus,
		Scope:                 req.Scope,
		CrewID:                legacyCrewID,
		CrewIDs:               respCrewIDs,
		Tags:                  respTags,
		AccountLabel:          req.AccountLabel,
		AccountEmail:          req.AccountEmail,
		Username:              req.Username,
		LastUsedIPs:           []string{},
		CreatedAt:             now,
		UpdatedAt:             now,
		AgentNames:            []string{},
		CreatedByActorType:    actorTypeResp,
		CreatedByActorID:      actorID,
		ProvisionedForService: provisionedForService,
	})
}

// Get returns a single credential by ID (without the secret value).
// GET /api/v1/credentials/{credentialId}

func (h *CredentialHandler) Update(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("credentialId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "update") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	// Load current type+username up front so the merged-payload
	// validation below sees the persisted state, not just the patch.
	// Doubles as the existence + workspace-scoping check that
	// credentialExists used to do — single round-trip, same gate.
	var currentType string
	var currentUsername sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		`SELECT type, username FROM credentials
		 WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		credID, workspaceID).Scan(&currentType, &currentUsername)
	if err != nil {
		if err == sql.ErrNoRows {
			replyError(w, http.StatusNotFound, "Credential not found")
			return
		}
		h.logger.Error("load credential for update", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	var body map[string]interface{}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	// Merged-payload validation: overlay patch fields onto current row
	// state, then enforce the closed enum + per-type invariants the
	// Create path checks. Without this, a PATCH can drop a credential
	// into an inconsistent state — set type=USERPASS without a
	// username, change type to SSH_KEY while the stored value is a
	// raw API key, paste a public key over an existing SSH_KEY's
	// value, etc. — that the resolver then trips over at agent-run
	// time. Create blocks all of these; Update used to silently
	// accept them.
	//
	// JSON-shape gating: type/username/value must be either absent or
	// a string (or null, where the column is nullable). A naked
	// `v.(string)` assertion with `ok` silently treats a non-string
	// like the field is missing, but the downstream `for jsonKey, col`
	// loop still calls `ub.Set(col, val)` with the raw `any`, writing
	// e.g. a numeric `{"type": 123}` straight into the TEXT column
	// as "123" — past the closed-enum check, past any sane downstream
	// resolver behaviour. Fail closed up-front instead.
	mergedType := currentType
	if v, ok := body["type"]; ok {
		s, isStr := v.(string)
		if !isStr {
			replyError(w, http.StatusBadRequest, "type must be a string")
			return
		}
		mergedType = s
	}
	if _, ok := validCredentialTypes[mergedType]; !ok {
		replyError(w, http.StatusBadRequest, "type must be one of: AI_CLI_TOKEN, API_KEY, CLI_TOKEN, SECRET, OAUTH2, USERPASS, SSH_KEY, CERTIFICATE, GENERIC_SECRET")
		return
	}

	// Per-type field rules. Skip value-shape checks for SSH_KEY and
	// CERTIFICATE when the patch doesn't touch `value` — we trust the
	// stored encrypted blob was PEM-shaped when it was written by
	// Create. Only the cases where the patch could newly violate the
	// invariant need to fail closed here.
	valueSent := false
	valueStr := ""
	if v, ok := body["value"]; ok {
		s, isStr := v.(string)
		if !isStr {
			replyError(w, http.StatusBadRequest, "value must be a string")
			return
		}
		if s != "" {
			valueSent = true
			valueStr = s
		}
	}
	typeChanged := mergedType != currentType

	switch mergedType {
	case CredTypeUserPass:
		// USERPASS must always end up with a non-empty username.
		// Effective username = patch username if sent, else current.
		// null clears it (rejected below); non-string is a malformed
		// request (rejected up-front, like type/value above).
		effectiveUsername := currentUsername.String
		if v, ok := body["username"]; ok {
			switch s := v.(type) {
			case string:
				effectiveUsername = s
			case nil:
				effectiveUsername = ""
			default:
				replyError(w, http.StatusBadRequest, "username must be a string")
				return
			}
		}
		if strings.TrimSpace(effectiveUsername) == "" {
			replyError(w, http.StatusBadRequest, "username is required for USERPASS credentials")
			return
		}

	case CredTypeSSHKey:
		// Changing TO SSH_KEY requires a new value — we can't validate
		// the existing encrypted blob's shape without decrypting it
		// in the hot path, and the existing value almost certainly
		// isn't PEM-shaped if the row was previously API_KEY/SECRET/etc.
		if typeChanged && !valueSent {
			replyError(w, http.StatusBadRequest, "changing type to SSH_KEY requires a new value")
			return
		}
		if valueSent && !looksLikePEM(valueStr, "PRIVATE KEY") {
			replyError(w, http.StatusBadRequest, "ssh key must be a PEM-encoded private key (begins with -----BEGIN ... PRIVATE KEY-----)")
			return
		}

	case CredTypeCertificate:
		if typeChanged && !valueSent {
			replyError(w, http.StatusBadRequest, "changing type to CERTIFICATE requires a new value")
			return
		}
		if valueSent && !looksLikePEM(valueStr, "CERTIFICATE") {
			replyError(w, http.StatusBadRequest, "certificate must be PEM-encoded (begins with -----BEGIN CERTIFICATE-----)")
			return
		}
	}

	// Note: "status" is intentionally excluded to prevent users from
	// re-activating revoked/expired credentials. Status changes are
	// managed by the credential monitor and OAuth refresh worker.
	allowed := map[string]string{
		"name": "name", "description": "description", "type": "type",
		"provider": "provider", "scope": "scope",
		"crew_id": "crew_id", "account_label": "account_label",
		"account_email": "account_email", "token_expires_at": "token_expires_at",
		"security_level": "security_level", "username": "username",
	}

	// Parse crew_ids if provided — will be written to junction table
	var updateCrewIDs bool
	var crewIDs []string
	if raw, ok := body["crew_ids"]; ok {
		updateCrewIDs = true
		if arr, ok := raw.([]interface{}); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok && s != "" {
					crewIDs = append(crewIDs, s)
				}
			}
		}
		// Validate all crew IDs
		for _, cid := range crewIDs {
			ok, err := crewExists(r.Context(), h.db, cid, workspaceID)
			if err != nil {
				h.logger.Error("crew exists check", "error", err)
				replyError(w, http.StatusInternalServerError, "Internal server error")
				return
			}
			if !ok {
				replyError(w, http.StatusBadRequest, fmt.Sprintf("Invalid crew_id: %s", cid))
				return
			}
		}
		// Auto-update scope and legacy crew_id
		if len(crewIDs) > 0 {
			body["scope"] = "CREW"
			body["crew_id"] = crewIDs[0]
		} else {
			body["scope"] = "WORKSPACE"
			body["crew_id"] = nil
		}
		delete(body, "crew_ids")
	} else if crewIDVal, ok := body["crew_id"]; ok && crewIDVal != nil {
		// Legacy single-crew patch path. Mirror the new shape-loop's
		// strictness so a non-string crew_id fails closed here rather
		// than silently being caught by the downstream loop — keeps
		// behaviour stable if a future refactor moves the loop and
		// makes the failure mode discoverable from the right call site.
		crewIDStr, ok := crewIDVal.(string)
		if !ok {
			replyError(w, http.StatusBadRequest, "crew_id must be a string")
			return
		}
		if crewIDStr != "" {
			crewFound, err := crewExists(r.Context(), h.db, crewIDStr, workspaceID)
			if err != nil {
				h.logger.Error("crew exists check", "error", err)
				replyError(w, http.StatusInternalServerError, "Internal server error")
				return
			}
			if !crewFound {
				replyError(w, http.StatusBadRequest, "Invalid crew_id")
				return
			}
		}
	}

	ub := newUpdate()
	// valueRotated flips when this PATCH actually replaces the encrypted
	// secret — drives the post-commit audit ROTATE event so silent
	// in-place rewrites (the Vercel-style inline Save value flow) still
	// land on the timeline.
	valueRotated := false

	// Handle value separately (needs encryption)
	if val, ok := body["value"]; ok {
		if s, ok := val.(string); ok && s != "" {
			encrypted, err := encryption.Encrypt(s)
			if err != nil {
				h.logger.Error("encrypt credential value", "error", err)
				replyError(w, http.StatusInternalServerError, "Failed to encrypt credential")
				return
			}
			ub.Set("encrypted_value", encrypted)
			// Reset status when value changes so monitor re-validates
			ub.Set("status", "ACTIVE")
			ub.SetNull("last_error")
			valueRotated = true
		}
	}

	// Tags: accept either a JSON array or null. Empty/missing arrays
	// clear the column so the row goes back to NULL rather than "[]".
	if raw, ok := body["tags"]; ok {
		var tags []string
		if arr, ok := raw.([]interface{}); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok {
					tags = append(tags, s)
				}
			}
		}
		if encoded, ok := encodeTagsJSON(tags); ok {
			ub.Set("tags", encoded)
		} else {
			ub.SetNull("tags")
		}
	}

	// security_level is the only typed scalar in `allowed`; mirror the
	// Create path's 1..3 validation so a PATCH can't smuggle a string
	// into the INTEGER column (SQLite stores by storage class but won't
	// reject the type mismatch).
	if raw, ok := body["security_level"]; ok {
		var n int
		switch v := raw.(type) {
		case float64:
			n = int(v)
		case int:
			n = v
		default:
			replyError(w, http.StatusBadRequest, "security_level must be 1, 2, or 3")
			return
		}
		if n < 1 || n > 3 {
			replyError(w, http.StatusBadRequest, "security_level must be 1, 2, or 3")
			return
		}
		body["security_level"] = n
	}

	// JSON-shape gate for the generic allowed map. type/value/username
	// already got their explicit assertions above; this loop catches
	// the rest (provider/scope/crew_id/account_label/account_email/
	// token_expires_at) — without it, `{"provider": 123}` would
	// silently land "123" in the TEXT column. security_level is
	// numeric by design and has its own validator earlier; null is
	// accepted because it nulls the column (legitimate operation for
	// the *nullable string fields).
	for jsonKey, col := range allowed {
		val, ok := body[jsonKey]
		if !ok {
			continue
		}
		switch jsonKey {
		case "security_level":
			// Already normalised to an int above.
		case "type", "username":
			// Already typed above; the merged-validation block ran on
			// the same body and rejected non-strings before we got here.
		default:
			// Remaining keys are all string-or-null TEXT columns.
			switch val.(type) {
			case string, nil:
				// OK.
			default:
				replyError(w, http.StatusBadRequest, jsonKey+" must be a string")
				return
			}
		}
		ub.Set(col, val)
	}

	if ub.Empty() && !updateCrewIDs {
		replyError(w, http.StatusBadRequest, "No fields to update")
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx (update credential)", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	if !ub.Empty() {
		query, args := ub.Build("credentials", "id = ? AND workspace_id = ?", credID, workspaceID)
		if _, err := tx.ExecContext(r.Context(), query, args...); err != nil {
			tx.Rollback()
			h.logger.Error("update credential", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	if updateCrewIDs {
		if err := h.setCrewIDs(r.Context(), tx, credID, crewIDs); err != nil {
			tx.Rollback()
			h.logger.Error("update credential crews", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit credential update", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Inline value rewrite (Vercel-parity quick rotation) is logically
	// a rotation — no grace overlap, but the timeline must still see
	// it. Outside the tx so a slow audit insert never rolls back the
	// rotation itself.
	if valueRotated {
		var rotatedBy string
		if u := UserFromContext(r.Context()); u != nil {
			rotatedBy = u.ID
		}
		if recErr := RecordCredentialEvent(r.Context(), h.db, h.logger, credID, AuditEventRotate, "", clientIP(r),
			map[string]any{"mode": "inline", "rotated_by": rotatedBy}); recErr != nil {
			h.logger.Warn("record inline-rotate audit event", "error", recErr, "credential_id", credID)
		}
	}

	h.Get(w, r)
}

// Delete removes a credential and all its agent assignments.
// DELETE /api/v1/credentials/{credentialId}
