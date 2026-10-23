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

// An editor session expires when nobody used or renewed it for
// gridcraftSessionIdle; edits and heartbeats slide that expiry. It never lives
// longer than gridcraftSessionTTL from opening, nor beyond the human
// credential behind it: the web sign-in session (not its short-lived access
// token, which the app refreshes) or the CLI token.
const (
	gridcraftSessionIdle = 15 * time.Minute
	gridcraftSessionTTL  = 12 * time.Hour
	// An edit or heartbeat renews the idle expiry only once less than idle
	// minus this step remains, so at most one renewal per step and session is
	// written.
	gridcraftSessionRenewStep = 5 * time.Minute
	// Expired sessions linger this long, so a late edit still learns that its
	// session expired rather than that it never existed; then they are pruned.
	gridcraftSessionLinger = 24 * time.Hour
	// The outbox sweeper prunes lingering sessions at most this often.
	gridcraftSessionPruneInterval = 10 * time.Minute
)

// gridcraftMaxSessions bounds the live editor sessions of one user on one Page.
const gridcraftMaxSessions = 32

// gridcraftEditorProtocol is the editor bridge protocol version.
const gridcraftEditorProtocol = 1

var (
	errGridCraftEditorDisabled = errors.New("gridcraft_editor_disabled")
	// The expired-session refusal is deliberately not a 401: the web app's
	// fetch wrapper treats every 401 as an expired sign-in and refreshes or
	// signs out. A client reopens an editor session instead.
	errGridCraftSessionExpired = errors.New("editor_session_expired")
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
		replyError(w, 409, errGridCraftSessionExpired.Error())
	default:
		return false
	}
	return true
}

// humanCredential identifies the credential of a human request: the sign-in
// session for the web app (which outlives every access token refreshed from
// it), the CLI token otherwise.
func humanCredential(r *http.Request) (string, bool) {
	user := UserFromContext(r.Context())
	switch {
	case user == nil:
		return "", false
	case user.SessionID != "":
		return "session:" + user.SessionID, true
	case user.TokenID != "":
		return "token:" + user.TokenID, true
	}
	return "", false
}

// credentialLimit is the expiry of the human credential an editor session may
// not outlive: the current sign-in session record for the web app, read in tx,
// or the CLI token's expiry. Zero means the credential does not expire.
func credentialLimit(ctx context.Context, tx *sql.Tx, r *http.Request) (time.Time, error) {
	user := UserFromContext(r.Context())
	if user == nil {
		return time.Time{}, appengine.ErrForbidden
	}
	if user.SessionID == "" {
		return user.CredentialExpiresAt, nil
	}
	var expiry string
	if err := tx.QueryRowContext(ctx, `SELECT expires_at FROM user_sessions WHERE id=? AND user_id=? AND revoked_at IS NULL`, user.SessionID, user.ID).Scan(&expiry); err != nil {
		return time.Time{}, appengine.ErrForbidden
	}
	limit, err := time.Parse(time.RFC3339Nano, expiry)
	if err != nil {
		return time.Time{}, appengine.ErrForbidden
	}
	return limit, nil
}

