package api

// Multi-part credentials — PRD-CREDENTIALS-V2-2026 §2.2, and the fix for the
// §1.5 V5 defect.
//
// A credential used to be one encrypted value plus an optional cleartext
// username. That shape cannot express AWS static credentials (access key id +
// secret + region), a service-account JSON (blob + filename), or anything
// carrying a TOTP seed, a passphrase, a host or an account id. USERPASS was
// made to fit by adding a `username` column, which does not generalise: the
// next multi-part type adds another column and every reader of the credentials
// row grows a branch it did not ask for.
//
// So: named parts in their own table, with `is_secret` deciding whether a part
// is encrypted or stored as a cleartext identifier. Six item types plus
// arbitrary fields cover thousands of tools without a type per tool, which is
// the Vaultwarden answer and the one the PRD adopts.
//
// SCOPE. This file is storage, CRUD and validation. It does NOT deliver
// anything: how one credential becomes N env vars or files is
// credential_delivery.go / exec_env.go / exec_sidecar.go, deliberately
// untouched here. Existing single-value delivery keeps working because
// `credentials.encrypted_value` and `credentials.username` are unchanged and
// this table is purely additive — nothing was backfilled into it (see the
// migration's comment for why a backfill would be actively harmful).
//
// THE ONE INVARIANT. A secret field's value never appears in a response. The
// enforcement is structural rather than a redaction step: every read path's
// SELECT omits `encrypted_value` entirely, so the ciphertext is not even
// loaded into the process, and there is nothing for a future struct-field
// addition or a helpful error message to leak. A redaction that has to be
// remembered is a redaction that gets forgotten.

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// CredentialFieldHandler owns the field sub-resource. Separate from
// CredentialHandler for the same reason CredentialRevealHandler is: the rules
// about what may and may not cross the wire are the whole point of this
// surface, and keeping them in one file means the disclosure behaviour can be
// reviewed by reading one file rather than by auditing a large handler's
// every method.
type CredentialFieldHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewCredentialFieldHandler(db *sql.DB, logger *slog.Logger) *CredentialFieldHandler {
	return &CredentialFieldHandler{db: db, logger: logger}
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// credentialFieldKeyRe is lower_snake_case, starting with a letter, at most 64
// characters.
//
// Lowercase-only is not cosmetic. A field key becomes an env-var suffix and a
// file basename once the delivery phase lands, where `Region` and `region`
// collide the moment either is upcased to REGION — two fields that validate
// independently and then fight over one destination. Rejecting the ambiguity
// now costs a 400; discovering it later costs a rename migration on live
// vaults. The leading-letter rule keeps `1region` (not a legal shell
// identifier) and `_region` (reads as reserved) out for the same reason, and
// the character class excludes newlines and NULs, which are the injection
// shapes for anything that later writes `KEY=value` lines.
var credentialFieldKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// reservedCredentialFieldKeys are the names that already have a home on the
// credentials row. Allowing them here would create a SECOND writable copy of
// one datum with no owner: `credential update --username` writes the column, a
// field write writes the row, and the delivery path — which reads the column —
// keeps using the stale one while the UI shows the fresh one. Silent
// divergence inside a vault is worse than a rejected request, so this fails
// loudly and names the field that already exists.
var reservedCredentialFieldKeys = map[string]string{
	"username": "credentials.username (set it with `crewship credential update --username`)",
	"value":    "the credential's own value (rotate it with `crewship credential rotate`)",
	"password": "the credential's own value — for USERPASS the password IS that value",
}

// maxCredentialFieldValueLen caps one field's value at the same 64 KiB as
// credentials.encrypted_value (maxCredentialValueLen). Deliberately the same
// number rather than a second one: a field value IS a credential value — a
// blob, a PEM, a fat JWT — and two caps for one concept is two numbers to keep
// in sync and one 400 message that contradicts the other. The global 1 MB
// request-body limit still bounds any single request below the theoretical
// per-credential total.
const maxCredentialFieldValueLen = maxCredentialValueLen

// maxCredentialFields caps how many parts one credential may carry. No real
// credential shape comes close — the fattest in the PRD's table is a
// CERTIFICATE with three PEMs — so this is not a product limit, it is a bound
// on what a compromised or buggy client can do to a single row (and to every
// boot payload that will eventually carry it).
const maxCredentialFields = 32

// validateCredentialFieldKey returns an end-user-readable 400 message, or "".
func validateCredentialFieldKey(key string) string {
	if key == "" {
		return "field key is required"
	}
	if len(key) > 64 {
		return "field key is too long (max 64 characters)"
	}
	if !credentialFieldKeyRe.MatchString(key) {
		return "field key must be lower_snake_case, start with a letter, and contain only a-z, 0-9 and _ " +
			"(it becomes an environment-variable name and a file name when the credential is delivered)"
	}
	if where, reserved := reservedCredentialFieldKeys[key]; reserved {
		return "field key \"" + key + "\" is reserved: that value lives on " + where +
			". Two copies of one secret drift apart silently — use a different key."
	}
	return ""
}

// validateCredentialFieldValue returns an end-user-readable 400 message, or "".
func validateCredentialFieldValue(value string) string {
	if value == "" {
		return "field value is required (delete the field instead of storing an empty one)"
	}
	if len(value) > maxCredentialFieldValueLen {
		return "field value is too long (max 65536 bytes)"
	}
	return ""
}

// ---------------------------------------------------------------------------
// Wire types
// ---------------------------------------------------------------------------

// credentialFieldRequest is the body for both Create and Update. Key is read
// only by Create — Update takes it from the path, so a body that disagrees
// with the URL cannot silently rename a field.
//
// IsSecret is a pointer so "omitted" is distinguishable from "false", and
// omitted defaults to TRUE. That default is the whole reason for the pointer:
// a client that forgot to say whether a part is secret must get it encrypted,
// because the case nobody tests by hand is the one that has to fail safe.
type credentialFieldRequest struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	IsSecret *bool  `json:"is_secret"`
	Ordinal  *int   `json:"ordinal"`
}

