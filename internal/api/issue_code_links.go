package api

// Link-first Git integration — an issue carries the pull requests / merge
// requests that resolve it.
//
// The model, and why it is this one: a user (or an agent) pastes a PR/MR URL
// onto an issue; Crewship recognises the provider from the URL's path grammar,
// fetches the object through that provider's REST API with a credential
// already in the vault, and stores what it said. That works identically for
// GitHub and GitLab, needs no webhook and no publicly reachable instance —
// which matters, because Crewship is self-hosted and most instances are not
// addressable from github.com.
//
// Explicitly NOT here (phase 2, and the schema is shaped so they need no
// migration rewrite): transitioning issue status when a PR merges, `Fixes
// ENG-123` magic words in PR bodies, and branch-name generation.
//
// Routes (all under the crew/issue path, so they inherit the same
// `crews:write` route scope as the sibling comments/relations sub-resources):
//
//	GET    /api/v1/crews/{crewId}/issues/{identifier}/code-links
//	POST   /api/v1/crews/{crewId}/issues/{identifier}/code-links
//	POST   /api/v1/crews/{crewId}/issues/{identifier}/code-links/{linkId}/refresh
//	DELETE /api/v1/crews/{crewId}/issues/{identifier}/code-links/{linkId}

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/gitlink"
	"github.com/crewship-ai/crewship/internal/ws"
)

// SettingAllowPrivateGitHosts is the instance-level opt-in that lets a code
// link point at a forge on a private/loopback address.
//
// Default OFF. The URL being fetched is user-supplied by construction, and the
// server attaches a credential to it, so the default has to be the strict one:
// without this setting a pasted link can never be turned into a probe of the
// network Crewship runs on. An operator running a self-hosted GitLab on an
// intranet address turns it on knowingly. Even then the hard tier does not
// move — cloud metadata (169.254.169.254 and friends) stays unreachable. See
// gitlink.NewClient.
const SettingAllowPrivateGitHosts = "git_links.allow_private_hosts"

// codeLinkProblemBase namespaces the RFC 7807 `type` URIs below. Status codes
// collide (three different failures are all "the upstream said no"), so the
// type URI plus the `code` member — not the status — is what a client keys off
// to tell them apart.
const codeLinkProblemBase = "https://crewship.ai/problems/code-link/"

// CodeLinkHandler serves the issue↔pull-request link surface.
type CodeLinkHandler struct {
	db     *sql.DB
	hub    *ws.Hub
	logger *slog.Logger
}

// NewCodeLinkHandler wires the handler.
func NewCodeLinkHandler(db *sql.DB, hub *ws.Hub, logger *slog.Logger) *CodeLinkHandler {
	return &CodeLinkHandler{db: db, hub: hub, logger: logger}
}

