package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/featureflags"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Live workbook editing: editor sessions, session-bound human cell edits, the
// commits feed and the workbook.committed push. The surface needs both the
// operator switch (appengine.Options.Editor) and the workspace flag.

// gridcraftEditorFlag is the workspace kill switch of the live editor surface.
const gridcraftEditorFlag = "gridcraft_editor"

// gridcraftSessionTTL bounds one editor session; it also never outlives the
// credential that opened it. A heartbeat renews it.
const gridcraftSessionTTL = 12 * time.Hour

// gridcraftMaxSessions bounds the live editor sessions of one user on one Page.
const gridcraftMaxSessions = 32

// gridcraftEditorProtocol is the editor bridge protocol version.
const gridcraftEditorProtocol = 1

var (
	errGridCraftEditorDisabled = errors.New("gridcraft_editor_disabled")
	errGridCraftSessionExpired = errors.New("session_expired")
)

// editorOn reports whether the live editor surface is enabled for workspace.
// A failed flag read is treated as off.
func (h *gridcraftHTTP) editorOn(ctx context.Context, q featureflags.RowQuerier, workspace string) bool {
	if !h.editor {
		return false
	}
	on, err := featureflags.IsEnabledWith(ctx, q, workspace, gridcraftEditorFlag)
	return err == nil && on
}

// editorError writes the live-editing refusals; it reports whether it did.
func editorError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, errGridCraftEditorDisabled):
		replyError(w, 503, errGridCraftEditorDisabled.Error())
	case errors.Is(err, errGridCraftSessionExpired):
		replyError(w, 401, errGridCraftSessionExpired.Error())
	default:
		return false
	}
	return true
}

// humanCredential identifies the credential of a human request: the auth
// session for the web app, the CLI token otherwise, with its expiry (zero when
// it has none).
func humanCredential(r *http.Request) (string, time.Time, bool) {
	user := UserFromContext(r.Context())
	switch {
	case user == nil:
		return "", time.Time{}, false
	case user.SessionID != "":
		return "session:" + user.SessionID, user.CredentialExpiresAt, true
	case user.TokenID != "":
		return "token:" + user.TokenID, user.CredentialExpiresAt, true
	}
	return "", time.Time{}, false
}

type gridcraftEditorSession struct {
	SessionID  string `json:"session_id"`
	PageID     string `json:"page_id"`
	ActorLabel string `json:"actor_label"`
	Rev        int64  `json:"rev"`
	Generation int64  `json:"generation"`
	Protocol   int    `json:"protocol"`
	ExpiresAt  string `json:"expires_at"`
	// BundleDigest and EditorURL name the qualified editor bundle the host
	// may frame. They stay empty until an editor origin is configured, and a
	// host must then not frame anything.
	BundleDigest string `json:"bundle_digest"`
	EditorURL    string `json:"editor_url,omitempty"`
}

type gridcraftSessionRequest struct {
	SessionID string `json:"session_id,omitempty"`
	// Protocol, when sent, must be the editor bridge protocol the server speaks.
	Protocol *int `json:"protocol,omitempty"`
}

