package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// A pending task exists only in host memory. StartTask binds it to a newly
// accepted host-generated work ID before a future trusted dispatcher attaches
// its confirmed attempt. It is not a serialized run origin, an agent bearer,
// or a participant capability. No route or dispatcher uses these dormant types.
type collaborationAgentTaskBinding struct {
	authorVector       collaborationAuthorVector
	key                mooring.SessionKey
	intent             string
	allowPartial       bool
	work               collaborationAgentWorkBinding
	messageID          string
	originalHuman      chataudience.Receipt
	originalCredential auth.OriginalCredential
	permissionVersion  string
	memberID           string
	memberRevision     int64
	ingress            collaborationIngressStamp
}

// runID is the canonical work_attempts primary key. Neither the generic run
// journal nor a payload-provided run/agent name can replace this exact tuple.
// Renewable heartbeat/lease expiry is checked at each use, not captured here.
// Recovery that changes this identity requires a fresh actual human mandate.
type collaborationAgentAttemptBinding struct {
	collaborationAgentWorkBinding
	runID          string
	attempt        int64
	generation     int64
	startedAt      string
	leaseOwner     string
	runtimeLocator string
}

type collaborationAgentWorkBinding struct {
	workID          string
	workspaceID     string
	agentID         string
	crewID          string
	humanUserID     string
	chatID          string
	source          string
	sourceRef       string
	domainKind      string
	domainID        string
	authorizedScope string
	inputSHA256     string
	targetRevision  string
}

// An attached mandate must be an entry in the current registry incarnation.
// Even a fully restored SQL principal/attempt and valid old agtv2 cannot
// recreate its original human consent. All fields stay private and are copied.
type collaborationAgentMandateBinding struct {
	task             collaborationAgentTaskBinding
	attempt          collaborationAgentAttemptBinding
	credentialSHA256 [32]byte
	expiresAt        time.Time
}

// Pending handles stay in the trusted host producer, never serialized in work
// input or delivered to an agent. Only AttachConfirmedAttempt consumes them.
type collaborationAgentPending struct {
	store *collaborationAgentMandateStore
	id    string
}

type collaborationAgentWorkSeal struct{ marker byte }
type collaborationAgentFreshWork struct {
	seal         *collaborationAgentWorkSeal
	workID       string
	boot         string
	expiresTick  int64
	allowPartial bool
}

type collaborationAgentPendingEntry struct {
	task        collaborationAgentTaskBinding
	boot        string
	expiresTick int64
}

type collaborationAgentMandateEntry struct {
	binding     collaborationAgentMandateBinding
	boot        string
	expiresTick int64
}

// Verified identity for the future host participant adapter. The actor is
// derived from these authenticated fields, never a payload or projected user.
// Private registry/reference fields prevent JSON from creating a host proof.
type collaborationAgentPageAccess struct {
	authorVector              collaborationAuthorVector
	currentDeliveryPermission string
	collaborationHumanPageAccess
	agentID, crewID, runID, workID string
	attempt, generation            int64
	store                          *collaborationAgentMandateStore
	mandateRef                     string
	credentialBinding              [32]byte
	intent                         string
	allowPartial                   bool
}

// This default-off registry is deliberately absent from PageHandler routes.
// A new instance cannot recover pending consent or attached mandates from SQL.
// A future in-process restore must retire it and all associated delivery gates
// before restoring; merely rechecking restored SQL is not an incarnation fence.
type collaborationAgentMandateStore struct {
	mu          sync.Mutex
	pages       *PageHandler
	enabled     bool
	clock       mooring.OwnerClock
	fence       *collaborationIngressFence
	workSeal    *collaborationAgentWorkSeal
	ttl         time.Duration
	capacity    int
	incarnation string
	pending     map[string]collaborationAgentPendingEntry
	attached    map[string]collaborationAgentMandateEntry
}

func newCollaborationAgentMandateStore(h *PageHandler, enabled bool, fence *collaborationIngressFence, clock mooring.OwnerClock, ttl time.Duration, capacity int) (*collaborationAgentMandateStore, error) {
	s := &collaborationAgentMandateStore{pages: h, enabled: enabled, fence: fence, clock: clock, ttl: ttl, capacity: capacity}
	if !enabled {
		return s, nil
	}
	if h == nil || h.db == nil || h.artifactMasterToken == "" || fence == nil || clock == nil || ttl <= 0 || ttl > time.Hour || capacity < 1 || capacity > 5000 {
		return nil, errCollaborationAgentAccess
	}
	incarnation, e := collaborationAgentRandomID()
	if e != nil {
		return nil, e
	}
	s.incarnation = incarnation
	s.workSeal = &collaborationAgentWorkSeal{marker: 1}
	s.pending = make(map[string]collaborationAgentPendingEntry)
	s.attached = make(map[string]collaborationAgentMandateEntry)
	return s, nil
}