// codeLinkResponse is the wire shape of one link.
//
// Title and Author are external, attacker-choosable strings: anyone able to
// open a pull request against a linked repository picks them. They are
// returned raw here because this endpoint feeds a browser, which escapes them;
// the agent-facing read path fences them instead (see issues_internal.go).
type codeLinkResponse struct {
	ID          string `json:"id"`
	MissionID   string `json:"mission_id"`
	WorkspaceID string `json:"workspace_id"`
	Provider    string `json:"provider"`
	Host        string `json:"host"`
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	Number      int    `json:"number"`
	Kind        string `json:"kind"`
	URL         string `json:"url"`

	Title        *string `json:"title"`
	State        *string `json:"state"`
	Author       *string `json:"author"`
	SourceBranch *string `json:"source_branch"`
	TargetBranch *string `json:"target_branch"`

	RemoteCreatedAt *string `json:"remote_created_at"`
	RemoteUpdatedAt *string `json:"remote_updated_at"`
	RemoteMergedAt  *string `json:"remote_merged_at"`
	RemoteClosedAt  *string `json:"remote_closed_at"`

	CredentialID  *string `json:"credential_id"`
	LastSyncedAt  *string `json:"last_synced_at"`
	LastSyncError *string `json:"last_sync_error"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

const codeLinkColumns = `
	SELECT id, mission_id, workspace_id, provider, host, owner, repo, number, kind, url,
	       title, state, author, source_branch, target_branch,
	       remote_created_at, remote_updated_at, remote_merged_at, remote_closed_at,
	       credential_id, last_synced_at, last_sync_error, created_at, updated_at
	FROM mission_code_links`

func scanCodeLink(s interface{ Scan(...interface{}) error }) (codeLinkResponse, error) {
	var l codeLinkResponse
	err := s.Scan(
		&l.ID, &l.MissionID, &l.WorkspaceID, &l.Provider, &l.Host, &l.Owner, &l.Repo,
		&l.Number, &l.Kind, &l.URL,
		&l.Title, &l.State, &l.Author, &l.SourceBranch, &l.TargetBranch,
		&l.RemoteCreatedAt, &l.RemoteUpdatedAt, &l.RemoteMergedAt, &l.RemoteClosedAt,
		&l.CredentialID, &l.LastSyncedAt, &l.LastSyncError, &l.CreatedAt, &l.UpdatedAt,
	)
	return l, err
}

// ── List — GET /api/v1/crews/{crewId}/issues/{identifier}/code-links ─────

// List returns every code link on an issue, newest first.
func (h *CodeLinkHandler) List(w http.ResponseWriter, r *http.Request) {
	missionID, wsID, ok := h.resolveIssue(w, r)
	if !ok {
		return
	}

	rows, err := h.db.QueryContext(r.Context(),
		codeLinkColumns+` WHERE mission_id = ? AND workspace_id = ? ORDER BY created_at DESC, id DESC`,
		missionID, wsID)
	if err != nil {
		internalError(w, r, h.logger, "list code links", err)
		return
	}
	defer rows.Close()

	result := []codeLinkResponse{}
	for rows.Next() {
		l, err := scanCodeLink(rows)
		if err != nil {
			internalError(w, r, h.logger, "scan code link", err)
			return
		}
		result = append(result, l)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, h.logger, "rows iteration (code links)", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// ── Attach — POST /api/v1/crews/{crewId}/issues/{identifier}/code-links ──

// Attach parses a pasted pull-request URL, fetches its state, and records it
// against the issue.
//
// The fetch happens BEFORE the insert on purpose. A link stored without ever
// having been reachable is a row that looks attached and answers nothing —
// the failure would surface later, as a stale card, instead of now, as an
// error naming the credential to fix.
func (h *CodeLinkHandler) Attach(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}
	missionID, wsID, ok := h.resolveIssue(w, r)
	if !ok {
		return
	}
	crewID := r.PathValue("crewId")

	var req struct {
		URL string `json:"url"`
	}
	if err := readJSON(r, &req); err != nil {
		writeCodeLinkProblem(w, r, http.StatusBadRequest, "invalid-body", "Invalid JSON body", 0)
		return
	}

	ref, err := gitlink.Parse(req.URL)
	if err != nil {
		writeCodeLinkProblem(w, r, http.StatusBadRequest, "unsupported-url", err.Error(), 0)
		return
	}

	// Reject a duplicate BEFORE spending a provider request on it. The UNIQUE
	// constraint below is still the authority (two concurrent attaches race
	// past this check), but without it every re-paste of the same link burns a
	// rate-limit unit and writes a credential USE event for a call whose result
	// is discarded.
	var dupID string
	switch err := h.db.QueryRowContext(r.Context(), `
		SELECT id FROM mission_code_links
		WHERE mission_id = ? AND provider = ? AND host = ? AND owner = ? AND repo = ? AND number = ?`,
		missionID, string(ref.Provider), ref.Host, ref.Owner, ref.Repo, ref.Number).Scan(&dupID); {
	case err == nil:
		writeCodeLinkProblem(w, r, http.StatusConflict, "already-linked",
			fmt.Sprintf("%s is already linked to this issue", ref.URL), 0)
		return
	case errors.Is(err, sql.ErrNoRows):
		// The normal path.
	default:
		internalError(w, r, h.logger, "check duplicate code link", err)
		return
	}

	cred, err := resolveCodeLinkCredential(r.Context(), h.db, wsID, crewID, ref.Provider, ref.Host)
	if err != nil {
		h.replyCredentialProblem(w, r, ref, err)
		return
	}

	allowPrivate := allowPrivateGitHosts(r.Context(), h.db)
	details, err := gitlink.NewClient(allowPrivate).Fetch(r.Context(), ref, cred.token)
	if err != nil {
		writeFetchProblem(w, r, err)
		return
	}
	// The credential was actually used against the provider — record it so
	// the vault's last-used / stale-key reporting stays honest.
	recordCredentialEventBestEffort(r.Context(), h.db, h.logger, cred.id, AuditEventUse, "", clientIP(r),
		map[string]any{"purpose": "code_link_fetch", "host": ref.Host})

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	var userID string
	if u := UserFromContext(r.Context()); u != nil {
		userID = u.ID
	}

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO mission_code_links (
			id, workspace_id, mission_id, provider, host, owner, repo, number, kind, url,
			title, state, author, source_branch, target_branch,
			remote_created_at, remote_updated_at, remote_merged_at, remote_closed_at,
			credential_id, last_synced_at, last_sync_error,
			created_by_user_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?)`,
		id, wsID, missionID, string(ref.Provider), ref.Host, ref.Owner, ref.Repo, ref.Number,
		string(ref.Kind), ref.URL,
		details.Title, string(details.State), details.Author, details.SourceBranch, details.TargetBranch,
		nullIfEmpty(details.CreatedAt), nullIfEmpty(details.UpdatedAt),
		nullIfEmpty(details.MergedAt), nullIfEmpty(details.ClosedAt),
		cred.id, now,
		nullIfEmpty(userID), now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeCodeLinkProblem(w, r, http.StatusConflict, "already-linked",
				fmt.Sprintf("%s is already linked to this issue", ref.URL), 0)
			return
		}
		internalError(w, r, h.logger, "insert code link", err)
		return
	}

	h.logActivity(r.Context(), missionID, userID, "code_link_added", ref.URL)
	broadcastWorkspaceEvent(h.hub, wsID, "issue.updated", map[string]string{"id": missionID})

	link, err := h.loadOne(r.Context(), id, wsID)
	if err != nil {
		internalError(w, r, h.logger, "reload code link", err)
		return
	}
	writeJSON(w, http.StatusCreated, link)
}