// newEditorSessionID returns 128 random bits in the session ID charset.
func newEditorSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "ws_" + base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// openSession opens an editor session for the Page owner, or with a
// session_id renews that live session (heartbeat). A session is bound to its
// user, the credential that opened it, the Page and the workbook generation.
func (h *gridcraftHTTP) openSession(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	k, err := h.humanKey(r, true)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	if !h.editorOn(r.Context(), h.pages.db, k.WorkspaceID) {
		editorError(w, errGridCraftEditorDisabled)
		return
	}
	var req gridcraftSessionRequest
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	raw, err := io.ReadAll(r.Body)
	if err != nil || (len(raw) > 0 && appengine.DecodeStrict(raw, &req) != nil) || (req.SessionID != "" && !appengine.ValidSessionID(req.SessionID)) || (req.Protocol != nil && *req.Protocol != gridcraftEditorProtocol) {
		gridcraftError(w, appengine.ErrInvalid)
		return
	}
	credential, credentialExpiry, ok := humanCredential(r)
	if !ok {
		gridcraftError(w, appengine.ErrForbidden)
		return
	}
	ctx := r.Context()
	tx, err := h.pages.db.BeginTx(ctx, nil)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE gridcraft_writer_fence SET seq=seq WHERE id=1`); err != nil {
		gridcraftError(w, err)
		return
	}
	p, err := h.humanAuthority(r, k, true, nil)(ctx, tx)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	if !h.editorOn(ctx, tx, k.WorkspaceID) {
		editorError(w, errGridCraftEditorDisabled)
		return
	}
	user := UserFromContext(ctx).ID
	out := gridcraftEditorSession{PageID: k.PageID, Protocol: gridcraftEditorProtocol}
	if err = tx.QueryRowContext(ctx, `SELECT head_rev,generation FROM gridcraft_heads WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&out.Rev, &out.Generation); err != nil {
		gridcraftError(w, appengine.ErrNotFound)
		return
	}
	now := time.Now().UTC()
	expires := now.Add(gridcraftSessionTTL)
	if !credentialExpiry.IsZero() && credentialExpiry.Before(expires) {
		expires = credentialExpiry
	}
	if !expires.After(now) {
		gridcraftError(w, appengine.ErrForbidden)
		return
	}
	status := 201
	if req.SessionID != "" {
		// A heartbeat renews only a live session of this user, credential,
		// Page and generation; anything else must open a new session.
		if err = h.liveSession(ctx, tx, k, user, credential, req.SessionID); err != nil {
			if !editorError(w, err) {
				gridcraftError(w, err)
			}
			return
		}
		if _, err = tx.ExecContext(ctx, `UPDATE gridcraft_sessions SET expires_at=max(expires_at,?) WHERE session_id=?`, expires.UnixMilli(), req.SessionID); err != nil {
			gridcraftError(w, err)
			return
		}
		out.SessionID, status = req.SessionID, 200
	} else {
		// Expired sessions linger a day, so a late edit still learns that its
		// session expired rather than that it never existed.
		if _, err = tx.ExecContext(ctx, `DELETE FROM gridcraft_sessions WHERE page_id=? AND expires_at<?`, k.PageID, now.Add(-24*time.Hour).UnixMilli()); err != nil {
			gridcraftError(w, err)
			return
		}
		var live int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM gridcraft_sessions WHERE page_id=? AND user_id=? AND closed_at IS NULL AND expires_at>?`, k.PageID, user, now.UnixMilli()).Scan(&live); err != nil {
			gridcraftError(w, err)
			return
		}
		if live >= gridcraftMaxSessions {
			replyError(w, 429, "too many open editor sessions; close one first")
			return
		}
		if out.SessionID, err = newEditorSessionID(); err != nil {
			gridcraftError(w, err)
			return
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO gridcraft_sessions(session_id,workspace_id,page_id,user_id,credential,generation,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?)`, out.SessionID, k.WorkspaceID, k.PageID, user, credential, out.Generation, now.UnixMilli(), expires.UnixMilli()); err != nil {
			gridcraftError(w, err)
			return
		}
	}
	var stored int64
	if err = tx.QueryRowContext(ctx, `SELECT expires_at FROM gridcraft_sessions WHERE session_id=?`, out.SessionID).Scan(&stored); err != nil {
		gridcraftError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		gridcraftError(w, err)
		return
	}
	out.ExpiresAt = tsformat.Format(time.UnixMilli(stored).UTC())
	out.ActorLabel = h.actorLabel(ctx, p.AuthorityID)
	if h.editorBundle != nil {
		out.BundleDigest, out.EditorURL = h.editorBundle()
	}
	writeJSON(w, status, out)
}

// closeSession ends one of the caller's own editor sessions on this Page.
// Closing an already closed or expired session succeeds again.
func (h *gridcraftHTTP) closeSession(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	k, err := h.humanKey(r, true)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	if !h.editorOn(r.Context(), h.pages.db, k.WorkspaceID) {
		editorError(w, errGridCraftEditorDisabled)
		return
	}
	session := r.PathValue("session")
	if !appengine.ValidSessionID(session) {
		gridcraftError(w, appengine.ErrNotFound)
		return
	}
	ctx := r.Context()
	tx, err := h.pages.db.BeginTx(ctx, nil)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	defer tx.Rollback()
	if _, err = h.humanAuthority(r, k, true, nil)(ctx, tx); err != nil {
		gridcraftError(w, err)
		return
	}
	result, err := tx.ExecContext(ctx, `UPDATE gridcraft_sessions SET closed_at=coalesce(closed_at,?) WHERE session_id=? AND workspace_id=? AND page_id=? AND user_id=?`, time.Now().UnixMilli(), session, k.WorkspaceID, k.PageID, UserFromContext(ctx).ID)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		gridcraftError(w, appengine.ErrNotFound)
		return
	}
	if err = tx.Commit(); err != nil {
		gridcraftError(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "closed", "session_id": session})
}

