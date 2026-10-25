package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
	protocolschema "github.com/crewship-ai/crewship/mooring/protocol"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"golang.org/x/net/websocket"
)

// MooringHTTPConfig is trusted startup configuration. Production wiring must
// supply verified embedded assets; requests cannot choose a binary or profile.
type MooringHTTPConfig struct {
	Enabled              bool
	Binary, BinarySHA256 string
	PublicOrigin         string
	SDKPrefix            string
	DeliveryTTL          time.Duration
	// The trusted loaded-bundle provider owns digest/URL verification and the
	// per-request editor-origin gate. Requests cannot nominate this metadata.
	EditorBundle func(*http.Request) (digest, indexURL string)
}

func WithMooringHTTP(c MooringHTTPConfig) RouterOption {
	return func(r *Router) { r.mooringHTTPConfig = c }
}

const collaborationHTTPFrameLimit = 256 << 10
const collaborationHTTPDocLimit = 16
const collaborationHTTPParticipantLimit = 128

var errCollaborationHTTP = errors.New("Mooring session unavailable")

type collaborationHTTPManager struct {
	workers     sync.WaitGroup
	stopped     chan struct{}
	mu          sync.Mutex
	pages       *PageHandler
	config      MooringHTTPConfig
	fence       *collaborationIngressFence
	clock       mooring.OwnerClock
	owners      *mooring.OwnerStore
	schema      *mooring.WireSchema
	ctx         context.Context
	cancel      context.CancelFunc
	closed      bool
	docs        map[mooring.SessionKey]*collaborationHTTPDocument
	usedOrigins map[mooring.SessionKey]map[uint64]struct{}

	agentMandates *collaborationAgentMandateStore
}
type collaborationHTTPDocument struct {
	manager     *collaborationHTTPManager
	key         mooring.SessionKey
	ready       chan struct{}
	err         error
	ctx         context.Context
	cancel      context.CancelFunc
	cell        *collaborationSessionStore
	owner       mooring.OwnerLease
	activation  *mooring.SessionActivation
	humans      *collaborationHumanRegistry
	submissions *collaborationSubmissionRegistry
	committer   *collaborationSessionCommitter
	pump        *collaborationSessionPump
	originals   *collaborationLiveOriginals
	mu          sync.Mutex
	closed      bool
	connections map[string]*collaborationHTTPConnection
}
type collaborationHTTPConnection struct {
	document                 *collaborationHTTPDocument
	request                  *http.Request
	admission                collaborationHumanAdmission
	ticket, ref, origin      string
	mu                       sync.Mutex
	joined                   collaborationHumanJoined
	socket                   *websocket.Conn
	used, closed             bool
	auditedOwner             string
	joinRequest, openRequest protocol.Identifier
	joinedDelivered          bool
	deliveryMu               sync.Mutex
	participantPending       *mooring.SessionParticipantPending
	participantBinding       *mooring.SessionParticipantBinding
	transition               *collaborationGateTransition
	retirements              map[string]*collaborationRetiredGate
}

func newCollaborationHTTPManager(pages *PageHandler, c MooringHTTPConfig) (*collaborationHTTPManager, error) {
	if !c.Enabled || pages == nil || pages.db == nil || c.Binary == "" || len(c.BinarySHA256) != 64 {
		return nil, errCollaborationHTTP
	}
	if c.DeliveryTTL == 0 {
		c.DeliveryTTL = 5 * time.Second
	}
	if c.DeliveryTTL < time.Millisecond || c.DeliveryTTL > 5*time.Second {
		return nil, errCollaborationHTTP
	}
	if c.SDKPrefix != "" {
		const prefix = "/mooring-sdk/generated/"
		if !strings.HasPrefix(c.SDKPrefix, prefix) || len(c.SDKPrefix) != len(prefix)+64 {
			return nil, errCollaborationHTTP
		}
		for _, r := range c.SDKPrefix[len(prefix):] {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return nil, errCollaborationHTTP
			}
		}
	}
	if c.PublicOrigin != "" {
		u, e := url.Parse(c.PublicOrigin)
		if e != nil || u.User != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, errCollaborationHTTP
		}
	}
	f, e := os.Open(c.Binary)
	if e != nil {
		return nil, errCollaborationHTTP
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Size() < 1 || st.Size() > 64<<20 {
		return nil, errCollaborationHTTP
	}
	h := sha256.New()
	if _, e = io.Copy(h, io.LimitReader(f, (64<<20)+1)); e != nil || hex.EncodeToString(h.Sum(nil)) != c.BinarySHA256 {
		return nil, errCollaborationHTTP
	}
	fence, e := newCollaborationIngressFence()
	if e != nil {
		return nil, e
	}
	clock, e := mooring.NewBootOwnerClock()
	if e != nil {
		return nil, e
	}
	schema, e := mooring.NewWireSchema(protocolschema.JSON())
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &collaborationHTTPManager{pages: pages, config: c, fence: fence, clock: clock, owners: mooring.NewOwnerStore(pages.db, true, clock, time.Minute), schema: schema, ctx: ctx, cancel: cancel, docs: make(map[mooring.SessionKey]*collaborationHTTPDocument), usedOrigins: make(map[mooring.SessionKey]map[uint64]struct{}), stopped: make(chan struct{})}, nil
}
func (m *collaborationHTTPManager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		stopped := m.stopped
		m.mu.Unlock()
		<-stopped
		return
	}
	m.closed = true
	m.cancel()
	docs := make([]*collaborationHTTPDocument, 0, len(m.docs))
	for _, d := range m.docs {
		docs = append(docs, d)
	}
	m.mu.Unlock()
	for _, d := range docs {
		d.close()
	}
	m.workers.Wait()
	close(m.stopped)
}

