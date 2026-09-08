package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/providerlogin"
	"github.com/crewship-ai/crewship/internal/providerpool"
)

// ProviderPoolHandler manages definitions only. These routes never authorize
// delivery, reserve a run's account, decrypt a token, or contact a provider.
type ProviderPoolHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewProviderPoolHandler(db *sql.DB, logger *slog.Logger) *ProviderPoolHandler {
	return &ProviderPoolHandler{db: db, logger: logger}
}

type providerPoolMemberView struct {
	CredentialID string `json:"credential_id"`
	Priority     int    `json:"priority"`
}

// Explicit wire DTOs exclude internal generation fingerprints and ciphertext.
type providerPoolView struct {
	ID              string                   `json:"id"`
	Name            string                   `json:"name"`
	Provider        string                   `json:"provider"`
	Mode            string                   `json:"mode"`
	AllowCrossOwner bool                     `json:"allow_cross_owner"`
	CreatedBy       *string                  `json:"created_by"`
	MemberCount     int                      `json:"member_count"`
	Members         []providerPoolMemberView `json:"members,omitempty"`
}

func providerPoolWorkspace(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !requireRole(w, r, "manage") {
		return "", false
	}
	ws := WorkspaceIDFromContext(r.Context())
	if ws == "" {
		replyError(w, 400, "Workspace context required")
		return "", false
	}
	return ws, true
}

// Create records an administrative account set, not a binding or grant.
func (h *ProviderPoolHandler) Create(w http.ResponseWriter, r *http.Request) {
	ws, ok := providerPoolWorkspace(w, r)
	if !ok {
		return
	}
	user := UserFromContext(r.Context())
	if user == nil || user.ID == "" {
		replyError(w, 401, "Authentication required")
		return
	}
	var body struct {
		Name            string                   `json:"name"`
		Provider        string                   `json:"provider"`
		Mode            string                   `json:"mode"`
		AllowCrossOwner bool                     `json:"allow_cross_owner"`
		Members         []providerPoolMemberView `json:"members"`
	}
	// A pool accepts IDs, not auth material or binding instructions. Reject
	// unknown fields rather than acknowledging an option we silently ignore.
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		replyError(w, 400, "Invalid JSON body")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		replyError(w, 400, "Expected one JSON object")
		return
	}
	pool := providerpool.Pool{ID: generateCUID(), WorkspaceID: ws, Name: body.Name, CreatedBy: user.ID,
		Policy: providerpool.Policy{Provider: body.Provider, Mode: body.Mode, AllowCrossOwner: body.AllowCrossOwner}}
	for _, m := range body.Members {
		pool.Members = append(pool.Members, providerpool.Member{CredentialID: m.CredentialID, Priority: m.Priority})
	}
	if err := providerpool.NewStore(h.db).Create(r.Context(), pool); err != nil {
		switch {
		case errors.Is(err, providerpool.ErrConflict):
			replyError(w, 409, "A provider pool with this name already exists")
		case errors.Is(err, providerpool.ErrCrossOwner):
			replyError(w, 400, "Pooling accounts from different owners requires explicit consent")
		case errors.Is(err, providerpool.ErrInvalid):
			replyError(w, 400, "Invalid provider pool or member")
		default:
			replyInternalError(w, h.logger, "create provider pool", err)
		}
		return
	}
	// Same best-effort settings audit as other administrative definitions.
	// No submitted account material or names are copied into the audit log.
	auditFromRequest(r, h.db, "provider_pool.create", "PROVIDER_POOL", pool.ID, map[string]interface{}{"allow_cross_owner": body.AllowCrossOwner, "member_count": len(body.Members)})
	writeJSON(w, 201, providerPoolView{ID: pool.ID, Name: strings.TrimSpace(body.Name), Provider: providerlogin.Canonical(body.Provider), Mode: body.Mode, AllowCrossOwner: body.AllowCrossOwner, CreatedBy: &user.ID, MemberCount: len(body.Members), Members: body.Members})
}

// List is a bounded, keyset-paginated metadata read; it never consumes a turn.
func (h *ProviderPoolHandler) List(w http.ResponseWriter, r *http.Request) {
	ws, ok := providerPoolWorkspace(w, r)
	if !ok {
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT p.id,p.name,p.provider,p.mode,p.allow_cross_owner,p.created_by,
		(SELECT COUNT(*) FROM provider_login_pool_members m WHERE m.pool_id=p.id)
		FROM provider_login_pools p WHERE p.workspace_id=? AND p.id>? ORDER BY p.id LIMIT 101`, ws, r.URL.Query().Get("after"))
	if err != nil {
		replyInternalError(w, h.logger, "list provider pools", err)
		return
	}
	defer rows.Close()
	items := make([]providerPoolView, 0)
	for rows.Next() {
		var item providerPoolView
		if err := rows.Scan(&item.ID, &item.Name, &item.Provider, &item.Mode, &item.AllowCrossOwner, &item.CreatedBy, &item.MemberCount); err != nil {
			replyInternalError(w, h.logger, "scan provider pool", err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "iterate provider pools", err)
		return
	}
	var next *string
	if len(items) > 100 {
		items = items[:100]
		next = &items[99].ID
	}
	writeJSON(w, 200, struct {
		Items      []providerPoolView `json:"items"`
		NextCursor *string            `json:"next_cursor"`
	}{items, next})
}

// Get reads one definition in a consistent snapshot, including revoked members
// so an administrator can diagnose the set. It is not an availability probe.
func (h *ProviderPoolHandler) Get(w http.ResponseWriter, r *http.Request) {
	ws, ok := providerPoolWorkspace(w, r)
	if !ok {
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		replyInternalError(w, h.logger, "begin provider pool read", err)
		return
	}
	defer tx.Rollback()
	var item providerPoolView
	err = tx.QueryRowContext(r.Context(), `SELECT id,name,provider,mode,allow_cross_owner,created_by FROM provider_login_pools WHERE workspace_id=? AND id=?`, ws, r.PathValue("poolId")).Scan(&item.ID, &item.Name, &item.Provider, &item.Mode, &item.AllowCrossOwner, &item.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "Provider pool not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "get provider pool", err)
		return
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT m.credential_id,m.priority,c.workspace_id FROM provider_login_pool_members m JOIN credentials c ON c.id=m.credential_id WHERE m.pool_id=? ORDER BY m.priority,m.credential_id`, item.ID)
	if err != nil {
		replyInternalError(w, h.logger, "read provider pool members", err)
		return
	}
	defer rows.Close()
	item.Members = make([]providerPoolMemberView, 0)
	for rows.Next() {
		var member providerPoolMemberView
		var memberWorkspace string
		if err := rows.Scan(&member.CredentialID, &member.Priority, &memberWorkspace); err != nil {
			replyInternalError(w, h.logger, "scan provider pool member", err)
			return
		}
		if memberWorkspace != ws {
			replyInternalError(w, h.logger, "provider pool tenant mismatch", providerpool.ErrInvalid)
			return
		}
		item.Members = append(item.Members, member)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "iterate provider pool members", err)
		return
	}
	if err := rows.Close(); err != nil {
		replyInternalError(w, h.logger, "close provider pool members", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit provider pool read", err)
		return
	}
	item.MemberCount = len(item.Members)
	writeJSON(w, 200, item)
}