// ── Refresh — POST …/code-links/{linkId}/refresh ─────────────────────────

// Refresh re-reads the link from its provider and updates the stored state.
//
// A failed refresh is recorded on the row (last_sync_error) as well as
// returned. The row keeps its previous state so the UI shows "merged, last
// checked Tuesday, refresh is failing because the token was revoked" rather
// than losing what it knew.
func (h *CodeLinkHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}
	missionID, wsID, ok := h.resolveIssue(w, r)
	if !ok {
		return
	}
	crewID := r.PathValue("crewId")
	linkID := r.PathValue("linkId")

	existing, err := h.loadOne(r.Context(), linkID, wsID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeCodeLinkProblem(w, r, http.StatusNotFound, "link-not-found", "Code link not found", 0)
			return
		}
		internalError(w, r, h.logger, "load code link", err)
		return
	}
	if existing.MissionID != missionID {
		// Do not distinguish "belongs to another issue" from "does not
		// exist" — that is a cross-issue existence oracle.
		writeCodeLinkProblem(w, r, http.StatusNotFound, "link-not-found", "Code link not found", 0)
		return
	}

	ref := gitlink.Ref{
		Provider: gitlink.Provider(existing.Provider),
		Host:     existing.Host,
		Owner:    existing.Owner,
		Repo:     existing.Repo,
		Number:   existing.Number,
		Kind:     gitlink.Kind(existing.Kind),
		Scheme:   schemeOf(existing.URL),
		URL:      existing.URL,
	}

	cred, err := resolveCodeLinkCredential(r.Context(), h.db, wsID, crewID, ref.Provider, ref.Host)
	if err != nil {
		h.noteSyncError(r.Context(), linkID, err.Error())
		h.replyCredentialProblem(w, r, ref, err)
		return
	}

	details, err := gitlink.NewClient(allowPrivateGitHosts(r.Context(), h.db)).Fetch(r.Context(), ref, cred.token)
	if err != nil {
		h.noteSyncError(r.Context(), linkID, err.Error())
		writeFetchProblem(w, r, err)
		return
	}
	recordCredentialEventBestEffort(r.Context(), h.db, h.logger, cred.id, AuditEventUse, "", clientIP(r),
		map[string]any{"purpose": "code_link_refresh", "host": ref.Host})

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(r.Context(), `
		UPDATE mission_code_links
		SET title = ?, state = ?, author = ?, source_branch = ?, target_branch = ?,
		    remote_created_at = ?, remote_updated_at = ?, remote_merged_at = ?, remote_closed_at = ?,
		    credential_id = ?, last_synced_at = ?, last_sync_error = NULL, updated_at = ?
		WHERE id = ? AND workspace_id = ?`,
		details.Title, string(details.State), details.Author, details.SourceBranch, details.TargetBranch,
		nullIfEmpty(details.CreatedAt), nullIfEmpty(details.UpdatedAt),
		nullIfEmpty(details.MergedAt), nullIfEmpty(details.ClosedAt),
		cred.id, now, now, linkID, wsID); err != nil {
		internalError(w, r, h.logger, "update code link", err)
		return
	}

	broadcastWorkspaceEvent(h.hub, wsID, "issue.updated", map[string]string{"id": missionID})

	link, err := h.loadOne(r.Context(), linkID, wsID)
	if err != nil {
		internalError(w, r, h.logger, "reload code link", err)
		return
	}
	writeJSON(w, http.StatusOK, link)
}

