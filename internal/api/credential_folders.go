package api

// Credential folders (Harbor 2 Vault, #3479). A folder groups credentials and
// carries the policy the Vault draws on its detail:
//
//   - reveal_policy   inherit | admins | nobody. Only ever TIGHTENS the
//     workspace reveal gate (credentials_reveal.go): "admins" limits reveal to
//     OWNER/ADMIN, "nobody" seals the whole folder. It cannot grant a reveal the
//     workspace gate would refuse.
//   - min_security_level / default_security_level  the lowest Keeper tier a
//     member may hold, and the tier a new member gets when none is given.
//   - remind_after_days  replace-reminder; 0 = off. Surfaced on each member as
//     replace_due (see credentialReplaceDue) — nothing is rotated automatically.
//
// Two levels at most. Policy fields are an access decision, so setting or
// changing them needs the manage tier (OWNER/ADMIN); organising (name,
// description, parent, reminder) needs the update tier (MANAGER+). Deleting
// needs manage and only an empty folder is deleted — a credential is never
// silently unfiled. Folder grants (Can use / Can manage for people and crews)
// are NOT here: sharing still flows through credential crew assignment.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	folderRevealInherit = "inherit"
	folderRevealAdmins  = "admins"
	folderRevealNobody  = "nobody"

	credentialFolderMaxNameRunes = 120
	credentialFolderMaxDescRunes = 500
	credentialFolderMaxRemind    = 3650
)

func validFolderRevealPolicy(p string) bool {
	return p == folderRevealInherit || p == folderRevealAdmins || p == folderRevealNobody
}

// CredentialFolderHandler serves /api/v1/credential-folders.
type CredentialFolderHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewCredentialFolderHandler(db *sql.DB, logger *slog.Logger) *CredentialFolderHandler {
	return &CredentialFolderHandler{db: db, logger: logger}
}