// credentialFieldResponse is what every read path returns.
//
// Value is a pointer and is non-nil ONLY for a non-secret field. There is no
// "redacted" placeholder and no length, prefix or fingerprint of a secret
// value — those look harmless and are how a plaintext ends up in a log
// aggregator (the same reasoning as revealResponse).
type credentialFieldResponse struct {
	Key      string `json:"key"`
	IsSecret bool   `json:"is_secret"`
	Ordinal  int    `json:"ordinal"`
	// Value carries the cleartext of a NON-SECRET field only. Non-secret
	// fields are stored unencrypted on purpose — `region`, `account_id`,
	// `host` are identifiers, not secrets, exactly like credentials.username
	// — so echoing them costs nothing and lets the UI search and sort without
	// a per-row AEAD decrypt.
	Value     *string `json:"value"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// Tenancy
// ---------------------------------------------------------------------------

// errCredentialNotVisible marks "there is no such credential for this caller".
// Every route turns it into a 404, never a 403: a 403 on a cross-tenant id
// confirms the id is real somewhere in the fleet, which turns a leaked id into
// a tenancy oracle.
var errCredentialNotVisible = errors.New("credential not visible to caller")

// resolveVisibleCredential confirms the credential exists in the caller's
// workspace AND is visible to their role, reusing credentialVisibilityFilter
// so the field surface can never be looser than the credential surface it
// hangs off. Soft-deleted rows are invisible, the same way they are to
// GET /credentials/{id}.
func (h *CredentialFieldHandler) resolveVisibleCredential(ctx context.Context, credID string) error {
	workspaceID := WorkspaceIDFromContext(ctx)
	visFilter, visArgs := credentialVisibilityFilter(RoleFromContext(ctx), UserFromContext(ctx))
	args := append([]any{credID, workspaceID}, visArgs...)

	var one int
	err := h.db.QueryRowContext(ctx,
		`SELECT 1 FROM credentials c WHERE c.id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL `+visFilter,
		args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return errCredentialNotVisible
	}
	return err
}

// guardCredential runs the tenancy/visibility check and writes the response on
// failure. The bool reports whether the caller may continue.
func (h *CredentialFieldHandler) guardCredential(w http.ResponseWriter, r *http.Request, credID string) bool {
	if credID == "" {
		replyError(w, http.StatusNotFound, msgCredentialNotFound)
		return false
	}
	switch err := h.resolveVisibleCredential(r.Context(), credID); {
	case err == nil:
		return true
	case errors.Is(err, errCredentialNotVisible):
		replyError(w, http.StatusNotFound, msgCredentialNotFound)
		return false
	default:
		replyInternalError(w, h.logger, "credential fields: resolve credential", err)
		return false
	}
}

// ---------------------------------------------------------------------------
// Read
// ---------------------------------------------------------------------------

// List returns the credential's fields.
// GET /api/v1/credentials/{credentialId}/fields
//
// The SELECT deliberately omits `encrypted_value`. Not "selects it and drops
// it" — omits it, so a secret's ciphertext is never in a variable this handler
// could accidentally serialise. `value` is NULL for every secret row by the
// table's CHECK constraint, so the only bytes available to return for a secret
// field are its key, its classification and its position.
func (h *CredentialFieldHandler) List(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("credentialId")
	if !h.guardCredential(w, r, credID) {
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT key, value, is_secret, ordinal, created_at, updated_at
		FROM credential_fields
		WHERE credential_id = ?
		ORDER BY ordinal ASC, key ASC`, credID)
	if err != nil {
		replyInternalError(w, h.logger, "credential fields: list", err)
		return
	}
	defer rows.Close()

	// Non-nil so a credential with no fields serialises as [] rather than
	// null — a JS client iterating the result would throw on null, and this is
	// the shape EVERY credential that exists today has.
	out := []credentialFieldResponse{}
	for rows.Next() {
		var f credentialFieldResponse
		var value sql.NullString
		var isSecret int
		if err := rows.Scan(&f.Key, &value, &isSecret, &f.Ordinal, &f.CreatedAt, &f.UpdatedAt); err != nil {
			replyInternalError(w, h.logger, "credential fields: scan", err)
			return
		}
		f.IsSecret = isSecret != 0
		if !f.IsSecret && value.Valid {
			v := value.String
			f.Value = &v
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "credential fields: iterate", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// loadField re-reads one field through the same value-omitting SELECT the list
// path uses, so the 201/200 echo cannot be a different, laxer shape than the
// list. Returns (nil, nil) when the field is gone.
func (h *CredentialFieldHandler) loadField(ctx context.Context, credID, key string) (*credentialFieldResponse, error) {
	var f credentialFieldResponse
	var value sql.NullString
	var isSecret int
	err := h.db.QueryRowContext(ctx, `
		SELECT key, value, is_secret, ordinal, created_at, updated_at
		FROM credential_fields WHERE credential_id = ? AND key = ?`, credID, key).
		Scan(&f.Key, &value, &isSecret, &f.Ordinal, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f.IsSecret = isSecret != 0
	if !f.IsSecret && value.Valid {
		v := value.String
		f.Value = &v
	}
	return &f, nil
}

// ---------------------------------------------------------------------------
// Write
// ---------------------------------------------------------------------------

// storageColumns turns (value, isSecret) into the (cleartext, ciphertext) pair
// the table's CHECK constraint demands: exactly one non-NULL. Encryption
// failures surface to the caller rather than being swallowed — a field the
// server could not encrypt must not be written in any other form.
func storageColumns(value string, isSecret bool) (cleartext, ciphertext any, err error) {
	if !isSecret {
		return value, nil, nil
	}
	enc, err := encryption.Encrypt(value)
	if err != nil {
		return nil, nil, err
	}
	return nil, enc, nil
}

// Create adds one field.
// POST /api/v1/credentials/{credentialId}/fields
//
// Create rather than upsert, so a duplicate key is a 409 the caller can act on
// instead of a silent overwrite of somebody else's value.
func (h *CredentialFieldHandler) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	credID := r.PathValue("credentialId")
	if !h.guardCredential(w, r, credID) {
		return
	}

	var req credentialFieldRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	key := strings.TrimSpace(req.Key)
	if msg := validateCredentialFieldKey(key); msg != "" {
		replyError(w, http.StatusBadRequest, msg)
		return
	}
	if msg := validateCredentialFieldValue(req.Value); msg != "" {
		replyError(w, http.StatusBadRequest, msg)
		return
	}
	isSecret := true
	if req.IsSecret != nil {
		isSecret = *req.IsSecret
	}

	// Count first so the cap is reported as a 400 naming the limit rather than
	// as a constraint violation. The UNIQUE index below is still the authority
	// on duplicates — this count is advisory and racy by nature, which is
	// acceptable for a bound whose only job is to stop runaway growth.
	var n int
	if err := h.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM credential_fields WHERE credential_id = ?`, credID).Scan(&n); err != nil {
		replyInternalError(w, h.logger, "credential fields: count", err)
		return
	}
	if n >= maxCredentialFields {
		replyError(w, http.StatusBadRequest,
			"a credential may carry at most 32 custom fields")
		return
	}

	// Default ordinal appends. Explicit ordinals are honoured so a client can
	// reorder without deleting and recreating (which for a secret field would
	// mean the operator has to re-enter the value).
	ordinal := n
	if req.Ordinal != nil {
		ordinal = *req.Ordinal
	}

	cleartext, ciphertext, err := storageColumns(req.Value, isSecret)
	if err != nil {
		replyInternalError(w, h.logger, "credential fields: encrypt", err)
		return
	}

	_, err = h.db.ExecContext(ctx, `
		INSERT INTO credential_fields (credential_id, key, value, encrypted_value, is_secret, ordinal)
		VALUES (?, ?, ?, ?, ?, ?)`,
		credID, key, cleartext, ciphertext, boolToInt(isSecret), ordinal)
	if err != nil {
		// The composite primary key is what actually rejects a duplicate — an
		// application-level "does it exist?" check would let two concurrent
		// POSTs both pass before either inserts.
		if isUniqueConstraintErr(err) {
			replyError(w, http.StatusConflict,
				"a field named \""+key+"\" already exists on this credential; update it instead")
			return
		}
		replyInternalError(w, h.logger, "credential fields: insert", err)
		return
	}

	h.replyWithField(w, ctx, credID, key, http.StatusCreated)
}

// Update replaces one field's value, secrecy and position.
// PUT /api/v1/credentials/{credentialId}/fields/{fieldKey}
//
// The key comes from the path and is never taken from the body, so a request
// cannot rename a field as a side effect of updating it — a rename that
// silently dropped the old key would leave the delivery path looking for a
// name that no longer exists.
func (h *CredentialFieldHandler) Update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	credID := r.PathValue("credentialId")
	if !h.guardCredential(w, r, credID) {
		return
	}
	key := strings.TrimSpace(r.PathValue("fieldKey"))
	if msg := validateCredentialFieldKey(key); msg != "" {
		replyError(w, http.StatusBadRequest, msg)
		return
	}

	var req credentialFieldRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if msg := validateCredentialFieldValue(req.Value); msg != "" {
		replyError(w, http.StatusBadRequest, msg)
		return
	}

	// An omitted is_secret on update keeps the field's current classification
	// rather than silently re-defaulting to secret: a client PUTting a new
	// region should not turn it into an encrypted blob it can no longer read.
	existing, err := h.loadField(ctx, credID, key)
	if err != nil {
		replyInternalError(w, h.logger, "credential fields: load", err)
		return
	}
	if existing == nil {
		replyError(w, http.StatusNotFound, "Credential field not found")
		return
	}
	isSecret := existing.IsSecret
	if req.IsSecret != nil {
		isSecret = *req.IsSecret
	}
	ordinal := existing.Ordinal
	if req.Ordinal != nil {
		ordinal = *req.Ordinal
	}

	cleartext, ciphertext, err := storageColumns(req.Value, isSecret)
	if err != nil {
		replyInternalError(w, h.logger, "credential fields: encrypt", err)
		return
	}

	// Both columns are written every time, so flipping is_secret MOVES the
	// value instead of leaving the old copy behind. A stale cleartext row for
	// a now-secret field would be a plaintext secret the read path happily
	// returns — the exact failure this assignment exists to prevent.
	res, err := h.db.ExecContext(ctx, `
		UPDATE credential_fields
		SET value = ?, encrypted_value = ?, is_secret = ?, ordinal = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE credential_id = ? AND key = ?`,
		cleartext, ciphertext, boolToInt(isSecret), ordinal, credID, key)
	if err != nil {
		replyInternalError(w, h.logger, "credential fields: update", err)
		return
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		replyError(w, http.StatusNotFound, "Credential field not found")
		return
	}

	h.replyWithField(w, ctx, credID, key, http.StatusOK)
}

// Delete removes one field.
// DELETE /api/v1/credentials/{credentialId}/fields/{fieldKey}
//
// A missing key is a 404, not a silent success: an operator retrying a revoke
// needs to know whether the part was ever there.
func (h *CredentialFieldHandler) Delete(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("credentialId")
	if !h.guardCredential(w, r, credID) {
		return
	}
	key := strings.TrimSpace(r.PathValue("fieldKey"))

	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM credential_fields WHERE credential_id = ? AND key = ?`, credID, key)
	if err != nil {
		replyInternalError(w, h.logger, "credential fields: delete", err)
		return
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		replyError(w, http.StatusNotFound, "Credential field not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// replyWithField re-reads the row and writes it as the response. Going back to
// the database rather than echoing the request means the write paths return
// the same shape the list path does — including, and especially, the omission
// of a secret's value.
func (h *CredentialFieldHandler) replyWithField(w http.ResponseWriter, ctx context.Context, credID, key string, status int) {
	f, err := h.loadField(ctx, credID, key)
	if err != nil {
		replyInternalError(w, h.logger, "credential fields: reload", err)
		return
	}
	if f == nil {
		// The row was written and is already gone — a concurrent delete. Report
		// the write as done rather than inventing a body.
		writeJSON(w, status, map[string]bool{"success": true})
		return
	}
	writeJSON(w, status, f)
}

// boolToInt renders a Go bool for SQLite's INTEGER-backed booleans.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
