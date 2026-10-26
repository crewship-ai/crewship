package api

import (
	"context"
	"database/sql"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

func (c *collaborationHTTPConnection) submitOriginal(raw []byte) (result error) {
	diagnostic := &collaborationIngressDiagnostic{stage: "submit_binding"}
	var diagnosticContext context.Context
	defer func() {
		if result != nil {
			c.logIngressFailure(diagnostic.stage, collaborationIngressCategory(result, diagnosticContext, diagnostic))
		}
	}()
	if c == nil || c.document == nil || c.document.originals == nil {
		return errCollaborationHTTP
	}
	c.mu.Lock()
	joined := c.joined
	request, valid := c.request, !c.closed && c.used && c.joinedDelivered && c.admission.write
	c.mu.Unlock()
	diagnostic.at("submit_admission")
	if !valid || request == nil || c.document.activation.RequireHumanNativePrepare() != nil {
		return errCollaborationHTTP
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, collaborationIngressDiagnosticKey{}, diagnostic)
	diagnosticContext = ctx
	d := c.document
	group, err := d.originals.capturePublic(ctx, c, raw)
	if err != nil {
		return err
	}
	diagnostic.at("send_public_group")
	outer, packet, duplicate, err := d.originals.sendPublicGroup(group)
	if err != nil || duplicate {
		return err
	}
	diagnostic.at("send_original_bound")
	return d.humans.runHumanOperation(request.Clone(ctx), c.admission.participant, func(bound context.Context) error {
		diagnostic.at("send_original_writer")
		return d.submissions.withWriter(bound, func(tx *sql.Tx) error {
			diagnostic.at("send_original_cell")
			if err := d.cell.check(bound, tx); err != nil {
				return err
			}
			diagnostic.at("send_original_admission")
			if _, err := d.humans.checkCaptured(bound, tx, c.admission, true, 0); err != nil {
				return err
			}
			diagnostic.at("send_original_runtime")
			return d.cell.runtime.SendParticipantInput(bound, d.activation, protocol.SessionParticipantInput{Type: "session_participant_input", RequestID: outer, ControlProfile: mooring.PrivateSessionProfile, Scope: d.wireScope(), SessionID: protocol.Identifier(d.activation.SessionID()), RuntimeRef: protocol.Identifier(d.cell.runtime.RuntimeRef()), Fence: d.wireFence(), ConnectionRef: protocol.Identifier(c.ref), ParticipantRef: protocol.Identifier(c.admission.participant), GateID: protocol.Identifier(joined.gate.GateID), PermissionVersion: protocol.Counter(joined.gate.PermissionVersion), Message: packet})
		})
	})
}
