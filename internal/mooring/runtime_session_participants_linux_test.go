//go:build linux

package mooring

import (
	"context"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Actual inherited child and real SQL startup/activation, synthetic native
// document/participant semantics. This is transport, not an ingress grant.
func participantTransportRequests(t *testing.T, f originFixture) (protocol.SessionParticipantOpen, protocol.SessionParticipantInput) {
	t.Helper()
	scope := protocol.Scope{WorkspaceID: protocol.Identifier(f.owner.Key.WorkspaceID), ArtifactID: protocol.Identifier(f.owner.Key.ArtifactID), ApplicationID: "wordcraft"}
	fence := protocol.Fence{Generation: protocol.Counter(strconv.FormatInt(f.owner.Generation, 10)), Token: f.owner.Token}
	join, err := json.Marshal(protocol.SessionJoin{Type: "session_join", RequestID: "join", Ticket: "synthetic-ticket", Versions: protocol.Versions{ProtocolVersion: sessionTestVersions.Protocol, ModelVersion: sessionTestVersions.Model, EngineVersion: sessionTestVersions.Engine}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := json.Marshal(protocol.StatusRequest{Type: "operation_status", RequestID: "status", Scope: scope, OpID: "operation", ExpectedFingerprint: protocol.Digest("sha256:" + strings.Repeat("a", 64))})
	if err != nil {
		t.Fatal(err)
	}
	open := protocol.SessionParticipantOpen{Type: "session_participant_open", RequestID: "participant-open", ControlProfile: PrivateSessionProfile, Scope: scope, SessionID: protocol.Identifier(f.activation.SessionID()), RuntimeRef: protocol.Identifier(f.runtime.RuntimeRef()), Fence: fence, ConnectionRef: "connection", Join: join}
	input := protocol.SessionParticipantInput{Type: "session_participant_input", RequestID: "participant-input", ControlProfile: PrivateSessionProfile, Scope: scope, SessionID: open.SessionID, RuntimeRef: open.RuntimeRef, Fence: fence, ConnectionRef: "connection", ParticipantRef: "synthetic-participant", GateID: "synthetic-gate", PermissionVersion: "1", Message: message}
	return open, input
}
func participantTransportReceive(t *testing.T, f originFixture, request string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	token, err := f.runtime.NextControl(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := token.Frame()
	if err != nil {
		t.Fatal(err)
	}
	var output protocol.SessionParticipantOutput
	if json.Unmarshal(raw, &output) != nil || string(output.RequestID) != "output-"+request || output.ConnectionRef != "connection" {
		t.Fatal("wrong original notification", string(raw))
	}
	if err := f.runtime.CompleteControl(token); err != nil {
		t.Fatal(err)
	}
}
func TestSessionParticipantTransportRequiresActivationAndExactBinding(t *testing.T) {
	f := sessionOriginsFixture(t, "participant-notifications", false)
	open, input := participantTransportRequests(t, f)
	for _, activation := range []*SessionActivation{nil, {}} {
		if err := f.runtime.SendParticipantOpen(t.Context(), activation, open); err == nil {
			t.Fatal("missing activation sent participant frame")
		}
	}
	for _, mutate := range []func(*protocol.SessionParticipantOpen){
		func(r *protocol.SessionParticipantOpen) { r.Scope.ArtifactID = "foreign" },
		func(r *protocol.SessionParticipantOpen) { r.SessionID = "foreign" },
		func(r *protocol.SessionParticipantOpen) { r.RuntimeRef = "foreign" },
		func(r *protocol.SessionParticipantOpen) {
			r.Fence.Generation = protocol.Counter(strconv.FormatInt(f.owner.Generation+1, 10))
		},
		func(r *protocol.SessionParticipantOpen) { r.Fence.Token = strings.Repeat("f", 64) },
		func(r *protocol.SessionParticipantOpen) {
			r.Join = json.RawMessage(strings.Repeat(" ", sessionParticipantFrameBytes+1))
		},
	} {
		wrong := open
		mutate(&wrong)
		if err := f.runtime.SendParticipantOpen(t.Context(), f.activation, wrong); err == nil {
			t.Fatal("foreign/oversized frame sent")
		}
	}
	if err := f.runtime.SendParticipantOpen(t.Context(), f.activation, open); err != nil {
		t.Fatal("actual notification", err)
	}
	participantTransportReceive(t, f, string(open.RequestID))
	if err := f.runtime.SendParticipantInput(t.Context(), f.activation, input); err != nil {
		t.Fatal("actual input", err)
	}
	participantTransportReceive(t, f, string(input.RequestID))
	f.runtime.mu.Lock()
	count, bytes := f.runtime.count, f.runtime.bytes
	f.runtime.mu.Unlock()
	if count != 0 || bytes != 0 {
		t.Fatal("transport retained completed frames", count, bytes)
	}
}
func TestSessionParticipantTransportQueuedCancellationDoesNotEmitOrPoison(t *testing.T) {
	f := sessionOriginsFixture(t, "participant-notifications", false)
	_, input := participantTransportRequests(t, f)
	<-f.runtime.writer
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { result <- f.runtime.SendParticipantInput(ctx, f.activation, input) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.runtime.mu.Lock()
		reserved := f.runtime.count > 0
		f.runtime.mu.Unlock()
		if reserved {
			break
		}
		if time.Now().After(deadline) {
			f.runtime.writer <- struct{}{}
			t.Fatal("notification did not enter bounded queue")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal("queued cancellation", err)
	}
	f.runtime.writer <- struct{}{}
	select {
	case <-f.runtime.Done():
		t.Fatal("queued cancellation killed peer")
	default:
	}
	// A subsequent valid frame still crosses the same actual FD.
	if err := f.runtime.SendParticipantInput(t.Context(), f.activation, input); err != nil {
		t.Fatal(err)
	}
	participantTransportReceive(t, f, string(input.RequestID))
}

func participantTransportWaitReserved(t *testing.T, f originFixture) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.runtime.mu.Lock()
		reserved := f.runtime.count > 0
		f.runtime.mu.Unlock()
		if reserved {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("notification reservation missing")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestSessionParticipantTransportRechecksActivationAfterWriterWait(t *testing.T) {
	f := sessionOriginsFixture(t, "participant-notifications", false)
	_, input := participantTransportRequests(t, f)
	<-f.runtime.writer
	result := make(chan error, 1)
	go func() { result <- f.runtime.SendParticipantInput(t.Context(), f.activation, input) }()
	participantTransportWaitReserved(t, f)
	f.runtime.mu.Lock()
	f.runtime.observed.invalid = true
	f.runtime.mu.Unlock()
	f.runtime.writer <- struct{}{}
	if err := <-result; err == nil {
		t.Fatal("invalidated activation crossed writer")
	}
	// Synthetic invalidation is restored only to prove no rejected frame crossed.
	f.runtime.mu.Lock()
	f.runtime.observed.invalid = false
	f.runtime.mu.Unlock()
	if err := f.runtime.SendParticipantInput(t.Context(), f.activation, input); err != nil {
		t.Fatal(err)
	}
	participantTransportReceive(t, f, string(input.RequestID))
}
func TestSessionParticipantTransportCanceledPhysicalWritePoisonsActualChild(t *testing.T) {
	f := sessionOriginsFixture(t, "participant-notification-stall", false)
	_, input := participantTransportRequests(t, f)
	connection, ok := f.runtime.process.RPC.conn.(syscall.Conn)
	if !ok {
		t.Fatal("test needs the actual inherited socket")
	}
	raw, err := connection.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var sockErr error
	if err = raw.Control(func(fd uintptr) { sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_SNDBUF, 4096) }); err != nil || sockErr != nil {
		t.Fatal("bounded test socket", err, sockErr)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	failed := false
	for i := 0; i < 1024; i++ {
		input.RequestID = protocol.Identifier("blocked-" + strconv.Itoa(i))
		if err = f.runtime.SendParticipantInput(ctx, f.activation, input); err != nil {
			failed = true
			break
		}
	}
	if !failed || ctx.Err() == nil {
		t.Fatal("actual stalled write was not canceled", err)
	}
	select {
	case <-f.runtime.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("uncertain physical write left actual child running")
	}
	if err := f.runtime.SendParticipantInput(t.Context(), f.activation, input); err == nil {
		t.Fatal("later notification crossed uncertain stream")
	}
}