func collaborationAgentRandomID() (string, error) {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *collaborationAgentMandateStore) now() (string, int64, error) {
	if s == nil || !s.enabled || s.clock == nil || s.incarnation == "" {
		return "", 0, errCollaborationAgentAccess
	}
	boot, tick, e := s.clock.Now()
	if e != nil || !mooring.ValidWireIdentifier(boot) || tick < 0 || int64(tick) > math.MaxInt64-int64(s.ttl) {
		return "", 0, errCollaborationAgentAccess
	}
	return boot, int64(tick), nil
}

func (s *collaborationAgentMandateStore) pruneLocked(boot string, tick int64) {
	for id, e := range s.pending {
		if e.boot != boot || e.expiresTick <= tick {
			delete(s.pending, id)
		}
	}
	for id, e := range s.attached {
		if e.boot != boot || e.expiresTick <= tick {
			delete(s.attached, id)
		}
	}
}

// Only StartTask can supply the private seal for its newly inserted work row.
// A body-provided or restored queued workID cannot enter through this helper.
// The caller holds its full-operation ingress lease through commit/response.
func (s *collaborationAgentMandateStore) prepareAcceptedTask(ctx context.Context, tx *sql.Tx, key mooring.SessionKey, intent, message string, receipt *chataudience.Receipt, fresh collaborationAgentFreshWork) (*collaborationAgentPending, error) {
	if s == nil {
		return nil, errCollaborationAgentAccess
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	boot, tick, e := s.now()
	if e != nil || tx == nil || receipt == nil {
		return nil, errCollaborationAgentAccess
	}
	if fresh.seal == nil || fresh.seal != s.workSeal || fresh.boot != boot || tick >= fresh.expiresTick {
		return nil, errCollaborationAgentAccess
	}
	workID := fresh.workID
	stamp, e := s.fence.capture(ctx)
	if e != nil {
		return nil, e
	}
	release, e := s.fence.acquire(ctx, stamp)
	if e != nil {
		return nil, e
	}
	defer release() // Callback check only; caller holds its full-operation lease.
	write, valid := collaborationAgentIntent(intent)
	if !valid || fresh.allowPartial && !write {
		return nil, errCollaborationAgentAccess
	}
	source := auth.OriginalCredentialFromContext(ctx)
	if source == nil || receipt.OriginalCredential() != *source || receipt.WorkspaceID != key.WorkspaceID || receipt.AgentID == "" || !mooring.ValidWireIdentifier(workID) || !mooring.ValidWireIdentifier(message) {
		return nil, errCollaborationAgentAccess
	}
	current, e := chataudience.RevalidateOriginalUserQuery(ctx, tx, receipt)
	if e != nil || current == nil || *current != *receipt {
		return nil, errCollaborationAgentAccess
	}
	proof, e := s.pages.authorizeCollaborationHumanPage(ctx, tx, key, intent)
	if e != nil || proof.record.PrivateAudience {
		return nil, errCollaborationAgentAccess
	}
	var w collaborationAgentWorkBinding
	var state, class string
	var generation, attempts int64
	e = tx.QueryRowContext(ctx, `SELECT id,workspace_id,agent_id,crew_id,authorized_by_user_id,session_id,source,source_ref,domain_kind,domain_id,authorized_scope,input_sha256,target_revision,state,class,generation,attempts FROM work_items WHERE id=?`, workID).Scan(&w.workID, &w.workspaceID, &w.agentID, &w.crewID, &w.humanUserID, &w.chatID, &w.source, &w.sourceRef, &w.domainKind, &w.domainID, &w.authorizedScope, &w.inputSHA256, &w.targetRevision, &state, &class, &generation, &attempts)
	scope := "pages:read"
	if write {
		scope = "pages:write"
	}
	if e != nil || state != "queued" || class != "chat" || generation != 0 || attempts != 0 || w.workspaceID != key.WorkspaceID || w.agentID != receipt.AgentID || w.humanUserID != receipt.UserID || w.chatID != receipt.ChatID || w.source != "chat" || w.sourceRef != message || w.domainKind != "mooring_wordcraft_task" || w.domainID != key.ArtifactID || w.authorizedScope != scope {
		return nil, errCollaborationAgentAccess
	}
	var live, messageMatches bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents a JOIN crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id AND c.deleted_at IS NULL WHERE a.id=? AND a.workspace_id=? AND a.crew_id=? AND a.deleted_at IS NULL AND a.expired_at IS NULL),EXISTS(SELECT 1 FROM conversation_messages WHERE id=? AND session_id=? AND agent_id=? AND author_user_id=? AND role='user')`, w.agentID, w.workspaceID, w.crewID, message, w.chatID, w.agentID, w.humanUserID).Scan(&live, &messageMatches)
	pageCtx := context.WithValue(ctx, pageAuthReaderKey{}, tx)
	if e != nil || !live || !messageMatches || !s.pages.artifactAgentPolicyAllows(pageCtx, w.workspaceID, w.crewID) || !collaborationAgentGrantAllows(pageCtx, s.pages, key.WorkspaceID, proof.record, w.agentID, write) {
		return nil, errCollaborationAgentAccess
	}
	vector, e := captureCollaborationAuthorVector(ctx, tx, key, source.UserID, w.agentID, w.crewID)
	if e != nil {
		return nil, e
	}
	s.pruneLocked(boot, tick)
	if len(s.pending)+len(s.attached) >= s.capacity {
		return nil, errCollaborationAgentAccess
	}
	id, e := collaborationAgentRandomID()
	if e != nil {
		return nil, e
	}
	// Re-read the trusted clock after SQL work; queue/DB latency cannot add TTL.
	bootNow, tickNow, e := s.now()
	if e != nil || bootNow != boot {
		return nil, errCollaborationAgentAccess
	}
	expiry := fresh.expiresTick
	if tickNow >= expiry {
		return nil, errCollaborationAgentAccess
	}
	task := collaborationAgentTaskBinding{authorVector: vector, key: key, intent: intent, allowPartial: fresh.allowPartial, work: w, messageID: message, originalHuman: *receipt, originalCredential: *source, permissionVersion: proof.permissionVersion, memberID: proof.memberID, memberRevision: proof.memberRevision, ingress: stamp}
	s.pending[id] = collaborationAgentPendingEntry{task: task, boot: boot, expiresTick: expiry}
	return &collaborationAgentPending{store: s, id: id}, nil
}

// AttachConfirmedAttempt is only for the actual trusted host dispatcher result,
// never a run-bearer endpoint. It consumes the original pending human consent
// once; an old origin/agtv2 cannot manufacture a pending handle after restart.
func (s *collaborationAgentMandateStore) AttachConfirmedAttempt(ctx context.Context, tx *sql.Tx, pending *collaborationAgentPending, runID string) (string, error) {
	if s == nil {
		return "", errCollaborationAgentAccess
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	boot, tick, e := s.now()
	if e != nil || tx == nil || pending == nil || pending.store != s {
		return "", errCollaborationAgentAccess
	}
	s.pruneLocked(boot, tick)
	entry, ok := s.pending[pending.id]
	if !ok {
		return "", errCollaborationAgentAccess
	}
	release, e := s.fence.acquireOriginal(entry.task.ingress)
	if e != nil {
		return "", e
	}
	defer release() // Caller retains original ingress lease through SQL commit.
	a, _, e := loadCollaborationAgentAttempt(ctx, tx, runID)
	if e != nil || a.collaborationAgentWorkBinding != entry.task.work {
		return "", errCollaborationAgentAccess
	}
	proof, principalExpiry, e := s.checkTaskAuthority(ctx, tx, entry.task, a)
	if e != nil {
		return "", errCollaborationAgentAccess
	}
	expected := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(s.pages.artifactMasterToken, a.workspaceID, a.crewID), a.workspaceID, a.agentID, a.runID)
	if expected == "" {
		return "", errCollaborationAgentAccess
	}
	id, e := collaborationAgentRandomID()
	if e != nil {
		return "", e
	}
	bootNow, tickNow, e := s.now()
	if e != nil || bootNow != boot || tickNow >= entry.expiresTick {
		return "", errCollaborationAgentAccess
	}
	horizon := collaborationAgentEarlier(proof.credentialExpiry, principalExpiry)
	if !horizon.IsZero() && !time.Now().Before(horizon) {
		return "", errCollaborationAgentAccess
	}
	binding := collaborationAgentMandateBinding{task: entry.task, attempt: a, credentialSHA256: sha256.Sum256([]byte(expected)), expiresAt: horizon}
	// Instance namespace is opaque; even an accidental random-ID collision
	// cannot make an old instance's mandate name a new registry's record.
	handle := s.incarnation + id
	s.attached[handle] = collaborationAgentMandateEntry{binding: binding, boot: boot, expiresTick: entry.expiresTick}
	delete(s.pending, pending.id)
	return handle, nil
}

// Check is a dormant authority primitive, not a delivery gate or send permit.
// Call only in the protected store's acquired SQLite writer. No caller user,
// role, payload agent ID or old projected artifact principal is trusted here.
func (s *collaborationAgentMandateStore) Check(r *http.Request, tx *sql.Tx, handle string, key mooring.SessionKey, intent string) (collaborationAgentPageAccess, error) {
	var result collaborationAgentPageAccess
	if s == nil || r == nil {
		return result, errCollaborationAgentAccess
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	boot, tick, e := s.now()
	if e != nil || tx == nil || len(handle) != 128 {
		return result, errCollaborationAgentAccess
	}
	s.pruneLocked(boot, tick)
	entry, ok := s.attached[handle]
	if !ok || entry.binding.task.key != key {
		return result, errCollaborationAgentAccess
	}
	release, e := s.fence.acquire(r.Context(), entry.binding.task.ingress)
	if e != nil {
		return result, e
	}
	defer release() // The enclosing response/transaction needs its own lease.
	write, valid := collaborationAgentIntent(intent)
	allowedWrite, _ := collaborationAgentIntent(entry.binding.task.intent)
	if !valid || write && !allowedWrite {
		return result, errCollaborationAgentAccess
	}
	ctx := r.Context()
	if UserFromContext(ctx) != nil || AuthKindFromContext(ctx) != "" || auth.OriginalCredentialFromContext(ctx) != nil || projectAgentFrom(ctx) != nil || ctx.Value(artifactRunKey{}) != nil {
		return result, errCollaborationAgentAccess
	}
	a := entry.binding.attempt
	if InternalTokenWorkspaceFromContext(ctx) != a.workspaceID || InternalTokenCrewFromContext(ctx) != a.crewID {
		return result, errCollaborationAgentAccess
	}
	raw := r.Header.Get("Authorization")
	if len(raw) > 2048 {
		return result, errCollaborationAgentAccess
	}
	fields := strings.Fields(raw)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || sha256.Sum256([]byte(fields[1])) != entry.binding.credentialSHA256 {
		return result, errCollaborationAgentAccess
	}
	ws, agent, run, valid := internaltoken.ValidateAgentRunToken(internaltoken.DeriveAgentRunKey(s.pages.artifactMasterToken, a.workspaceID, a.crewID), fields[1])
	if !valid || ws != a.workspaceID || agent != a.agentID || run != a.runID {
		return result, errCollaborationAgentAccess
	}
	current, leaseExpiry, e := loadCollaborationAgentAttempt(ctx, tx, run)
	if e != nil || current != a {
		return result, errCollaborationAgentAccess
	}
	page, principalExpiry, e := s.checkTaskAuthority(ctx, tx, entry.binding.task, a)
	if e != nil {
		return result, e
	}
	bootNow, tickNow, e := s.now()
	if e != nil || bootNow != boot || tickNow >= entry.expiresTick {
		return result, errCollaborationAgentAccess
	}
	page.credentialExpiry = collaborationAgentEarlier(page.credentialExpiry, entry.binding.expiresAt)
	page.credentialExpiry = collaborationAgentEarlier(page.credentialExpiry, principalExpiry)
	page.credentialExpiry = collaborationAgentEarlier(page.credentialExpiry, leaseExpiry)
	page.credentialExpiry = collaborationAgentEarlier(page.credentialExpiry, time.Now().Add(time.Duration(entry.expiresTick-tickNow)))
	if !time.Now().Before(page.credentialExpiry) {
		return result, errCollaborationAgentAccess
	}
	deliveryPermission := page.permissionVersion
	page.permissionVersion = entry.binding.task.permissionVersion // immutable original metadata
	result = collaborationAgentPageAccess{authorVector: entry.binding.task.authorVector, currentDeliveryPermission: deliveryPermission, collaborationHumanPageAccess: page, agentID: a.agentID, crewID: a.crewID, runID: a.runID, workID: a.workID, attempt: a.attempt, generation: a.generation, store: s, mandateRef: handle, credentialBinding: entry.binding.credentialSHA256, intent: intent, allowPartial: entry.binding.task.allowPartial}
	return result, nil
}

func collaborationAgentEarlier(a, b time.Time) time.Time {
	if a.IsZero() || !b.IsZero() && b.Before(a) {
		return b
	}
	return a
}
