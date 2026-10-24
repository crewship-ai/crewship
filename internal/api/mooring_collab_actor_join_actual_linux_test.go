//go:build linux && mooring_rust_integration

package api

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Actual pinned Rust actor, SQL gate and BOOTTIME. HTTP authentication and
// confirmed work contexts are synthetic fixtures. No content is delivered here.
func TestCollaborationActualActorJoinRequiresOriginalCommittedSQLGate(t *testing.T) {
	withActualAgentReadSession(t, func(x *collaborationAgentTestFixture, cell *collaborationSessionStore, humans *collaborationHumanRegistry, owner mooring.OwnerLease) {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		runtime := cell.runtime
		ticket, admission, err := humans.issue(x.human, owner, false)
		if err != nil {
			t.Fatal("original admission", err)
		}
		scope := protocol.Scope{WorkspaceID: protocol.Identifier(x.key.WorkspaceID), ArtifactID: protocol.Identifier(x.key.ArtifactID), ApplicationID: protocol.Identifier(x.key.ApplicationID)}
		versions := protocol.Versions{ProtocolVersion: "0.4.0", ModelVersion: "0.2.0", EngineVersion: "0.10.0"}
		fence := protocol.Fence{Generation: protocol.Counter(strconv.FormatInt(owner.Generation, 10)), Token: owner.Token}
		join, _ := json.Marshal(protocol.SessionJoin{Type: "session_join", RequestID: "actual-join", Ticket: ticket, Versions: versions})
		opening := protocol.SessionParticipantOpen{Type: "session_participant_open", RequestID: "actual-opening", ControlProfile: mooring.PrivateSessionProfile, Scope: scope, SessionID: protocol.Identifier(cell.activation.SessionID()), RuntimeRef: protocol.Identifier(runtime.RuntimeRef()), Fence: fence, ConnectionRef: "actual-original-connection", Join: join}
		if err = runtime.SendParticipantOpen(ctx, cell.activation, opening); err != nil {
			t.Fatal("actual participant open", err)
		}
		token, err := runtime.NextControl(ctx)
		if err != nil {
			t.Fatal("actual ticket request", err)
		}
		raw, err := token.Frame()
		if err != nil {
			t.Fatal(err)
		}
		var validation protocol.TicketValidationRequest
		if json.Unmarshal(raw, &validation) != nil || validation.Type != "ticket_validate" || validation.Scope != scope || validation.SessionID != opening.SessionID || validation.Fence != fence || validation.Ticket != ticket {
			t.Fatal("uncorrelated ticket request", string(raw))
		}
		joined, err := humans.join(x.human, runtime, owner, validation.Ticket, string(validation.ValidationNonce))
		if err != nil {
			t.Fatal("actual original SQL gate", err)
		}
		if joined.admission.participant != admission.participant || joined.gate.GateID == "" || joined.gate.DurationMilliseconds != 2000 {
			t.Fatal("gate changed original binding")
		}
		var live int
		if err = x.f.rig.db.QueryRow(`SELECT COUNT(*) FROM mooring_delivery_gates WHERE id=? AND participant_ref=? AND closed=0`, joined.gate.GateID, admission.participant).Scan(&live); err != nil || live != 1 {
			t.Fatal("uncommitted SQL gate", live, err)
		}
		canRead, canWrite := true, false
		result := protocol.TicketValidationResult{Type: "ticket_validated", RequestID: validation.RequestID, Scope: scope, SessionID: opening.SessionID, Generation: fence.Generation, ParticipantRef: protocol.Identifier(admission.participant), PermissionVersion: protocol.Counter(joined.gate.PermissionVersion), Granted: protocol.Capabilities{Read: &canRead, Write: &canWrite}, Versions: versions, ExpiresInMs: protocol.PositiveCounter(strconv.FormatInt(joined.gate.DurationMilliseconds, 10)), ValidationNonce: validation.ValidationNonce, GateID: protocol.Identifier(joined.gate.GateID)}
		raw, _ = json.Marshal(result)
		if err = runtime.ReplyControl(ctx, token, raw); err != nil {
			t.Fatal("committed ticket result", err)
		}
		outputToken, err := runtime.NextControl(ctx)
		if err != nil {
			t.Fatal("actual joined output", err)
		}
		raw, err = outputToken.Frame()
		if err != nil {
			t.Fatal(err)
		}
		var output protocol.SessionParticipantOutput
		if json.Unmarshal(raw, &output) != nil || output.Scope != scope || output.SessionID != opening.SessionID || output.RuntimeRef != opening.RuntimeRef || output.Fence != fence || output.ConnectionRef != opening.ConnectionRef || output.ParticipantRef != result.ParticipantRef || output.GateID != result.GateID || output.PermissionVersion != result.PermissionVersion {
			t.Fatal("uncorrelated joined envelope", string(raw))
		}
		var confirmation protocol.SessionJoined
		if json.Unmarshal(output.Message, &confirmation) != nil || confirmation.Type != "session_joined" || confirmation.Scope != scope || confirmation.SessionID != opening.SessionID || confirmation.ParticipantRef != result.ParticipantRef || confirmation.GateID != result.GateID || confirmation.PermissionVersion != result.PermissionVersion || confirmation.Generation != fence.Generation || confirmation.Versions != versions {
			t.Fatal("uncorrelated inner join", string(output.Message))
		}
		if err = runtime.CompleteControl(outputToken); err != nil {
			t.Fatal(err)
		}
		if _, err = humans.join(x.human, runtime, owner, ticket, "replay-nonce"); err == nil {
			t.Fatal("burned ticket reopened gate")
		}
		mustExec(t, x.f.rig.db, `UPDATE user_sessions SET revoked_at='2026-10-24T00:00:00Z' WHERE id='original-session'`)
		if _, _, err = humans.issue(x.human, owner, false); err == nil {
			t.Fatal("revoked original credential issued a fresh ticket")
		}
		if _, err = joined.delivery.Renew(x.human, owner, joined.gate.GateID, "revoked-renew"); err == nil {
			t.Fatal("revoked source renewed delivery")
		}
	})
}
