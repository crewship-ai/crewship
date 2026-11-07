package mooring

import (
	"encoding/json"
	"testing"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

func rotationOutput(t *testing.T, gate, connection string, message any) []byte {
	t.Helper()
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	scope := protocol.Scope{WorkspaceID: "ws", ArtifactID: "page", ApplicationID: "app"}
	output, err := json.Marshal(protocol.SessionParticipantOutput{Type: "session_participant_output", RequestID: "original", ControlProfile: PrivateSessionProfile, Scope: scope, SessionID: "session", RuntimeRef: "runtime", Fence: protocol.Fence{Generation: "1", Token: "token"}, ConnectionRef: protocol.Identifier(connection), ParticipantRef: "participant", GateID: protocol.Identifier(gate), PermissionVersion: "7", Message: raw})
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func rotationPhases(t *testing.T, oldGate, newGate, connection string) (pending, terminal []byte) {
	t.Helper()
	scope := protocol.Scope{WorkspaceID: "ws", ArtifactID: "page", ApplicationID: "app"}
	pending = rotationOutput(t, oldGate, "connection", protocol.ChangesReceived{Type: "changes_received", RequestID: "public", Scope: scope, SessionID: "session", State: "pending", ChangeIds: []protocol.Identifier{"change"}})
	terminal = rotationOutput(t, newGate, connection, protocol.CommitResult{Type: "commit_result", RequestID: "public", Scope: scope, Generation: "1", Changes: []protocol.ChangeResult{{ChangeID: "change", Status: "saved"}}})
	return pending, terminal
}

// #5019. A rotation can overtake a queued Pending: the Pending stays on the old
// gate and its receipt arrives on the new one. That one original output must not
// read as a reused ID, which ends the session for every participant. Only a
// rotation this runtime itself recorded may bridge the two gates.
func TestSessionOutputPhasesPairOnlyAcrossRecordedRotation(t *testing.T) {
	chain := func(s *SessionRuntime, connection string, gates ...string) []*sessionParticipantState {
		states := make([]*sessionParticipantState, len(gates))
		for i, gate := range gates {
			st := &sessionParticipantState{gate: gate, left: i < len(gates)-1, open: protocol.SessionParticipantOpen{ConnectionRef: protocol.Identifier(connection)}, ticket: protocol.TicketValidationResult{GateID: protocol.Identifier(gate)}}
			if i > 0 {
				st.gate = "" // Reader sees the ticket before Joined is captured.
				st.predecessor, states[i-1].successor = states[i-1], st
			}
			states[i] = st
			s.participantStates[st] = struct{}{}
		}
		return states
	}
	for _, tc := range []struct {
		name       string
		newGate    string
		connection string
		gates      []string
		mutate     func([]*sessionParticipantState)
		want       bool
	}{
		{name: "same gate needs no rotation", newGate: "old", connection: "connection", gates: []string{"old"}, want: true},
		{name: "recorded rotation", newGate: "new", connection: "connection", gates: []string{"old", "new"}, want: true},
		{name: "two recorded rotations", newGate: "newest", connection: "connection", gates: []string{"old", "new", "newest"}, want: true},
		{name: "unrelated gate", newGate: "foreign", connection: "connection", gates: []string{"old", "new"}},
		{name: "no rotation recorded", newGate: "new", connection: "connection", gates: []string{"old"}},
		{name: "another connection", newGate: "new", connection: "other", gates: []string{"old", "new"}},
		{name: "old gate never left", newGate: "new", connection: "connection", gates: []string{"old", "new"}, mutate: func(s []*sessionParticipantState) { s[0].left = false }},
		{name: "successor released", newGate: "new", connection: "connection", gates: []string{"old", "new"}, mutate: func(s []*sessionParticipantState) { s[1].released = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &SessionRuntime{participantStates: make(map[*sessionParticipantState]struct{})}
			states := chain(s, "connection", tc.gates...)
			if tc.mutate != nil {
				tc.mutate(states)
			}
			pending, terminal := rotationPhases(t, "old", tc.newGate, tc.connection)
			if got := s.outputPhasesCorrelatedLocked(pending, terminal); got != tc.want {
				t.Fatalf("correlated=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestSessionOutputPhasesRotationBridgesOnlyTheGate(t *testing.T) {
	s := &SessionRuntime{participantStates: make(map[*sessionParticipantState]struct{})}
	old := &sessionParticipantState{gate: "old", left: true, open: protocol.SessionParticipantOpen{ConnectionRef: "connection"}}
	next := &sessionParticipantState{open: protocol.SessionParticipantOpen{ConnectionRef: "connection"}, ticket: protocol.TicketValidationResult{GateID: "new"}, predecessor: old}
	old.successor = next
	s.participantStates[old], s.participantStates[next] = struct{}{}, struct{}{}
	pending, terminal := rotationPhases(t, "old", "new", "connection")
	for _, field := range []string{"participant_ref", "permission_version", "runtime_ref"} {
		var changed map[string]any
		if err := json.Unmarshal(terminal, &changed); err != nil {
			t.Fatal(err)
		}
		changed[field] = "changed"
		raw, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		if s.outputPhasesCorrelatedLocked(pending, raw) {
			t.Fatalf("a rotation excused a changed %s", field)
		}
	}
	// Two terminals, or the phases of another change, are still a reused ID.
	if s.outputPhasesCorrelatedLocked(terminal, rotationOutput(t, "new", "connection", json.RawMessage(`{"type":"commit_result"}`))) {
		t.Fatal("a rotation paired two terminal outputs")
	}
}
