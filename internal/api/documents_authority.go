package api

// This authority is served ONLY on a private Unix listener. It is deliberately
// absent from the public Router: editor/CLI delivery belongs to D1b.
import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/documents"
)

type documentAuthority struct {
	pages      *PageHandler
	store      *documents.Store
	middleware *AuthMiddleware
	secret     string
}

var errDocumentAccess = errors.New("document access denied")

// DocumentAuthority constructs the private authority. It is never registered
// on r.mux. Boot fencing runs only after the caller owns its Unix listener.
func (r *Router) DocumentAuthority(ctx context.Context, secret string) (http.Handler, error) {
	if len(secret) < 32 {
		return nil, errors.New("Docs service secret requires at least 32 characters")
	}
	s := documents.New(r.db, documents.Options{})
	return &documentAuthority{r.pages, s, r.authMw, secret}, nil
}
func (h *documentAuthority) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	provided := r.Header.Get("X-Crewship-Docs-Service")
	if len(provided) != len(h.secret) || subtle.ConstantTimeCompare([]byte(provided), []byte(h.secret)) != 1 {
		replyError(w, 403, "service authentication required")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == "GET" && r.URL.Path == "/health/ready" {
		writeJSON(w, 200, map[string]int{"protocol": 1, "schema": 1})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if r.Method != "POST" || len(parts) != 5 || strings.Join(parts[:3], "/") != "internal/docs/v1" || len(parts[3]) > 128 {
		replyError(w, 404, "unknown Docs authority operation")
		return
	}
	r.SetPathValue("page", parts[3])
	r.SetPathValue("operation", parts[4])
	operation := http.HandlerFunc(h.operation)
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer agtv2.") {
		// Resolve the transport's crew from the stored principal, then let the
		// existing run-capability verifier authenticate its HMAC and liveness.
		fields := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
		if len(fields) != 5 {
			replyError(w, 403, "invalid run capability")
			return
		}
		run, e := base64.RawURLEncoding.DecodeString(fields[3])
		if e != nil {
			replyError(w, 403, "invalid run capability")
			return
		}
		var workspace, crew string
		if h.pages.db.QueryRowContext(r.Context(), `SELECT workspace_id,crew_id FROM artifact_run_principals WHERE run_id=?`, string(run)).Scan(&workspace, &crew) != nil || workspace != r.Header.Get("X-Workspace-ID") {
			replyError(w, 403, "invalid run capability")
			return
		}
		ctx := context.WithValue(r.Context(), ctxInternalTokenWS, workspace)
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, crew)
		if checked, ok := h.pages.artifactAgentRequest(w, r.WithContext(ctx)); ok {
			operation.ServeHTTP(w, checked)
		}
		return
	}
	h.middleware.RequireAuth(h.middleware.RequireWorkspace(operation)).ServeHTTP(w, r)
}
func (h *documentAuthority) authorize(r *http.Request, k documents.Key, write, initialize bool) documents.Authorize {
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		user := UserFromContext(ctx)
		if user == nil {
			return "", errDocumentAccess
		}
		role, e := h.pages.liveContentHumanRole(ctx, k.WorkspaceID, user.ID)
		if e != nil {
			return "", errDocumentAccess
		}
		ctx = context.WithValue(ctx, ctxRole, role)
		var slug string
		if tx.QueryRowContext(ctx, `SELECT slug FROM pages WHERE id=? AND workspace_id=?`, k.PageID, k.WorkspaceID).Scan(&slug) != nil {
			return "", errDocumentAccess
		}
		rec, e := h.pages.loadPage(ctx, k.WorkspaceID, slug)
		if e != nil {
			return "", errDocumentAccess
		}
		if rec.HasArtifact || rec.HasBoard || rec.HasProject || rec.HasApplication {
			return "", errDocumentAccess
		}
		panels, e := h.pages.loadPanels(ctx, k.WorkspaceID, k.PageID)
		if e != nil || len(panels) != 0 {
			return "", errDocumentAccess
		}
		if a, ok := ctx.Value(artifactRunKey{}).(*artifactRunAuthority); ok {
			if initialize || h.pages.checkArtifactRun(ctx, k.PageID, a) != nil {
				return "", errDocumentAccess
			}
			return "agent:" + a.AgentID + ":" + a.RunID, nil
		}
		// Exact session, not merely some other active session for this user.
		var expiry string
		var revoked sql.NullString
		if user.SessionID == "" || tx.QueryRowContext(ctx, `SELECT expires_at,revoked_at FROM user_sessions WHERE id=? AND user_id=?`, user.SessionID, user.ID).Scan(&expiry, &revoked) != nil || revoked.Valid {
			return "", errDocumentAccess
		}
		limit, e := time.Parse(time.RFC3339Nano, expiry)
		if e != nil || !time.Now().Before(limit) {
			return "", errDocumentAccess
		}
		if initialize && (rec.OwnerUserID != user.ID || rec.OwnerCrewID != "") {
			return "", errDocumentAccess
		}
		if write {
			if !h.pages.mayEditSpec(ctx, k.WorkspaceID, user.ID, role, rec) {
				return "", errDocumentAccess
			}
		} else {
			viewer, e := h.pages.loadViewer(ctx, k.WorkspaceID, user.ID)
			if e != nil {
				return "", errDocumentAccess
			}
			ok, e := h.pages.canSeePage(ctx, k.WorkspaceID, rec, nil, viewer)
			if e != nil || !ok {
				return "", errDocumentAccess
			}
		}
		return "user:" + user.ID, nil
	}
}
func documentAuthorityError(w http.ResponseWriter, err error) {
	status := 500
	message := "document authority failed"
	switch {
	case errors.Is(err, errDocumentAccess):
		status = 403
		message = "document access denied"
	case errors.Is(err, documents.ErrNotFound):
		status = 404
		message = "document not found"
	case errors.Is(err, documents.ErrConflict), errors.Is(err, documents.ErrFence), errors.Is(err, documents.ErrSchema), errors.Is(err, documents.ErrDuplicate):
		status = 409
		message = err.Error()
	case errors.Is(err, documents.ErrQuota):
		status = 429
		message = "document quota exceeded; confirmed history preserved"
	}
	replyError(w, status, message)
}
func documentStateWire(s documents.State) any {
	updates := make([][]byte, 0, len(s.Updates))
	for _, u := range s.Updates {
		updates = append(updates, u.Payload)
	}
	return map[string]any{"schema": s.Schema, "rev": s.HeadRev, "epoch": s.Epoch, "snapshot": s.Snapshot, "snapshotRev": s.SnapshotRev, "updates": updates, "blocks": s.Projection}
}
func documentReceiptWire(r documents.Receipt) map[string]any {
	return map[string]any{"id": r.ID, "rev": r.Rev, "epoch": r.Epoch, "actor": r.Actor, "kind": r.Kind}
}
func (h *documentAuthority) operation(w http.ResponseWriter, r *http.Request) {
	k := documents.Key{WorkspaceID: WorkspaceIDFromContext(r.Context()), PageID: r.PathValue("page")}
	op := r.PathValue("operation")
	var in struct {
		ID          string            `json:"id"`
		Fingerprint string            `json:"fingerprint"`
		Schema      int               `json:"schema"`
		Snapshot    []byte            `json:"snapshot"`
		Update      []byte            `json:"update"`
		Blocks      json.RawMessage   `json:"blocks"`
		Owner       string            `json:"owner"`
		Token       string            `json:"token"`
		LeaseOwner  string            `json:"leaseOwner"`
		LeaseToken  string            `json:"leaseToken"`
		Epoch       int64             `json:"epoch"`
		IfRev       int64             `json:"ifRev"`
		IfHashes    map[string]string `json:"ifHashes"`
		Kind        string            `json:"kind"`
		Checkpoint  string            `json:"checkpoint"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 3<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil {
		replyError(w, 400, "invalid bounded authority request")
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		replyError(w, 400, "one authority JSON object required")
		return
	}
	read := op == "state" || op == "receipt" || op == "checkpoint-read"
	auth := h.authorize(r, k, !read, op == "initialize")
	lease := documents.Lease{Owner: in.LeaseOwner, Writer: in.LeaseToken, Epoch: in.Epoch}
	if in.Token != "" {
		lease.Owner = in.Owner
		lease.Writer = in.Token
	}
	ctx := r.Context()
	var result any
	var err error
	switch op {
	case "state":
		var s documents.State
		s, err = h.store.Read(ctx, k, auth)
		result = documentStateWire(s)
	case "initialize":
		var s documents.State
		s, err = h.store.InitRequest(ctx, k, in.ID, in.Fingerprint, in.Schema, in.Snapshot, in.Blocks, auth)
		result = documentStateWire(s)
	case "lease":
		var l documents.Lease
		l, err = h.store.Acquire(ctx, k, in.Owner, auth)
		result = map[string]any{"token": l.Writer, "owner": l.Owner, "epoch": l.Epoch, "expiresAt": l.ExpiresAt}
	case "renew":
		var l documents.Lease
		l, err = h.store.Renew(ctx, k, lease, auth)
		result = map[string]any{"token": l.Writer, "owner": l.Owner, "epoch": l.Epoch, "expiresAt": l.ExpiresAt}
	case "release":
		err = h.store.Release(ctx, k, lease, auth)
		result = map[string]bool{"released": err == nil}
	case "receipt":
		var receipt documents.Receipt
		var found bool
		receipt, found, err = h.store.LookupReceipt(ctx, k, in.ID, in.Fingerprint, auth)
		v := documentReceiptWire(receipt)
		v["found"] = found
		result = v
	case "commit":
		if in.Kind != "human" && in.Kind != "agent" {
			replyError(w, 400, "unsupported commit kind")
			return
		}
		if artifactAgentID(r) != "" && in.Kind != "agent" {
			replyError(w, 403, "semantic agent operation required")
			return
		}
		if in.Kind == "agent" && len(in.IfHashes) == 0 {
			replyError(w, 400, "semantic target hashes required")
			return
		}
		var receipt documents.Receipt
		receipt, err = h.store.Commit(ctx, k, documents.Commit{Lease: lease, ID: in.ID, Fingerprint: in.Fingerprint, IfRev: in.IfRev, Schema: in.Schema, Update: in.Update, CandidateSnapshot: in.Snapshot, Projection: in.Blocks, IfHashes: in.IfHashes, Kind: in.Kind}, auth)
		result = documentReceiptWire(receipt)
	case "checkpoint":
		var c documents.Checkpoint
		c, err = h.store.CheckpointRequest(ctx, k, lease, in.ID, in.Fingerprint, in.Snapshot, in.IfRev, auth)
		result = map[string]any{"id": c.ID, "rev": c.Rev, "schema": c.Schema, "checkpoint": c.ID, "epoch": lease.Epoch}
	case "checkpoint-read":
		var c documents.Checkpoint
		c, err = h.store.CheckpointRead(ctx, k, in.ID, auth)
		result = map[string]any{"id": c.ID, "rev": c.Rev, "schema": c.Schema, "snapshot": c.Snapshot, "blocks": c.Projection}
	case "restore":
		var receipt documents.Receipt
		receipt, err = h.store.RestoreRequestWithFingerprint(ctx, k, lease, in.Checkpoint, in.ID, in.Fingerprint, in.IfRev, auth)
		result = documentReceiptWire(receipt)
	default:
		replyError(w, 404, "unknown Docs authority operation")
		return
	}
	if err != nil {
		documentAuthorityError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