type credentialFolderResponse struct {
	ID                   string  `json:"id"`
	ParentID             *string `json:"parent_id"`
	Name                 string  `json:"name"`
	Description          string  `json:"description"`
	RevealPolicy         string  `json:"reveal_policy"`
	RemindAfterDays      int     `json:"remind_after_days"`
	DefaultSecurityLevel *int    `json:"default_security_level"`
	MinSecurityLevel     int     `json:"min_security_level"`
	// CredentialCount counts the credentials filed directly in this folder that
	// the caller can see; the same visibility filter as GET /credentials.
	CredentialCount int    `json:"credential_count"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

const credentialFolderCols = `id, parent_id, name, description, reveal_policy, remind_after_days,
	default_security_level, min_security_level, created_at, updated_at`

func scanCredentialFolder(sc interface{ Scan(...any) error }) (credentialFolderResponse, error) {
	var f credentialFolderResponse
	var def sql.NullInt64
	err := sc.Scan(&f.ID, &f.ParentID, &f.Name, &f.Description, &f.RevealPolicy, &f.RemindAfterDays,
		&def, &f.MinSecurityLevel, &f.CreatedAt, &f.UpdatedAt)
	if def.Valid {
		v := int(def.Int64)
		f.DefaultSecurityLevel = &v
	}
	return f, err
}

// List GET /api/v1/credential-folders. Folder names are not secret, so every
// workspace member gets the tree; the counts follow credential visibility.
func (h *CredentialFolderHandler) List(w http.ResponseWriter, r *http.Request) {
	ws := WorkspaceIDFromContext(r.Context())
	vis, visArgs := credentialVisibilityFilter(RoleFromContext(r.Context()), UserFromContext(r.Context()))
	rows, err := h.db.QueryContext(r.Context(), `SELECT `+credentialFolderCols+`,
		(SELECT COUNT(*) FROM credentials c WHERE c.folder_id = credential_folders.id AND c.workspace_id = credential_folders.workspace_id AND c.deleted_at IS NULL `+vis+`)
		FROM credential_folders WHERE workspace_id = ? ORDER BY lower(name), id`,
		append(append([]any{}, visArgs...), ws)...)
	if err != nil {
		replyInternalError(w, h.logger, "list credential folders", err)
		return
	}
	defer rows.Close()
	out := []credentialFolderResponse{}
	for rows.Next() {
		var f credentialFolderResponse
		var def sql.NullInt64
		if err := rows.Scan(&f.ID, &f.ParentID, &f.Name, &f.Description, &f.RevealPolicy, &f.RemindAfterDays,
			&def, &f.MinSecurityLevel, &f.CreatedAt, &f.UpdatedAt, &f.CredentialCount); err != nil {
			replyInternalError(w, h.logger, "scan credential folder", err)
			return
		}
		if def.Valid {
			v := int(def.Int64)
			f.DefaultSecurityLevel = &v
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "list credential folders", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type folderBody struct {
	Name                 *string `json:"name"`
	Description          *string `json:"description"`
	RevealPolicy         *string `json:"reveal_policy"`
	RemindAfterDays      *int    `json:"remind_after_days"`
	DefaultSecurityLevel *int    `json:"default_security_level"`
	MinSecurityLevel     *int    `json:"min_security_level"`
}

// parentID needs to tell "absent" from "null" (promote to root).
type folderParent struct {
	set bool
	id  *string
}

func (p *folderParent) UnmarshalJSON(b []byte) error {
	p.set = true
	if string(b) == "null" {
		p.id = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	p.id = &s
	return nil
}

func validateFolderFields(b folderBody, minLevel int, defLevel *int) string {
	if b.Name != nil {
		n := strings.TrimSpace(*b.Name)
		if n == "" {
			return "name is required"
		}
		if utf8.RuneCountInString(n) > credentialFolderMaxNameRunes {
			return fmt.Sprintf("name must be at most %d characters", credentialFolderMaxNameRunes)
		}
	}
	if b.Description != nil && utf8.RuneCountInString(*b.Description) > credentialFolderMaxDescRunes {
		return fmt.Sprintf("description must be at most %d characters", credentialFolderMaxDescRunes)
	}
	if b.RevealPolicy != nil && !validFolderRevealPolicy(*b.RevealPolicy) {
		return "reveal_policy must be inherit, admins or nobody"
	}
	if b.RemindAfterDays != nil && (*b.RemindAfterDays < 0 || *b.RemindAfterDays > credentialFolderMaxRemind) {
		return fmt.Sprintf("remind_after_days must be between 0 and %d", credentialFolderMaxRemind)
	}
	if minLevel < 1 || minLevel > 4 {
		return "min_security_level must be between 1 and 4"
	}
	if defLevel != nil && (*defLevel < 1 || *defLevel > 4) {
		return "default_security_level must be between 1 and 4"
	}
	if defLevel != nil && *defLevel < minLevel {
		return "default_security_level cannot be below min_security_level"
	}
	return ""
}

// touchesPolicy reports whether the body changes an access-policy field.
func (b folderBody) touchesPolicy() bool {
	return b.RevealPolicy != nil || b.MinSecurityLevel != nil || b.DefaultSecurityLevel != nil
}

// parentDepthOK checks the parent exists in the workspace and is itself a
// root folder (two levels at most).
func (h *CredentialFolderHandler) checkParent(r *http.Request, ws, parentID, selfID string) (int, string) {
	if parentID == selfID && selfID != "" {
		return http.StatusBadRequest, "a folder cannot be its own parent"
	}
	var grand sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		`SELECT parent_id FROM credential_folders WHERE id = ? AND workspace_id = ?`, parentID, ws).Scan(&grand)
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusBadRequest, "Invalid parent_id"
	}
	if err != nil {
		return http.StatusInternalServerError, ""
	}
	if grand.Valid {
		return http.StatusBadRequest, "folders nest two levels at most"
	}
	return 0, ""
}

func folderNameConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE")
}

// Create POST /api/v1/credential-folders
func (h *CredentialFolderHandler) Create(w http.ResponseWriter, r *http.Request) {
	ws := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	var req struct {
		folderBody
		ParentID folderParent `json:"parent_id"`
	}
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Name == nil {
		replyError(w, http.StatusBadRequest, "name is required")
		return
	}
	minLevel := 1
	if req.MinSecurityLevel != nil {
		minLevel = *req.MinSecurityLevel
	}
	if msg := validateFolderFields(req.folderBody, minLevel, req.DefaultSecurityLevel); msg != "" {
		replyError(w, http.StatusBadRequest, msg)
		return
	}
	if req.touchesPolicy() && !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Reveal policy and tier limits need OWNER or ADMIN")
		return
	}
	var parent any
	if req.ParentID.set && req.ParentID.id != nil && *req.ParentID.id != "" {
		if code, msg := h.checkParent(r, ws, *req.ParentID.id, ""); code != 0 {
			if code == http.StatusInternalServerError {
				replyInternalError(w, h.logger, "check folder parent", errors.New("lookup failed"))
				return
			}
			replyError(w, code, msg)
			return
		}
		parent = *req.ParentID.id
	}
	policy := folderRevealInherit
	if req.RevealPolicy != nil {
		policy = *req.RevealPolicy
	}
	remind := 0
	if req.RemindAfterDays != nil {
		remind = *req.RemindAfterDays
	}
	desc := ""
	if req.Description != nil {
		desc = strings.TrimSpace(*req.Description)
	}
	var def any
	if req.DefaultSecurityLevel != nil {
		def = *req.DefaultSecurityLevel
	}
	userID := ""
	if u := UserFromContext(r.Context()); u != nil {
		userID = u.ID
	}
	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := h.db.ExecContext(r.Context(), `INSERT INTO credential_folders
		(id, workspace_id, parent_id, name, description, reveal_policy, remind_after_days,
		 default_security_level, min_security_level, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)`,
		id, ws, parent, strings.TrimSpace(*req.Name), desc, policy, remind, def, minLevel, userID, now, now)
	if err != nil {
		if folderNameConflict(err) {
			replyError(w, http.StatusConflict, "A folder with this name already exists here")
			return
		}
		replyInternalError(w, h.logger, "create credential folder", err)
		return
	}
	auditFromRequest(r, h.db, "credential_folder.create", "CREDENTIAL_FOLDER", id,
		map[string]interface{}{"name": strings.TrimSpace(*req.Name), "reveal_policy": policy})
	h.reply(w, r, http.StatusCreated, ws, id)
}

func (h *CredentialFolderHandler) reply(w http.ResponseWriter, r *http.Request, status int, ws, id string) {
	f, err := scanCredentialFolder(h.db.QueryRowContext(r.Context(),
		`SELECT `+credentialFolderCols+` FROM credential_folders WHERE id = ? AND workspace_id = ?`, id, ws))
	if err != nil {
		replyInternalError(w, h.logger, "load credential folder", err)
		return
	}
	writeJSON(w, status, f)
}

// Update PATCH /api/v1/credential-folders/{folderId}
func (h *CredentialFolderHandler) Update(w http.ResponseWriter, r *http.Request) {
	ws := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	id := r.PathValue("folderId")
	if !canRole(role, "update") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	cur, err := scanCredentialFolder(h.db.QueryRowContext(r.Context(),
		`SELECT `+credentialFolderCols+` FROM credential_folders WHERE id = ? AND workspace_id = ?`, id, ws))
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Folder not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "load credential folder", err)
		return
	}
	var req struct {
		folderBody
		ParentID folderParent `json:"parent_id"`
	}
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	// The tier floor and default are validated against the values the row will
	// hold, not just the patch.
	minLevel := cur.MinSecurityLevel
	if req.MinSecurityLevel != nil {
		minLevel = *req.MinSecurityLevel
	}
	defLevel := cur.DefaultSecurityLevel
	if req.DefaultSecurityLevel != nil {
		defLevel = req.DefaultSecurityLevel
	}
	if msg := validateFolderFields(req.folderBody, minLevel, defLevel); msg != "" {
		replyError(w, http.StatusBadRequest, msg)
		return
	}
	if req.touchesPolicy() && !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Reveal policy and tier limits need OWNER or ADMIN")
		return
	}
	ub := newUpdate()
	if req.Name != nil {
		ub.Set("name", strings.TrimSpace(*req.Name))
	}
	if req.Description != nil {
		ub.Set("description", strings.TrimSpace(*req.Description))
	}
	if req.RevealPolicy != nil {
		ub.Set("reveal_policy", *req.RevealPolicy)
	}
	if req.RemindAfterDays != nil {
		ub.Set("remind_after_days", *req.RemindAfterDays)
	}
	if req.MinSecurityLevel != nil {
		ub.Set("min_security_level", *req.MinSecurityLevel)
	}
	if req.DefaultSecurityLevel != nil {
		ub.Set("default_security_level", *req.DefaultSecurityLevel)
	}
	if req.ParentID.set {
		if req.ParentID.id == nil || *req.ParentID.id == "" {
			ub.SetNull("parent_id")
		} else {
			if code, msg := h.checkParent(r, ws, *req.ParentID.id, id); code != 0 {
				if code == http.StatusInternalServerError {
					replyInternalError(w, h.logger, "check folder parent", errors.New("lookup failed"))
					return
				}
				replyError(w, code, msg)
				return
			}
			// A folder that already has children cannot become a child itself,
			// and a folder cannot move under its own child (that child is not
			// a root, so checkParent has already refused it).
			var kids int
			if err := h.db.QueryRowContext(r.Context(),
				`SELECT COUNT(*) FROM credential_folders WHERE parent_id = ?`, id).Scan(&kids); err != nil {
				replyInternalError(w, h.logger, "count subfolders", err)
				return
			}
			if kids > 0 {
				replyError(w, http.StatusBadRequest, "folders nest two levels at most")
				return
			}
			ub.Set("parent_id", *req.ParentID.id)
		}
	}
	if ub.Empty() {
		replyError(w, http.StatusBadRequest, "No fields to update")
		return
	}
	q, args := ub.Build("credential_folders", "id = ? AND workspace_id = ?", id, ws)
	if _, err := h.db.ExecContext(r.Context(), q, args...); err != nil {
		if folderNameConflict(err) {
			replyError(w, http.StatusConflict, "A folder with this name already exists here")
			return
		}
		replyInternalError(w, h.logger, "update credential folder", err)
		return
	}
	auditFromRequest(r, h.db, "credential_folder.update", "CREDENTIAL_FOLDER", id, map[string]interface{}{
		"policy_changed": req.touchesPolicy(),
	})
	h.reply(w, r, http.StatusOK, ws, id)
}

// Delete DELETE /api/v1/credential-folders/{folderId}
func (h *CredentialFolderHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ws := WorkspaceIDFromContext(r.Context())
	id := r.PathValue("folderId")
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	var exists int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT 1 FROM credential_folders WHERE id = ? AND workspace_id = ?`, id, ws).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "Folder not found")
			return
		}
		replyInternalError(w, h.logger, "load credential folder", err)
		return
	}
	// Counts include soft-deleted credentials on purpose: the row still
	// references the folder, and the foreign key would refuse the delete.
	var creds, kids int
	if err := h.db.QueryRowContext(r.Context(), `SELECT
		(SELECT COUNT(*) FROM credentials WHERE folder_id = ?),
		(SELECT COUNT(*) FROM credential_folders WHERE parent_id = ?)`, id, id).Scan(&creds, &kids); err != nil {
		replyInternalError(w, h.logger, "count folder members", err)
		return
	}
	if creds > 0 || kids > 0 {
		replyError(w, http.StatusConflict, fmt.Sprintf(
			"Folder is not empty (%d secrets, %d subfolders). Move them out first.", creds, kids))
		return
	}
	if _, err := h.db.ExecContext(r.Context(),
		`DELETE FROM credential_folders WHERE id = ? AND workspace_id = ?`, id, ws); err != nil {
		replyInternalError(w, h.logger, "delete credential folder", err)
		return
	}
	auditFromRequest(r, h.db, "credential_folder.delete", "CREDENTIAL_FOLDER", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// ── Membership: folder policy as the credential paths see it ────────────────

// folderPolicy is the part of a folder the credential write and reveal paths
// need.
type folderPolicy struct {
	id           string
	revealPolicy string
	minLevel     int
	defLevel     *int
}

// restricted reports whether filing a credential in (or taking it out of) this
// folder changes who can reveal it or how tightly Keeper gates it, which makes
// the move an access decision rather than tidying.
func (p *folderPolicy) restricted() bool {
	return p != nil && (p.revealPolicy != folderRevealInherit || p.minLevel > 1)
}

// folderEffectiveRevealSQL is the reveal policy a credential in folder alias f
// (child) / p (parent) is held to: the stricter of the two, so a subfolder can
// never be looser than the folder it sits in. nobody > admins > inherit.
const folderEffectiveRevealSQL = `CASE
	WHEN f.reveal_policy = 'nobody' OR COALESCE(p.reveal_policy, 'inherit') = 'nobody' THEN 'nobody'
	WHEN f.reveal_policy = 'admins' OR COALESCE(p.reveal_policy, 'inherit') = 'admins' THEN 'admins'
	ELSE 'inherit' END`

// loadFolderPolicy returns the EFFECTIVE policy of a folder (its own tightened
// by its parent's): the stricter reveal policy, the higher tier floor, and the
// folder's default tier or else the parent's. (nil, nil) when the folder does
// not exist in this workspace; a folder of another workspace is
// indistinguishable from none.
func loadFolderPolicy(ctx context.Context, db rowQuerier, ws, id string) (*folderPolicy, error) {
	var p folderPolicy
	var def sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT f.id, `+folderEffectiveRevealSQL+`,
			MAX(f.min_security_level, COALESCE(p.min_security_level, 1)),
			COALESCE(f.default_security_level, p.default_security_level)
		FROM credential_folders f LEFT JOIN credential_folders p ON p.id = f.parent_id
		WHERE f.id = ? AND f.workspace_id = ?`, id, ws).Scan(&p.id, &p.revealPolicy, &p.minLevel, &def)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if def.Valid {
		v := int(def.Int64)
		if v < p.minLevel {
			v = p.minLevel
		}
		p.defLevel = &v
	}
	return &p, nil
}

const msgFolderPolicyNeedsManage = "Filing a secret in or out of a folder with a reveal policy or tier floor needs OWNER or ADMIN"

// folderInfo is what the list/get responses add per credential.
type folderInfo struct {
	folderID     *string
	replaceAfter *time.Time
}

var folderTimeLayouts = []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z"}

func parseFolderTime(s string) (time.Time, bool) {
	for _, l := range folderTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// loadFolderInfoBatch resolves folder_id and the replace-reminder moment for
// many credentials in one query. The clock starts at the later of creation and
// the last ROTATE event, so rotating (or replacing the value in place, which
// records the same event) resets it.
func (h *CredentialHandler) loadFolderInfoBatch(ctx context.Context, ids []string) map[string]folderInfo {
	out := make(map[string]folderInfo, len(ids))
	if len(ids) == 0 {
		return out
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := h.db.QueryContext(ctx, `SELECT c.id, c.folder_id, COALESCE(f.remind_after_days, 0), c.created_at,
		(SELECT MAX(a.occurred_at) FROM credential_audit a WHERE a.credential_id = c.id AND a.event_type = 'ROTATE')
		FROM credentials c LEFT JOIN credential_folders f ON f.id = c.folder_id
		WHERE c.id IN (`+sqlPlaceholders(len(ids))+`)`, args...)
	if err != nil {
		h.logger.Error("loadFolderInfoBatch: query failed", "error", err, "n", len(ids))
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var folder, rotated sql.NullString
		var days int
		var created string
		if err := rows.Scan(&id, &folder, &days, &created, &rotated); err != nil {
			h.logger.Error("loadFolderInfoBatch: scan failed", "error", err)
			continue
		}
		fi := folderInfo{}
		if folder.Valid {
			v := folder.String
			fi.folderID = &v
		}
		if days > 0 {
			base, ok := parseFolderTime(created)
			if rotated.Valid {
				if t, ok2 := parseFolderTime(rotated.String); ok2 && (!ok || t.After(base)) {
					base, ok = t, true
				}
			}
			if ok {
				due := base.AddDate(0, 0, days)
				fi.replaceAfter = &due
			}
		}
		out[id] = fi
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("loadFolderInfoBatch: iterate", "error", err)
	}
	return out
}

func applyFolderInfo(c *credentialResponse, fi folderInfo, now time.Time) {
	c.FolderID = fi.folderID
	c.ReplaceDue = false
	c.ReplaceAfter = nil
	if fi.replaceAfter != nil {
		s := fi.replaceAfter.Format(time.RFC3339)
		c.ReplaceAfter = &s
		c.ReplaceDue = !fi.replaceAfter.After(now)
	}
}