// sessionDeadline is a session's expiry when used at now: the idle expiry,
// bounded by the absolute lifetime from opening and by the credential.
func sessionDeadline(created, now, limit time.Time) time.Time {
	expires := now.Add(gridcraftSessionIdle)
	if absolute := created.Add(gridcraftSessionTTL); absolute.Before(expires) {
		expires = absolute
	}
	if !limit.IsZero() && limit.Before(expires) {
		expires = limit
	}
	return expires
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
	credential, ok := humanCredential(r)
	if !ok {
		gridcraftError(w, appengine.ErrForbidden)
		return
	}
	if req.SessionID != "" {
		if out, p, ok := h.quietHeartbeat(r, k, credential, req.SessionID); ok {
			h.writeSession(w, r, 200, out, p)
			return
		}
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
	limit, err := credentialLimit(ctx, tx, r)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	now := time.Now().UTC()
	status := 201
	if req.SessionID != "" {
		// A heartbeat renews only a live session of this user, credential,
		// Page and generation; anything else must open a new session.
		live, err := h.liveSession(ctx, tx, k, user, credential, req.SessionID)
		if err != nil {
			if !editorError(w, err) {
				gridcraftError(w, err)
			}
			return
		}
		expires := sessionDeadline(live.created, now, limit)
		if !expires.After(now) {
			editorError(w, errGridCraftSessionExpired)
			return
		}
		if _, err = tx.ExecContext(ctx, `UPDATE gridcraft_sessions SET expires_at=? WHERE session_id=?`, expires.UnixMilli(), req.SessionID); err != nil {
			gridcraftError(w, err)
			return
		}
		out.SessionID, status = req.SessionID, 200
	} else {
		expires := sessionDeadline(now, now, limit)
		if !expires.After(now) {
			gridcraftError(w, appengine.ErrForbidden)
			return
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM gridcraft_sessions WHERE page_id=? AND expires_at<?`, k.PageID, now.Add(-gridcraftSessionLinger).UnixMilli()); err != nil {
			gridcraftError(w, err)
			return
		}
		var live int
		var soonest sql.NullInt64
		if err = tx.QueryRowContext(ctx, `SELECT count(*),min(expires_at) FROM gridcraft_sessions WHERE page_id=? AND user_id=? AND closed_at IS NULL AND expires_at>?`, k.PageID, user, now.UnixMilli()).Scan(&live, &soonest); err != nil {
			gridcraftError(w, err)
			return
		}
		if live >= gridcraftMaxSessions {
			// Unused sessions expire when idle; retry once the first one has.
			wait := time.UnixMilli(soonest.Int64).Sub(now)
			w.Header().Set("Retry-After", strconv.FormatInt(max(1, int64((wait+time.Second-1)/time.Second)), 10))
			replyError(w, 429, "too_many_editor_sessions")
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
	h.writeSession(w, r, status, out, p)
}

func (h *gridcraftHTTP) writeSession(w http.ResponseWriter, r *http.Request, status int, out gridcraftEditorSession, p appengine.Principal) {
	out.ActorLabel = h.actorLabel(r.Context(), p.AuthorityID)
	if h.editorBundle != nil {
		out.BundleDigest, out.EditorURL = h.editorBundle()
	}
	writeJSON(w, status, out)
}

// quietHeartbeat answers a heartbeat that has nothing to write: the session is
// live, more than idle minus a renew step remains, and no limit (the absolute
// lifetime, the sign-in or token) now ends it earlier. Like an edit's renewal
// it then writes nothing and takes no write lock; it reports the stored
// expiry. It reports false whenever the writing path has to run, which also
// answers every refusal.
func (h *gridcraftHTTP) quietHeartbeat(r *http.Request, k appengine.Key, credential, session string) (gridcraftEditorSession, appengine.Principal, bool) {
	ctx := r.Context()
	out := gridcraftEditorSession{SessionID: session, PageID: k.PageID, Protocol: gridcraftEditorProtocol}
	tx, err := h.pages.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, appengine.Principal{}, false
	}
	defer tx.Rollback()
	p, err := h.humanAuthority(r, k, true, nil)(ctx, tx)
	if err != nil || !h.editorOn(ctx, tx, k.WorkspaceID) {
		return out, p, false
	}
	if tx.QueryRowContext(ctx, `SELECT head_rev,generation FROM gridcraft_heads WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&out.Rev, &out.Generation) != nil {
		return out, p, false
	}
	limit, err := credentialLimit(ctx, tx, r)
	if err != nil {
		return out, p, false
	}
	live, err := h.liveSession(ctx, tx, k, UserFromContext(ctx).ID, credential, session)
	if err != nil {
		return out, p, false
	}
	now := time.Now()
	if live.expires.Sub(now) <= gridcraftSessionIdle-gridcraftSessionRenewStep || sessionDeadline(live.created, now, limit).Before(live.expires) {
		return out, p, false
	}
	out.ExpiresAt = tsformat.Format(live.expires.UTC())
	return out, p, true
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

// liveEditorSession is what an edit or heartbeat learns about its session.
type liveEditorSession struct {
	created, expires time.Time
}

// liveSession checks, in the caller's transaction, that session is a live
// editor session of user, opened with credential on this Page for the current
// workbook generation. Someone else's session is not found. An expired, closed
// or older-generation session, or the user's own session opened with another
// credential (another sign-in or token), must be replaced.
func (h *gridcraftHTTP) liveSession(ctx context.Context, tx *sql.Tx, k appengine.Key, user, credential, session string) (liveEditorSession, error) {
	var owner, bound string
	var generation, head, created, expires int64
	var closed sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT s.user_id,s.credential,s.generation,s.created_at,s.expires_at,s.closed_at,h.generation FROM gridcraft_sessions s JOIN gridcraft_heads h ON h.page_id=s.page_id AND h.workspace_id=s.workspace_id WHERE s.session_id=? AND s.workspace_id=? AND s.page_id=?`, session, k.WorkspaceID, k.PageID).Scan(&owner, &bound, &generation, &created, &expires, &closed, &head)
	if errors.Is(err, sql.ErrNoRows) {
		return liveEditorSession{}, appengine.ErrForbidden
	}
	if err != nil {
		return liveEditorSession{}, err
	}
	if owner != user {
		return liveEditorSession{}, appengine.ErrForbidden
	}
	live := liveEditorSession{created: time.UnixMilli(created), expires: time.UnixMilli(expires)}
	now := time.Now()
	if bound != credential || closed.Valid || !now.Before(live.expires) || !now.Before(live.created.Add(gridcraftSessionTTL)) || generation != head {
		return liveEditorSession{}, errGridCraftSessionExpired
	}
	return live, nil
}

// sessionAuthority is the human edit authority: the Page owner's write
// authority, the editor switches and a live own session, all rechecked inside
// every writer transaction of the edit. seen receives the session as last
// checked, for renewOnUse.
func (h *gridcraftHTTP) sessionAuthority(r *http.Request, k appengine.Key, session string, seen *liveEditorSession) appengine.Authorize {
	human := h.humanAuthority(r, k, true, nil)
	var mu sync.Mutex
	return func(ctx context.Context, tx *sql.Tx) (appengine.Principal, error) {
		p, err := human(ctx, tx)
		if err != nil {
			return appengine.Principal{}, err
		}
		if !h.editorOn(ctx, tx, k.WorkspaceID) {
			return appengine.Principal{}, errGridCraftEditorDisabled
		}
		credential, ok := humanCredential(r)
		if !ok {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		live, err := h.liveSession(ctx, tx, k, UserFromContext(ctx).ID, credential, session)
		if err != nil {
			return appengine.Principal{}, err
		}
		mu.Lock()
		*seen = live
		mu.Unlock()
		p.MandateID = appengine.HumanEditMandate
		return p, nil
	}
}

// renewOnUse slides the idle expiry of a session after a confirmed edit. It
// writes only once less than idle minus a renew step remains, so most edits
// add no write; a failure only leaves the session to expire as it would have.
func (h *gridcraftHTTP) renewOnUse(r *http.Request, session string, seen liveEditorSession) {
	now := time.Now()
	if seen.expires.IsZero() || seen.expires.Sub(now) > gridcraftSessionIdle-gridcraftSessionRenewStep {
		return
	}
	ctx := r.Context()
	err := func() error {
		tx, err := h.pages.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		limit, err := credentialLimit(ctx, tx, r)
		if err != nil {
			return err
		}
		expires := sessionDeadline(seen.created, now, limit)
		if _, err = tx.ExecContext(ctx, `UPDATE gridcraft_sessions SET expires_at=? WHERE session_id=? AND closed_at IS NULL AND expires_at>? AND expires_at<?`, expires.UnixMilli(), session, now.UnixMilli(), expires.UnixMilli()); err != nil {
			return err
		}
		return tx.Commit()
	}()
	if err != nil && h.pages.logger != nil {
		h.pages.logger.Warn("workbook editor session renewal", "error", err)
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
// and pushes each on its Page channel while the live editor is on for its
// workspace. The hub rechecks the recipient's access before every frame.
// Delivery is at most once: a claimed revision is not pushed again, also when
// the editor was off, and clients recover a missed push from the commits feed.
func (h *gridcraftHTTP) publish(ctx context.Context, pageID string) error {
	editor := map[string]bool{}
	for {
		claimed, err := h.store.ClaimCommitted(ctx, pageID, 100)
		if err != nil {
			return err
		}
		for _, c := range claimed {
			if !c.Deliverable || h.broadcast == nil {
				continue
			}
			on, known := editor[c.WorkspaceID]
			if !known {
				on = h.editorOn(ctx, h.pages.db, c.WorkspaceID)
				editor[c.WorkspaceID] = on
			}
			if on {
				h.broadcast("page", c.PageID, "workbook.committed", gridcraftCommitted{PageID: c.PageID, Rev: c.Rev, Generation: c.Generation, Kind: c.Kind, Cells: c.Cells})
			}
		}
		if len(claimed) < 100 {
			return nil
		}
	}
}

// published hands a confirmed revision to the push. With the sweeper started
// it only wakes the sweeper, so claiming and pushing stay off the response
// path; without one (a server that never started it) it pushes inline,
// detached from the request so a disconnecting client cannot cancel it.
func (h *gridcraftHTTP) published(pageID string) {
	if h.outbox.notify() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.publish(ctx, pageID); err != nil && h.pages.logger != nil {
		h.pages.logger.Warn("workbook push deferred to sweeper", "error", err)
	}
}

// gridcraftOutboxWorker pushes confirmed revisions when woken by a commit and
// sweeps every few seconds, so a revision whose push was not yet claimed when
// the server stopped is pushed after the next start.
type gridcraftOutboxWorker struct {
	h      *gridcraftHTTP
	mu     sync.Mutex
	cancel context.CancelFunc
	kick   chan struct{}
	wg     sync.WaitGroup
	// pruned is when the sweeper last pruned sessions; only its goroutine
	// touches it.
	pruned time.Time
}

const gridcraftOutboxInterval = 5 * time.Second

// StartGridCraftOutbox starts the workbook outbox sweeper for the server's
// lifetime. It does nothing when the workbook API is not installed.
func (r *Router) StartGridCraftOutbox(ctx context.Context) {
	if r.gridcraftOutbox == nil {
		return
	}
	r.gridcraftOutbox.start(ctx)
}

func (w *gridcraftOutboxWorker) start(parent context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.kick != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	w.cancel = cancel
	kick := make(chan struct{}, 1)
	w.kick = kick
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
			case <-kick:
			}
			w.sweep(ctx)
		}
	}()
}

// notify wakes a started sweeper and reports whether it was ever started.
// After stop it drops the wake-up: the revision stays pending for the next
// start rather than being pushed while the server shuts down.
func (w *gridcraftOutboxWorker) notify() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	kick := w.kick
	w.mu.Unlock()
	if kick == nil {
		return false
	}
	select {
	case kick <- struct{}{}:
	default:
	}
	return true
}

func (w *gridcraftOutboxWorker) sweep(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	attempt, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := w.h.publish(attempt, ""); err != nil && ctx.Err() == nil && w.h.pages.logger != nil {
		w.h.pages.logger.Warn("workbook outbox sweep", "error", err)
	}
	if now := time.Now(); now.Sub(w.pruned) >= gridcraftSessionPruneInterval {
		w.pruned = now
		if _, err := w.h.pruneSessions(attempt, now); err != nil && ctx.Err() == nil && w.h.pages.logger != nil {
			w.h.pages.logger.Warn("workbook editor session prune", "error", err)
		}
	}
}

// pruneSessions deletes the editor sessions of every Page that expired more
// than the linger period before now, and reports how many. Opening a session
// prunes its own Page too; this reaches Pages nobody opens again. It checks
// for such rows with a plain read first, so a prune with nothing to delete
// takes no write lock.
func (h *gridcraftHTTP) pruneSessions(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.Add(-gridcraftSessionLinger).UnixMilli()
	var due bool
	if err := h.pages.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gridcraft_sessions WHERE expires_at<?)`, cutoff).Scan(&due); err != nil || !due {
		return 0, err
	}
	result, err := h.pages.db.ExecContext(ctx, `DELETE FROM gridcraft_sessions WHERE expires_at<?`, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (w *gridcraftOutboxWorker) stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	cancel := w.cancel
	w.mu.Unlock()
	if cancel != nil {
		cancel()
		w.wg.Wait()
	}
}
