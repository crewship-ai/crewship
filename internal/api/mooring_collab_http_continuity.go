package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// Reauthorization only refreshes an existing, still-live original admission.
// It never creates a connection, revives a retired gate, submits a write or
// reports Saved. Normal Crewship auth, ingress and page RBAC remain mandatory.
func (m *collaborationHTTPManager) reauthorize(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), collaborationHTTPSessionTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	if controller.SetWriteDeadline(deadline) != nil {
		replyError(w, 503, "bounded Mooring response unavailable")
		return
	}
	defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	if controller.SetReadDeadline(deadline) != nil || !collaborationSessionCanFlush(w) {
		replyError(w, 503, "bounded Mooring request unavailable")
		return
	}
	stamp, e := m.fence.capture(ctx)
	if e != nil {
		replyError(w, 403, "Mooring access denied")
		return
	}
	release, e := m.fence.acquire(ctx, stamp)
	if e != nil {
		replyError(w, 403, "Mooring access denied")
		return
	}
	defer release()
	defer func() { _ = controller.Flush() }()
	key, rec, e := m.scope(r)
	if e != nil {
		replyError(w, 403, "Mooring access denied")
		return
	}
	if !m.pages.auditPrivateOwnerAccess(w, r, rec) {
		return
	}
	// Normal browsers send Origin for this POST. No cross-origin credential use.
	expected := m.config.PublicOrigin
	if expected == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		expected = scheme + "://" + r.Host
	}
	if r.Header.Get("Origin") != expected {
		replyError(w, 403, "Mooring origin denied")
		return
	}
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if e != nil {
		replyError(w, 413, "Mooring request too large")
		return
	}
	canonical, e := mooring.CanonicalJSON(raw)
	if e != nil {
		replyError(w, 400, "invalid Mooring request")
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(canonical, &fields) != nil || len(fields) != 1 || fields["connection_ref"] == nil {
		replyError(w, 400, "invalid Mooring request")
		return
	}
	var input struct {
		ConnectionRef string `json:"connection_ref"`
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if len(canonical) == 0 || canonical[0] != '{' || decoder.Decode(&input) != nil || !mooring.ValidWireIdentifier(input.ConnectionRef) {
		replyError(w, 400, "invalid Mooring request")
		return
	}
	m.mu.Lock()
	d := m.docs[key]
	m.mu.Unlock()
	if d == nil {
		replyError(w, 403, "Mooring access denied")
		return
	}
	d.mu.Lock()
	c := d.connections[input.ConnectionRef]
	closed := d.closed
	d.mu.Unlock()
	if c == nil || closed {
		replyError(w, 403, "Mooring access denied")
		return
	}
	// Capture the exact live binding briefly; never wait for SQL or network
	// under connection locks, because delivery still has its unchanged 5s lease.
	c.deliveryMu.Lock()
	c.mu.Lock()
	binding, gate := c.participantBinding, c.joined.gate
	valid := c.used && !c.closed && c.joinedDelivered && binding != nil && c.transition == nil && c.auditedOwner == rec.OwnerUserID && d.ctx.Err() == nil
	c.mu.Unlock()
	c.deliveryMu.Unlock()
	if !valid {
		replyError(w, 403, "Mooring access denied")
		return
	}
	var gateExpiry time.Duration
	transport := collaborationHumanContinuityTransport{
		checkSQL: func(ctx context.Context, tx *sql.Tx) error {
			var boot string
			var expires int64
			var closed, blocked bool
			err := tx.QueryRowContext(ctx, `SELECT boot_id,expires_ns,closed,blocked FROM mooring_delivery_gates WHERE id=? AND page_id=? AND workspace_id=? AND application_id=? AND participant_ref=? AND owner_id=? AND owner_generation=? AND permission_version=?`, gate.GateID, key.ArtifactID, key.WorkspaceID, key.ApplicationID, c.admission.participant, d.owner.OwnerID, d.owner.Generation, gate.PermissionVersion).Scan(&boot, &expires, &closed, &blocked)
			currentBoot, now, clockErr := m.clock.Now()
			if err != nil || clockErr != nil || closed || blocked || boot != currentBoot || now < 0 || int64(now) >= expires {
				return errCollaborationAdmission
			}
			gateExpiry = time.Duration(expires)
			return nil
		},
		publish: func(publish func() error) error {
			c.deliveryMu.Lock()
			defer c.deliveryMu.Unlock()
			c.mu.Lock()
			defer c.mu.Unlock()
			_, now, err := m.clock.Now()
			if err != nil || now < 0 || now >= gateExpiry || c.closed || !c.joinedDelivered || c.participantBinding != binding || c.joined.gate.GateID != gate.GateID || c.transition != nil || d.ctx.Err() != nil {
				return errCollaborationAdmission
			}
			return publish()
		},
	}
	r = r.WithContext(context.WithValue(r.Context(), collaborationHumanContinuityTransportKey{}, transport))
	responded := false
	e = d.humans.runHumanOperation(r, c.admission.participant, func(bound context.Context) error {
		publish, err := d.humans.prepareHumanContinuity(bound, d.owner)
		if err != nil {
			return err
		}
		if err = publish(); err != nil {
			return err
		}
		if bound.Err() != nil {
			return bound.Err()
		}
		w.Header().Set("Cache-Control", "no-store")
		responded = true
		w.WriteHeader(http.StatusNoContent)
		return controller.Flush()
	})
	if e != nil && !responded {
		replyError(w, 403, "Mooring access denied")
	}
}
