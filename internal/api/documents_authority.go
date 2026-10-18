package api

// This authority is served ONLY on a private Unix listener. It is deliberately
// absent from the public Router: editor/CLI delivery belongs to D1b.
import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/documents"
)

type documentAuthority struct {
	pages       *PageHandler
	store       *documents.Store
	middleware  *AuthMiddleware
	secret      string
	incarnation string
}

var errDocumentAccess = errors.New("document access denied")

const documentEngineProtocol = 3

// DocumentAuthority constructs the private authority. It is never registered
// on r.mux. Boot fencing runs only after the caller owns its Unix listener.
func (r *Router) DocumentAuthority(ctx context.Context, secret string) (http.Handler, error) {
	if len(secret) < 32 {
		return nil, errors.New("Docs service secret requires at least 32 characters")
	}
	s := documents.New(r.db, r.documentStoreOptions)
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	return &documentAuthority{r.pages, s, r.authMw, secret, hex.EncodeToString(nonce[:])}, nil
}
func (h *documentAuthority) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	provided := r.Header.Get("X-Crewship-Docs-Service")
	if len(provided) != len(h.secret) || subtle.ConstantTimeCompare([]byte(provided), []byte(h.secret)) != 1 {
		replyError(w, 403, "service authentication required")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == "GET" && r.URL.Path == "/health/ready" {
		writeJSON(w, 200, map[string]int{"protocol": documentEngineProtocol, "schema": documents.LatestSchema})
		return
	}
	if r.Method == "POST" && r.URL.Path == "/internal/docs/v1/engine/recover" {
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		var input struct {
			Boot     string `json:"boot"`
			Protocol int    `json:"protocol"`
			Schema   int    `json:"schema"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
			replyError(w, 400, "bounded boot identity required")
			return
		}
		if input.Protocol != documentEngineProtocol || input.Schema != documents.LatestSchema {
			replyError(w, 409, "unsupported document engine protocol or content capability")
			return
		}
		if err := h.store.RecoverEngine(r.Context(), input.Boot); err != nil {
			documentAuthorityError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"recovered": true, "protocol": documentEngineProtocol, "schema": documents.LatestSchema})
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
	if h.authenticateDocumentDraftAgent(w, r, operation) {
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
		if rec.HasProject || rec.HasApplication {
			return "", errDocumentAccess
		}
		var panels int
		if tx.QueryRowContext(ctx, "SELECT count(*) FROM page_panels WHERE page_id=?", k.PageID).Scan(&panels) != nil || panels != 0 {
			return "", errDocumentAccess
		}
		// Current main installs private output types independently. Validate
		// every known schema and refuse any other output on this Page.
		if _, _, e := privatePageOwnershipSQL(ctx, tx, "p"); e != nil {
			return "", errDocumentAccess
		}
		for _, table := range []string{"slides_heads", "page_boards", "page_artifacts"} {
			var installed, attached bool
			if tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name=? COLLATE NOCASE)", table).Scan(&installed) != nil {
				return "", errDocumentAccess
			}
			if installed && (tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE page_id=?)", k.PageID).Scan(&attached) != nil || attached) {
				return "", errDocumentAccess
			}
		}
		if a, ok := ctx.Value(artifactRunKey{}).(*artifactRunAuthority); ok {
			if initialize {
				return "", errDocumentAccess
			}
			return h.pages.authorizeDocumentDraft(ctx, a, k, r.PathValue("operation"))
		}
		// Exact session, not merely some other active session for this user.
		var expiry string
		var revoked sql.NullString
		if IsCLIToken(extractToken(r)) {
			if validateDocumentCLITokenTx(ctx, tx, extractToken(r), user.ID, write) != nil {
				return "", errDocumentAccess
			}
		} else {
			if user.SessionID == "" || tx.QueryRowContext(ctx, `SELECT expires_at,revoked_at FROM user_sessions WHERE id=? AND user_id=?`, user.SessionID, user.ID).Scan(&expiry, &revoked) != nil || revoked.Valid {
				return "", errDocumentAccess
			}
			limit, e := time.Parse(time.RFC3339Nano, expiry)
			if e != nil || !time.Now().Before(limit) {
				return "", errDocumentAccess
			}
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
	if errors.Is(err, documents.ErrRateLimit) {
		writeJSON(w, 429, map[string]string{"error": "temporary document operation limit; wait and retry the original request", "code": "document_rate_limit"})
		return
	}
	if errors.Is(err, documents.ErrQuota) {
		writeJSON(w, 429, map[string]string{"error": "document quota exceeded; confirmed history preserved", "code": "document_quota"})
		return
	}
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
func (h *documentAuthority) documentStateWire(s documents.State) map[string]any {
	updates := make([][]byte, 0, len(s.Updates))
	for _, u := range s.Updates {
		updates = append(updates, u.Payload)
	}
	return map[string]any{"schema": s.Schema, "rev": s.HeadRev, "epoch": s.Epoch, "generation": s.Generation, "incarnation": h.incarnation, "snapshot": s.Snapshot, "snapshotRev": s.SnapshotRev, "updates": updates, "blocks": s.Projection}
}
func documentReceiptWire(r documents.Receipt) map[string]any {
	return map[string]any{"id": r.ID, "rev": r.Rev, "epoch": r.Epoch, "actor": r.Actor, "kind": r.Kind}
}
func (h *documentAuthority) operation(w http.ResponseWriter, r *http.Request) {
	k := documents.Key{WorkspaceID: WorkspaceIDFromContext(r.Context()), PageID: r.PathValue("page")}
	op := r.PathValue("operation")
	var in struct {
		Generation   *int64                 `json:"generation"`
		ID           string                 `json:"id"`
		Fingerprint  string                 `json:"fingerprint"`
		Schema       int                    `json:"schema"`
		TargetSchema int                    `json:"targetSchema"`
		Snapshot     []byte                 `json:"snapshot"`
		Update       []byte                 `json:"update"`
		Blocks       json.RawMessage        `json:"blocks"`
		Owner        string                 `json:"owner"`
		Token        string                 `json:"token"`
		LeaseOwner   string                 `json:"leaseOwner"`
		LeaseToken   string                 `json:"leaseToken"`
		Epoch        int64                  `json:"epoch"`
		IfRev        int64                  `json:"ifRev"`
		IfHashes     map[string]string      `json:"ifHashes"`
		Semantic     *documentSemanticPatch `json:"semantic"`
		Kind         string                 `json:"kind"`
		Checkpoint   string                 `json:"checkpoint"`
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
	read := op == "state" || op == "receipt" || op == "checkpoint-read" || op == "checkpoints"
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
	case "checkpoints":
		result, err = h.store.Checkpoints(ctx, k, auth)
	case "state":
		var s documents.State
		s, err = h.store.Read(ctx, k, auth)
		result = h.documentStateWire(s)
	case "initialize":
		var s documents.State
		s, err = h.store.InitRequest(ctx, k, in.ID, in.Fingerprint, in.Schema, in.Snapshot, in.Blocks, auth)
		result = h.documentStateWire(s)
	case "lease":
		var l documents.Lease
		l, err = h.store.Acquire(ctx, k, in.Owner, auth)
		result = map[string]any{"token": l.Writer, "owner": l.Owner, "epoch": l.Epoch, "expiresAt": l.ExpiresAt, "generation": l.Generation}
	case "renew":
		var l documents.Lease
		l, err = h.store.Renew(ctx, k, lease, auth)
		result = map[string]any{"token": l.Writer, "owner": l.Owner, "epoch": l.Epoch, "expiresAt": l.ExpiresAt, "generation": l.Generation}
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
	case "upgrade":
		if in.Generation == nil {
			replyError(w, 400, "original document generation required")
			return
		}
		var receipt documents.Receipt
		receipt, err = h.store.UpgradeRequest(ctx, k, lease, in.ID, in.Fingerprint, in.Schema, in.TargetSchema, in.IfRev, in.Generation, auth)
		wire := documentReceiptWire(receipt)
		wire["schema"] = documents.LatestSchema
		result = wire
	case "commit":
		if in.Generation == nil {
			replyError(w, 400, "document generation required")
			return
		}
		a, agent := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
		if (!agent && in.Kind != "human") || (agent && in.Kind != "agent") {
			replyError(w, 403, "human document commit required")
			return
		}
		var receipt documents.Receipt
		var guard documents.MutationGuard
		if agent {
			guard = documentRichSemanticGuard(in.Semantic, in.IfHashes, in.Blocks, h.pages.documentDraftGuard(a, k, "commit", in.IfRev))
		}
		receipt, err = h.store.Commit(ctx, k, documents.Commit{Lease: lease, ID: in.ID, Fingerprint: in.Fingerprint, IfRev: in.IfRev, IfGeneration: in.Generation, Schema: in.Schema, Update: in.Update, CandidateSnapshot: in.Snapshot, Projection: in.Blocks, IfHashes: in.IfHashes, Kind: in.Kind, Guard: guard}, auth)
		result = documentReceiptWire(receipt)
	case "checkpoint":
		if in.Generation == nil {
			replyError(w, 400, "document generation required")
			return
		}
		var c documents.Checkpoint
		var guard documents.MutationGuard
		if a, ok := ctx.Value(artifactRunKey{}).(*artifactRunAuthority); ok {
			guard = h.pages.documentDraftGuard(a, k, "checkpoint", in.IfRev)
		}
		c, err = h.store.CheckpointRequestGuarded(ctx, k, lease, in.ID, in.Fingerprint, in.Snapshot, in.IfRev, in.Generation, auth, guard)
		result = map[string]any{"id": c.ID, "rev": c.Rev, "schema": c.Schema, "checkpoint": c.ID, "epoch": lease.Epoch}
	case "checkpoint-read":
		var c documents.Checkpoint
		c, err = h.store.CheckpointRead(ctx, k, in.ID, auth)
		result = map[string]any{"id": c.ID, "rev": c.Rev, "schema": c.Schema, "snapshot": c.Snapshot, "blocks": c.Projection}
	case "restore":
		if in.Generation == nil {
			replyError(w, 400, "document generation required")
			return
		}
		var receipt documents.Receipt
		receipt, err = h.store.RestoreRequestWithFingerprintAtGeneration(ctx, k, lease, in.Checkpoint, in.ID, in.Fingerprint, in.IfRev, in.Generation, auth)
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
