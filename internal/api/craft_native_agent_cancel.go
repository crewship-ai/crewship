package api

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/work"
)

type craftNativeAgentCancelKey struct{}
type craftNativeAgentCancelProof struct {
	manager     *craftNativeAgentManager
	entry       *craftNativeAgentEntry
	source      auth.OriginalCredential
	tx          *sql.Tx
	lease       mooring.EditLease
	by          mooring.EditLeasePrincipal
	sawOriginal bool
}

func (m *craftNativeAgentManager) holderEntry(l mooring.EditLease) *craftNativeAgentEntry {
	if !strings.HasPrefix(l.Holder.ActorID, "agent:") || !strings.HasPrefix(l.Holder.SessionID, "native-agent:") {
		return nil
	}
	run := strings.TrimPrefix(l.Holder.SessionID, "native-agent:")
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.runs[run]
	if e == nil || e.grant.Scope != l.Scope || craftNativeAgentPrincipal(e) != l.Holder {
		return nil
	}
	return e
}

func (m *craftNativeAgentManager) authorizeCancel(ctx context.Context, tx *sql.Tx, a mooring.EditLeaseAccess) error {
	p, _ := ctx.Value(craftNativeAgentCancelKey{}).(*craftNativeAgentCancelProof)
	if p == nil || p.manager != m || p.tx != tx || p.entry == nil || a.Intent != "revoke" || a.Scope != p.lease.Scope || a.Principal != p.by || p.source.UserID != p.entry.grant.Source.UserID {
		return errCraftNativeAgentDenied
	}
	source, err := craftNativeHuman(ctx)
	if err != nil || source != p.source {
		return errCraftNativeAgentDenied
	}
	if _, err = m.human.pageAuthority(ctx, tx, source, a.Scope, true); err != nil {
		return err
	}
	m.mu.Lock()
	registered := m.entries[p.entry.workID] == p.entry && m.runs[p.entry.attempt.runID] == p.entry && p.entry.seal == m.seal
	m.mu.Unlock()
	if !registered {
		return errCraftNativeAgentDenied
	}
	var actor, session string
	var generation int64
	if err = tx.QueryRowContext(ctx, `SELECT actor_id,session_id,generation FROM mooring_edit_leases WHERE page_id=? AND workspace_id=?`, a.Scope.ArtifactID, a.Scope.WorkspaceID).Scan(&actor, &session, &generation); err != nil {
		return err
	}
	if generation != p.lease.Generation {
		return errCraftNativeAgentDenied
	}
	if actor == p.lease.Holder.ActorID && session == p.lease.Holder.SessionID {
		p.sawOriginal = true
		return nil
	}
	// Only this same caller-owned writer's second authorization may acknowledge
	// the empty row produced by Core.Revoke; a free/human row never starts it.
	if p.sawOriginal && actor == "" && session == "" {
		return nil
	}
	return errCraftNativeAgentDenied
}

func (m *craftNativeAgentManager) cancelTask(w http.ResponseWriter, r *http.Request) {
	if m == nil || !m.runtimeReady() {
		replyError(w, 503, "native Craft agents unavailable")
		return
	}
	source, err := craftNativeHuman(r.Context())
	if err != nil {
		replyError(w, 403, "native agent stop denied")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	canonical, canonicalErr := mooring.CanonicalJSON(raw)
	if err != nil || canonicalErr != nil || string(canonical) != "{}" {
		replyError(w, 400, "native agent stop requires an empty object")
		return
	}
	tx, err := m.writer(r.Context())
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	rec, err := m.human.pages.loadPageForRead(ctx, WorkspaceIDFromContext(ctx), r.PathValue("slug"))
	if err != nil {
		replyError(w, 403, "native agent stop denied")
		return
	}
	scope := mooring.EditLeaseScope{WorkspaceID: WorkspaceIDFromContext(ctx), ArtifactID: rec.ID}
	if _, err = m.human.pageAuthority(ctx, tx, source, scope, true); err != nil {
		replyError(w, 403, "native agent stop denied")
		return
	}
	l := mooring.EditLease{Scope: scope}
	err = tx.QueryRowContext(ctx, `SELECT actor_id,session_id,generation FROM mooring_edit_leases WHERE workspace_id=? AND page_id=?`, scope.WorkspaceID, scope.ArtifactID).Scan(&l.Holder.ActorID, &l.Holder.SessionID, &l.Generation)
	if err != nil {
		replyError(w, 409, "no registered native agent holds this page")
		return
	}
	e := m.holderEntry(l)
	if e == nil || source.UserID != e.grant.Source.UserID {
		replyError(w, 403, "only the original human may stop this native agent")
		return
	}
	proof := &craftNativeAgentCancelProof{manager: m, entry: e, source: source, tx: tx, lease: l, by: mooring.EditLeasePrincipal{ActorID: "user:" + source.UserID, SessionID: "native-agent-stop:" + e.workID}}
	ctx = context.WithValue(ctx, craftNativeAgentCancelKey{}, proof)
	if err = m.human.leases.Revoke(ctx, tx, scope, proof.by, l.Generation); err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	// Withdraw under the held SQL writer before COMMIT releases it. A failed
	// commit also leaves the old grant closed rather than reviving consent.
	m.mu.Lock()
	e.closed = true
	rt := m.runtime
	m.mu.Unlock()
	if err = tx.Commit(); err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	// Do not wait behind a long engine conversion's callMu: the durable lease
	// fence already makes its later publication fail; closed never reopens.
	_, cancelErr := work.NewStore(m.human.pages.db).RequestCancel(r.Context(), e.workID, "user:"+source.UserID, "original human stopped native Craft agent")
	stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer stopCancel()
	stopped, stopErr := rt.Stop(stopCtx, "agent-run:"+e.attempt.runID)
	status := http.StatusAccepted
	if stopped && stopErr == nil && cancelErr == nil {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]bool{"grant_revoked": true, "lease_retired": true, "cancel_requested": cancelErr == nil, "stopped": stopped && stopErr == nil, "stop_pending": !stopped || stopErr != nil})
}

func (m *craftNativeAgentManager) canStop(ctx context.Context, human *craftNativeSession, l mooring.EditLease) bool {
	if m == nil || !m.runtimeReady() {
		return false
	}
	e := m.holderEntry(l)
	if e == nil || human == nil || e.grant.Source.UserID != human.source.UserID {
		return false
	}
	tx, err := m.human.pages.db.BeginTx(ctx, nil)
	if err != nil {
		return false
	}
	defer tx.Rollback()
	_, err = m.human.checkEntry(ctx, tx, human, true)
	return err == nil
}