// ── Delete — DELETE …/code-links/{linkId} ────────────────────────────────

// Delete removes a link. Hard delete: mission_relations, the closest
// neighbour, works the same way, and an un-asserted link leaves nothing worth
// auditing behind.
func (h *CodeLinkHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}
	missionID, wsID, ok := h.resolveIssue(w, r)
	if !ok {
		return
	}

	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM mission_code_links WHERE id = ? AND mission_id = ? AND workspace_id = ?`,
		r.PathValue("linkId"), missionID, wsID)
	if err != nil {
		internalError(w, r, h.logger, "delete code link", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeCodeLinkProblem(w, r, http.StatusNotFound, "link-not-found", "Code link not found", 0)
		return
	}

	var userID string
	if u := UserFromContext(r.Context()); u != nil {
		userID = u.ID
	}
	h.logActivity(r.Context(), missionID, userID, "code_link_removed", r.PathValue("linkId"))
	broadcastWorkspaceEvent(h.hub, wsID, "issue.updated", map[string]string{"id": missionID})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── plumbing ─────────────────────────────────────────────────────────────

// resolveIssue turns {crewId}/{identifier} into a mission id inside the
// caller's workspace, replying 404 when it does not resolve.
func (h *CodeLinkHandler) resolveIssue(w http.ResponseWriter, r *http.Request) (missionID, wsID string, ok bool) {
	wsID = WorkspaceIDFromContext(r.Context())
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
		r.PathValue("identifier"), r.PathValue("crewId"), wsID).Scan(&missionID)
	if err != nil {
		writeCodeLinkProblem(w, r, http.StatusNotFound, "issue-not-found", "Issue not found", 0)
		return "", "", false
	}
	return missionID, wsID, true
}

func (h *CodeLinkHandler) loadOne(ctx context.Context, id, wsID string) (codeLinkResponse, error) {
	row := h.db.QueryRowContext(ctx, codeLinkColumns+` WHERE id = ? AND workspace_id = ?`, id, wsID)
	return scanCodeLink(row)
}

// noteSyncError records why the last refresh failed. Best-effort: the caller
// is already returning an error and must not be turned into a 500 by a
// bookkeeping write.
func (h *CodeLinkHandler) noteSyncError(ctx context.Context, linkID, detail string) {
	if _, err := h.db.ExecContext(ctx,
		`UPDATE mission_code_links SET last_sync_error = ?, updated_at = ? WHERE id = ?`,
		truncate(detail, 500), time.Now().UTC().Format(time.RFC3339), linkID); err != nil {
		h.logger.Warn("record code link sync error", "link_id", linkID, "error", err)
	}
}

// logActivity mirrors the issue handlers' mission_activity trail so attaching
// and removing a link show up on the issue timeline like every other change.
func (h *CodeLinkHandler) logActivity(ctx context.Context, missionID, userID, action, details string) {
	actorType, actorID := "user", userID
	if actorID == "" {
		actorType, actorID = "system", ""
	}
	if _, err := h.db.ExecContext(ctx,
		`INSERT INTO mission_activity (id, mission_id, actor_type, actor_id, action, details, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		generateCUID(), missionID, actorType, actorID, action, details,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		h.logger.Error("insert code link activity", "action", action, "mission_id", missionID, "error", err)
	}
}