// This is a current original-source writer check BEFORE request-body parsing.
// It grants no later commit/send lease; each protected operation checks again.
func (m *collaborationHTTPManager) scope(r *http.Request) (mooring.SessionKey, *pageRecord, error) {
	var key mooring.SessionKey
	if m == nil || r == nil {
		return key, nil, errCollaborationHTTP
	}
	source := auth.OriginalCredentialFromContext(r.Context())
	if source == nil {
		return key, nil, errCollaborationHTTP
	}
	tx, e := m.pages.db.BeginTx(r.Context(), nil)
	if e != nil {
		return key, nil, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(r.Context(), `UPDATE mooring_session_writer_fence SET seq=seq WHERE id=1`); e != nil {
		return key, nil, e
	}
	if _, e = validateOriginalHumanCredential(r.Context(), tx, *source, "pages:read"); e != nil {
		return key, nil, e
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	rec, e := m.pages.loadPageForRead(ctx, WorkspaceIDFromContext(ctx), r.PathValue("slug"))
	if e != nil {
		return key, nil, e
	}
	key = mooring.SessionKey{WorkspaceID: WorkspaceIDFromContext(ctx), ArtifactID: rec.ID, ApplicationID: "wordcraft"}
	var exists bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=?)`, rec.ID).Scan(&exists); e != nil {
		return key, nil, e
	}
	intent := "read"
	if !exists {
		intent = "initialize"
	}
	if _, e = m.pages.authorizeCollaborationHumanPage(ctx, tx, key, intent); e != nil {
		return key, nil, e
	}
	if e = tx.Commit(); e != nil {
		return key, nil, e
	}
	return key, rec, nil
}
func (m *collaborationHTTPManager) origin(key mooring.SessionKey) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", errCollaborationHTTP
	}
	used := m.usedOrigins[key]
	if used == nil {
		if len(m.usedOrigins) >= collaborationHTTPDocLimit {
			return "", errCollaborationHTTP
		}
		used = make(map[uint64]struct{})
		m.usedOrigins[key] = used
	}
	if len(used) >= 4096 {
		return "", errCollaborationHTTP
	}
	for range 8 {
		var b [8]byte
		if _, e := rand.Read(b[:]); e != nil {
			return "", e
		}
		n := binary.LittleEndian.Uint64(b[:]) & ((1 << 53) - 1)
		if n == 0 {
			continue
		}
		if _, ok := used[n]; ok {
			continue
		}
		used[n] = struct{}{}
		return jsonNumber(n), nil
	}
	return "", errCollaborationHTTP
}
func jsonNumber(n uint64) string { b, _ := json.Marshal(n); return string(b) }

// The actual emitted wire DTO is paired with the generated schema so new
// response fields cannot silently escape the response-shape inventory guard.
type collaborationHTTPSessionResponse struct {
	SessionID          string                          `json:"session_id"`
	CheckpointRevision string                          `json:"checkpoint_revision"`
	SDKPrefix          string                          `json:"sdk_prefix"`
	ParticipantRef     string                          `json:"participant_ref"`
	Ticket             string                          `json:"ticket"`
	ConnectionRef      string                          `json:"connection_ref"`
	Origin             string                          `json:"origin"`
	Versions           protocol.Versions               `json:"versions"`
	WebsocketPath      string                          `json:"websocket_path"`
	Editor             *collaborationHTTPSessionEditor `json:"editor,omitempty"`
}

type collaborationHTTPSessionEditor struct {
	URL          string `json:"url"`
	BundleDigest string `json:"bundle_digest"`
}

// Keep admission I/O within the existing bootstrap ceiling. This bounds the
// original request lifecycle; it does not extend any delivery gate or source.
const collaborationHTTPSessionTimeout = 12 * time.Second

// Match ResponseController's supported flush interfaces through trusted HTTP
// wrappers, without flushing a success header before the original checks.
func collaborationSessionCanFlush(w http.ResponseWriter) bool {
	for {
		switch v := w.(type) {
		case interface{ FlushError() error }:
			return true
		case http.Flusher:
			return true
		case interface{ Unwrap() http.ResponseWriter }:
			w = v.Unwrap()
		default:
			return false
		}
	}
}

func (m *collaborationHTTPManager) session(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), collaborationHTTPSessionTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	if err := controller.SetWriteDeadline(deadline); err != nil {
		replyError(w, 503, "bounded Mooring response unavailable")
		return
	}
	defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	if err := controller.SetReadDeadline(deadline); err != nil {
		replyError(w, 503, "bounded Mooring request unavailable")
		return
	}
	// Keep this read deadline through net/http unread-body cleanup, including
	// pre-lease denials. The server installs its own deadline for the next request.
	if !collaborationSessionCanFlush(w) || ctx.Err() != nil {
		replyError(w, 503, "bounded Mooring response unavailable")
		return
	}
	stamp, e := m.fence.capture(r.Context())
	if e != nil {
		replyError(w, 503, "Mooring session unavailable")
		return
	}
	release, e := m.fence.acquire(r.Context(), stamp)
	if e != nil {
		replyError(w, 503, "Mooring session unavailable")
		return
	}
	defer release()
	// Flush buffered success AND denial bytes before releasing original ingress.
	defer func() { _ = controller.Flush() }()
	key, rec, e := m.scope(r)
	if e != nil {
		replyError(w, 403, "Mooring access denied")
		return
	}
	if !m.pages.auditPrivateOwnerAccess(w, r, rec) {
		return
	}
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if e != nil {
		replyError(w, 413, "Mooring initialization too large")
		return
	}
	canonical, e := mooring.CanonicalJSON(raw)
	if e != nil {
		replyError(w, 400, "invalid Mooring request")
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(canonical, &fields) != nil {
		replyError(w, 400, "invalid Mooring request")
		return
	}
	for name := range fields {
		if name != "write" && name != "checkpoint_base64" {
			replyError(w, 400, "invalid Mooring request")
			return
		}
	}
	var input struct {
		Write      bool   `json:"write"`
		Checkpoint string `json:"checkpoint_base64,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if len(canonical) == 0 || canonical[0] != '{' || decoder.Decode(&input) != nil {
		replyError(w, 400, "invalid Mooring request")
		return
	}
	var checkpoint []byte
	if input.Checkpoint != "" {
		checkpoint, e = base64.StdEncoding.Strict().DecodeString(input.Checkpoint)
		if e != nil || len(checkpoint) > 1536<<10 || base64.StdEncoding.EncodeToString(checkpoint) != input.Checkpoint {
			replyError(w, 400, "invalid Mooring checkpoint")
			return
		}
	}
	d, e := m.document(r, key, checkpoint)
	if e != nil {
		replyError(w, 503, "Mooring session unavailable")
		return
	}
	ticket, admission, e := d.humans.issue(r.Context(), d.owner, input.Write)
	if e != nil {
		replyError(w, 403, "Mooring access denied")
		return
	}
	head, origin, e := d.reserveHumanOrigin(r, admission)
	if e != nil {
		replyError(w, 503, "Mooring participant capacity exhausted")
		return
	}
	ref, e := collaborationOpaqueID()
	if e != nil {
		replyError(w, 503, "Mooring session unavailable")
		return
	}
	// Retain the verified original transport provenance, not bearer material.
	original := r.Clone(context.WithoutCancel(r.Context()))
	original.Body = nil
	original.Header = r.Header.Clone()
	original.Header.Del("Authorization")
	original.Header.Del("Cookie")
	c := &collaborationHTTPConnection{document: d, request: original, admission: admission, ticket: ticket, ref: ref, origin: origin, auditedOwner: rec.OwnerUserID}
	d.mu.Lock()
	if d.closed || len(d.connections) >= collaborationHTTPParticipantLimit {
		d.mu.Unlock()
		replyError(w, 503, "Mooring participant capacity exhausted")
		return
	}
	d.connections[ref] = c
	d.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	response := collaborationHTTPSessionResponse{
		SessionID: d.activation.SessionID(), CheckpointRevision: head, SDKPrefix: m.config.SDKPrefix,
		ParticipantRef: admission.participant, Ticket: ticket, ConnectionRef: ref, Origin: origin,
		Versions: d.wireVersions(), WebsocketPath: "/api/v1/pages/@" + key.ArtifactID + "/mooring/ws?workspace_id=" + key.WorkspaceID,
	}
	if m.config.EditorBundle != nil {
		digest, indexURL := m.config.EditorBundle(r)
		if digest != "" && indexURL != "" {
			response.Editor = &collaborationHTTPSessionEditor{URL: indexURL, BundleDigest: digest}
		}
	}
	writeJSON(w, 200, response)
}