// liveSession checks, in the caller's transaction, that session is a live
// editor session of user, opened with credential on this Page for the current
// workbook generation. Someone else's session is not found; an expired, closed
// or older-generation session must be replaced.
func (h *gridcraftHTTP) liveSession(ctx context.Context, tx *sql.Tx, k appengine.Key, user, credential, session string) error {
	var owner, bound string
	var generation, head, expires int64
	var closed sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT s.user_id,s.credential,s.generation,s.expires_at,s.closed_at,h.generation FROM gridcraft_sessions s JOIN gridcraft_heads h ON h.page_id=s.page_id AND h.workspace_id=s.workspace_id WHERE s.session_id=? AND s.workspace_id=? AND s.page_id=?`, session, k.WorkspaceID, k.PageID).Scan(&owner, &bound, &generation, &expires, &closed, &head)
	if errors.Is(err, sql.ErrNoRows) {
		return appengine.ErrForbidden
	}
	if err != nil {
		return err
	}
	if owner != user || bound != credential {
		return appengine.ErrForbidden
	}
	if closed.Valid || time.Now().UnixMilli() >= expires || generation != head {
		return errGridCraftSessionExpired
	}
	return nil
}

// sessionAuthority is the human edit authority: the Page owner's write
// authority, the editor switches and a live own session, all rechecked inside
// every writer transaction of the edit.
func (h *gridcraftHTTP) sessionAuthority(r *http.Request, k appengine.Key, session string) appengine.Authorize {
	human := h.humanAuthority(r, k, true, nil)
	return func(ctx context.Context, tx *sql.Tx) (appengine.Principal, error) {
		p, err := human(ctx, tx)
		if err != nil {
			return appengine.Principal{}, err
		}
		if !h.editorOn(ctx, tx, k.WorkspaceID) {
			return appengine.Principal{}, errGridCraftEditorDisabled
		}
		credential, _, ok := humanCredential(r)
		if !ok {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		if err = h.liveSession(ctx, tx, k, UserFromContext(ctx).ID, credential, session); err != nil {
			return appengine.Principal{}, err
		}
		p.MandateID = appengine.HumanEditMandate
		return p, nil
	}
}

type gridcraftFeedOp struct {
	Sheet string `json:"sheet"`
	Cell  string `json:"cell"`
	Input string `json:"input"`
}

type gridcraftFeedCommit struct {
	Rev        int64             `json:"rev"`
	Kind       string            `json:"kind"`
	ActorLabel string            `json:"actor_label"`
	Own        bool              `json:"own"`
	SessionID  string            `json:"session_id,omitempty"`
	MutationID string            `json:"mutation_id,omitempty"`
	Ops        []gridcraftFeedOp `json:"ops,omitempty"`
}

type gridcraftFeed struct {
	HeadRev    int64                 `json:"head_rev"`
	Generation int64                 `json:"generation"`
	MinSince   int64                 `json:"min_since"`
	Commits    []gridcraftFeedCommit `json:"commits"`
}

// feedQuery parses since (required) and limit (optional, 1..MaxFeedLimit).
func feedQuery(r *http.Request) (int64, int, bool) {
	q := r.URL.Query()
	since, err := strconv.ParseInt(q.Get("since"), 10, 64)
	if err != nil || since < 0 {
		return 0, 0, false
	}
	limit := appengine.DefaultFeedLimit
	if raw := q.Get("limit"); raw != "" {
		if limit, err = strconv.Atoi(raw); err != nil || limit < 1 || limit > appengine.MaxFeedLimit {
			return 0, 0, false
		}
	}
	return since, limit, true
}

// renderFeed shows other writers by display label only, never by internal
// authority or session.
func (h *gridcraftHTTP) renderFeed(ctx context.Context, f appengine.Feed) gridcraftFeed {
	out := gridcraftFeed{HeadRev: f.HeadRev, Generation: f.Generation, MinSince: f.MinSince, Commits: make([]gridcraftFeedCommit, 0, len(f.Commits))}
	labels := map[string]string{}
	for _, c := range f.Commits {
		label, ok := labels[c.AuthorityID]
		if !ok {
			label = h.actorLabel(ctx, c.AuthorityID)
			labels[c.AuthorityID] = label
		}
		commit := gridcraftFeedCommit{Rev: c.Rev, Kind: c.Kind, ActorLabel: label, Own: c.Own, SessionID: c.SessionID, MutationID: c.MutationID}
		for _, cell := range c.Cells {
			commit.Ops = append(commit.Ops, gridcraftFeedOp{cell.Sheet, cell.Cell, cell.Input})
		}
		out.Commits = append(out.Commits, commit)
	}
	return out
}

// writeHistoryGone reports that the feed cannot serve the requested range.
func writeHistoryGone(w http.ResponseWriter, g *appengine.HistoryGoneError) {
	writeJSON(w, 410, map[string]any{"error": "snapshot_required", "head_rev": g.HeadRev, "generation": g.Generation, "min_since": g.MinSince})
}

// commits serves the confirmed revisions after since under the workbook read
// authorization; a workspace OWNER reading another member's workbook is audited.
func (h *gridcraftHTTP) commits(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	k, err := h.humanKey(r, false)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	if !h.editorOn(r.Context(), h.pages.db, k.WorkspaceID) {
		editorError(w, errGridCraftEditorDisabled)
		return
	}
	since, limit, ok := feedQuery(r)
	if !ok {
		gridcraftError(w, appengine.ErrInvalid)
		return
	}
	var record *pageRecord
	feed, err := h.store.Commits(r.Context(), k, since, limit, h.humanAuthority(r, k, false, func(p *pageRecord) { record = p }))
	var gone *appengine.HistoryGoneError
	if err != nil && !errors.As(err, &gone) {
		gridcraftError(w, err)
		return
	}
	if record == nil || !h.pages.auditPrivateOwnerAccess(w, r, record) {
		return
	}
	if gone != nil {
		writeHistoryGone(w, gone)
		return
	}
	writeJSON(w, 200, h.renderFeed(r.Context(), feed))
}

// gridcraftCommitted is the workbook.committed push. It names the revision
// only; subscribers read the content through the commits feed under their own
// current access.
type gridcraftCommitted struct {
	PageID     string `json:"page_id"`
	Rev        int64  `json:"rev"`
	Generation int64  `json:"generation"`
	Kind       string `json:"kind"`
	Cells      int    `json:"cells"`
}

// publish claims the undelivered revisions of one Page (all Pages when empty)
// and pushes each on its Page channel. The hub rechecks the recipient's access
// before every frame. Failures leave rows for the sweeper.
func (h *gridcraftHTTP) publish(ctx context.Context, pageID string) error {
	for {
		claimed, err := h.store.ClaimCommitted(ctx, pageID, 100)
		if err != nil {
			return err
		}
		for _, c := range claimed {
			if c.Deliverable && h.broadcast != nil {
				h.broadcast("page", c.PageID, "workbook.committed", gridcraftCommitted{PageID: c.PageID, Rev: c.Rev, Generation: c.Generation, Kind: c.Kind, Cells: c.Cells})
			}
		}
		if len(claimed) < 100 {
			return nil
		}
	}
}

// published pushes a confirmed revision after the response is decided. It is
// detached from the request so a disconnecting client cannot cancel it.
func (h *gridcraftHTTP) published(pageID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.publish(ctx, pageID); err != nil && h.pages.logger != nil {
		h.pages.logger.Warn("workbook push deferred to sweeper", "error", err)
	}
}

// gridcraftOutboxWorker sweeps undelivered revisions every few seconds, so a
// crash between commit and push delays the push instead of losing it.
type gridcraftOutboxWorker struct {
	h      *gridcraftHTTP
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

const gridcraftOutboxInterval = 5 * time.Second

// StartGridCraftOutbox starts the workbook outbox sweeper for the server's
// lifetime. It does nothing when the workbook API is not installed.
func (r *Router) StartGridCraftOutbox(ctx context.Context) {
	if r.gridcraftOutbox == nil || r.gridcraftOutbox.cancel != nil {
		return
	}
	r.gridcraftOutbox.start(ctx)
}

func (w *gridcraftOutboxWorker) start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	w.cancel = cancel
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		ticker := time.NewTicker(gridcraftOutboxInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.sweep(ctx)
			}
		}
	}()
}

func (w *gridcraftOutboxWorker) sweep(ctx context.Context) {
	attempt, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := w.h.publish(attempt, ""); err != nil && ctx.Err() == nil && w.h.pages.logger != nil {
		w.h.pages.logger.Warn("workbook outbox sweep", "error", err)
	}
}

func (w *gridcraftOutboxWorker) stop() {
	if w != nil && w.cancel != nil {
		w.cancel()
		w.wg.Wait()
	}
}