// ── credential resolution ────────────────────────────────────────────────

// errNoCodeLinkCredential means the workspace has no stored credential this
// host can be fetched with. It is a precondition failure, not a server error:
// the fix is to add or label a credential.
var errNoCodeLinkCredential = errors.New("no usable credential for this host")

// errCodeLinkCredentialUnreadable means a credential matched but its stored
// value could not be decrypted — a real server-side fault.
var errCodeLinkCredentialUnreadable = errors.New("the matching credential could not be decrypted")

type codeLinkCredential struct {
	id    string
	token string
	name  string
}

// resolveCodeLinkCredential picks the stored token used to talk to `host`.
//
// It invents no new secret store: the candidates are ordinary rows in
// `credentials`, filtered the way every other consumer filters them (this
// workspace, ACTIVE, not soft-deleted, and either workspace-wide or scoped to
// the crew that owns the issue). The value is decrypted with the same
// encryption helper the vault uses, and is passed to the fetcher in memory —
// never into a URL, a query string or a command line.
//
// Which of several candidates wins:
//
//  1. a credential whose `account_label` is the host. That column already
//     exists to tell multiple accounts of one provider apart, and it is the
//     only field in the schema that can express "this token is for
//     gitlab.acme.internal". Exact, case-insensitive.
//  2. failing that, and ONLY for the provider's canonical SaaS host, the
//     oldest ACTIVE credential for that provider. github.com and gitlab.com
//     have exactly one meaning, so "the workspace's GitHub token" is
//     unambiguous there; ordering by created_at then id makes the choice
//     deterministic rather than whatever SQLite returns first.
//  3. otherwise: refuse. Reaching for an arbitrary token when the host is a
//     self-hosted instance would send a github.com PAT to whatever host was
//     pasted — the exact credential-exfiltration shape the SSRF guard exists
//     to prevent, just with a valid destination.
func resolveCodeLinkCredential(
	ctx context.Context,
	db *sql.DB,
	wsID, crewID string,
	provider gitlink.Provider,
	host string,
) (codeLinkCredential, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, name, COALESCE(account_label, ''), encrypted_value
		FROM credentials
		WHERE workspace_id = ?
		  AND provider = ?
		  AND status = 'ACTIVE'
		  AND deleted_at IS NULL
		  AND (crew_id IS NULL OR crew_id = ?)
		ORDER BY created_at ASC, id ASC`,
		wsID, string(provider), crewID)
	if err != nil {
		return codeLinkCredential{}, err
	}
	defer rows.Close()

	var fallback *codeLinkCredential
	for rows.Next() {
		var id, name, label, enc string
		if err := rows.Scan(&id, &name, &label, &enc); err != nil {
			return codeLinkCredential{}, err
		}
		if strings.EqualFold(strings.TrimSpace(label), host) {
			token, derr := encryption.Decrypt(enc)
			if derr != nil {
				return codeLinkCredential{}, errCodeLinkCredentialUnreadable
			}
			return codeLinkCredential{id: id, token: token, name: name}, nil
		}
		if fallback == nil && isCanonicalHost(provider, host) {
			token, derr := encryption.Decrypt(enc)
			if derr != nil {
				return codeLinkCredential{}, errCodeLinkCredentialUnreadable
			}
			fallback = &codeLinkCredential{id: id, token: token, name: name}
		}
	}
	if err := rows.Err(); err != nil {
		return codeLinkCredential{}, err
	}
	if fallback != nil {
		return *fallback, nil
	}
	return codeLinkCredential{}, errNoCodeLinkCredential
}

// isCanonicalHost reports whether host is the provider's one public instance,
// where "the workspace's GitHub token" is an unambiguous phrase.
func isCanonicalHost(p gitlink.Provider, host string) bool {
	switch p {
	case gitlink.ProviderGitHub:
		return host == "github.com" || host == "www.github.com"
	case gitlink.ProviderGitLab:
		return host == "gitlab.com" || host == "www.gitlab.com"
	default:
		return false
	}
}

// replyCredentialProblem turns a resolution failure into an actionable 4xx/5xx.
func (h *CodeLinkHandler) replyCredentialProblem(w http.ResponseWriter, r *http.Request, ref gitlink.Ref, err error) {
	switch {
	case errors.Is(err, errNoCodeLinkCredential):
		detail := fmt.Sprintf(
			"No ACTIVE %s credential in this workspace can reach %s. Add one, and for a self-hosted instance "+
				"set its account label to %q so it is matched by host.",
			ref.Provider, ref.Host, ref.Host)
		writeCodeLinkProblem(w, r, http.StatusPreconditionFailed, "no-credential", detail, 0)
	case errors.Is(err, errCodeLinkCredentialUnreadable):
		h.logger.Error("code link credential decrypt", "provider", ref.Provider, "host", ref.Host)
		writeCodeLinkProblem(w, r, http.StatusInternalServerError, "credential-unreadable",
			"The matching credential could not be decrypted; check ENCRYPTION_KEY.", 0)
	default:
		internalError(w, r, h.logger, "resolve code link credential", err)
	}
}

// ── problem responses ────────────────────────────────────────────────────

// writeCodeLinkProblem emits RFC 7807 with a stable `type` URI and a short
// machine-readable `code`. Both exist because the statuses are not injective:
// a revoked token, a forbidden repo and a provider outage are three different
// remedies that all come back as 502.
func writeCodeLinkProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string, retryAfter time.Duration) {
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Round(time.Second)/time.Second)))
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"type":     codeLinkProblemBase + code,
		"title":    http.StatusText(status),
		"status":   status,
		"detail":   detail,
		"code":     code,
		"instance": r.URL.Path,
	})
}

// writeFetchProblem maps a gitlink failure onto the response a user can act
// on. Each branch names a different remedy, which is the whole point: a
// generic "could not fetch pull request" sends everyone to the same dead end.
func writeFetchProblem(w http.ResponseWriter, r *http.Request, err error) {
	var fe *gitlink.FetchError
	_ = errors.As(err, &fe)
	retry := time.Duration(0)
	if fe != nil {
		retry = fe.RetryAfter
	}

	switch {
	case errors.Is(err, gitlink.ErrUnsupportedURL):
		writeCodeLinkProblem(w, r, http.StatusBadRequest, "unsupported-url", err.Error(), 0)
	case errors.Is(err, gitlink.ErrBlockedHost):
		writeCodeLinkProblem(w, r, http.StatusUnprocessableEntity, "blocked-host", err.Error(), 0)
	case errors.Is(err, gitlink.ErrNotFound):
		writeCodeLinkProblem(w, r, http.StatusNotFound, "pull-request-not-found", err.Error(), 0)
	case errors.Is(err, gitlink.ErrRateLimited):
		writeCodeLinkProblem(w, r, http.StatusTooManyRequests, "rate-limited", err.Error(), retry)
	case errors.Is(err, gitlink.ErrUnauthorized):
		writeCodeLinkProblem(w, r, http.StatusBadGateway, "credential-rejected", err.Error(), 0)
	case errors.Is(err, gitlink.ErrForbidden):
		writeCodeLinkProblem(w, r, http.StatusBadGateway, "credential-forbidden", err.Error(), 0)
	case errors.Is(err, gitlink.ErrProviderUnavailable):
		writeCodeLinkProblem(w, r, http.StatusBadGateway, "provider-unavailable", err.Error(), retry)
	default:
		writeCodeLinkProblem(w, r, http.StatusBadGateway, "unexpected-response", err.Error(), 0)
	}
}

// ── small helpers ────────────────────────────────────────────────────────

// allowPrivateGitHosts reads the instance opt-in. Anything other than an
// explicit truthy value keeps the strict default — a typo in the settings
// table must not open the network.
func allowPrivateGitHosts(ctx context.Context, db *sql.DB) bool {
	v, ok := readInstanceSetting(ctx, db, SettingAllowPrivateGitHosts)
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

// schemeOf recovers the scheme a stored link was created with, so a refresh
// dials the same way the attach did. Defaults to https.
func schemeOf(u string) string {
	if strings.HasPrefix(u, "http://") {
		return "http"
	}
	return "https"
}
