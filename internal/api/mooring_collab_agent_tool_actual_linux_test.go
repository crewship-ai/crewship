//go:build linux && mooring_rust_integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocolschema "github.com/crewship-ai/crewship/mooring/protocol"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Real native transport/admission and FULL SQL; the original HTTP and confirmed
// process are controlled host fixtures. This does NOT qualify an actual agent,
// model, Docker environment or a Saved result from synthetic work rows.
func withMooringAgentToolHostFixture(t *testing.T, call func(*collaborationAgentTestFixture, *collaborationHTTPManager, *collaborationLiveAgent)) {
	t.Helper()
	withActualAgentReadSession(t, func(x *collaborationAgentTestFixture, cell *collaborationSessionStore, humans *collaborationHumanRegistry, owner mooring.OwnerLease) {
		mustExec(t, x.f.rig.db, `UPDATE page_grants SET level='write' WHERE page_id=? AND subject_type='agent'`, x.key.ArtifactID)
		handle := x.attach(t, x.prepare(t))
		clock, err := mooring.NewBootOwnerClock()
		if err != nil {
			t.Fatal(err)
		}
		schema, err := mooring.NewWireSchema(protocolschema.JSON())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		registry, err := newCollaborationSubmissionRegistry(humans)
		if err != nil {
			t.Fatal(err)
		}
		m := &collaborationHTTPManager{pages: x.f.pages, config: MooringHTTPConfig{DeliveryTTL: 5 * time.Second}, fence: x.fence, clock: clock, owners: cell.owners, schema: schema, ctx: ctx, agentMandates: x.store}
		d := &collaborationHTTPDocument{manager: m, key: x.key, ctx: ctx, cancel: cancel, cell: cell, owner: owner, activation: cell.activation, humans: humans, submissions: registry, connections: make(map[string]*collaborationHTTPConnection), agents: make(map[string]*collaborationLiveAgent)}
		d.originals = newCollaborationLiveOriginals(d)
		a := &collaborationLiveAgent{document: d, ref: "actual-task-host-fixture", confirmed: collaborationAgentConfirmed{store: x.store, key: x.key, mandate: handle, workID: x.workID, runID: x.f.a.RunID, agentID: x.f.a.AgentID, crewID: x.f.a.CrewID, attempt: 1, generation: 1, execID: "synthetic-confirmed-exec"}, ready: make(chan struct{})}
		d.agents[a.ref] = a
		m.agentTasks = &collaborationAgentTaskController{manager: m, confirmed: map[string]*collaborationLiveAgent{x.f.a.RunID: a}}
		// Exercise the production Joined dispatcher as well as ordinary output.
		// Otherwise this fixture can pass while agent admission is intercepted
		// by the human-only reopen/catch-up path in the composed session pump.
		pump, err := newCollaborationSessionPump(cell.runtime, collaborationSessionPumpHandlers{Submit: func(context.Context, protocol.SessionSubmitBatch) (protocol.SessionCommitResult, error) {
			return protocol.SessionCommitResult{}, errors.New("fixture does not execute SQL submissions")
		}, Status: func(context.Context, protocol.SessionStatusRequest) (protocol.SessionStatusResult, error) {
			return protocol.SessionStatusResult{}, errors.New("fixture has no status operation")
		}, TicketControl: d.ticketControl, DeliveryControl: d.deliveryControl, OutputControl: d.outputControl, JoinedControl: d.joinedControl})
		if err != nil {
			t.Fatal(err)
		}
		d.pump = pump
		done := make(chan error, 1)
		go func() { done <- pump.run(ctx) }()
		defer func() {
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("fixture pump did not stop")
			}
		}()
		call(x, m, a)
	})
}

type agentToolBufferedWriter struct {
	*httptest.ResponseRecorder
	deadline     time.Time
	flushStarted chan struct{}
	once         sync.Once
	stall        bool
	flushed      bool
}

func (w *agentToolBufferedWriter) SetWriteDeadline(v time.Time) error { w.deadline = v; return nil }
func (w *agentToolBufferedWriter) FlushError() error {
	w.once.Do(func() { close(w.flushStarted) })
	if w.stall {
		<-time.After(time.Until(w.deadline))
		return os.ErrDeadlineExceeded
	}
	w.flushed = true
	return nil
}
func newAgentToolBufferedWriter(stall bool) *agentToolBufferedWriter {
	return &agentToolBufferedWriter{ResponseRecorder: httptest.NewRecorder(), flushStarted: make(chan struct{}), stall: stall}
}

func TestMooringAgentToolActualHandlerOwnsAdmissionAndPhysicalFlush(t *testing.T) {
	withMooringAgentToolHostFixture(t, func(x *collaborationAgentTestFixture, m *collaborationHTTPManager, a *collaborationLiveAgent) {
		req := x.request()
		req.Method = http.MethodGet
		w := newAgentToolBufferedWriter(false)
		m.agentTool(w, req)
		var view struct {
			TaskID      string          `json:"task_id"`
			Participant string          `json:"participant_ref"`
			Native      json.RawMessage `json:"native"`
		}
		if w.Code != 200 || !w.flushed || json.Unmarshal(w.Body.Bytes(), &view) != nil || view.TaskID != x.workID || view.Participant == "" || len(view.Native) == 0 {
			t.Fatalf("actual handler did not scope admission/read/physical flush: HTTP%d, flushed=%v", w.Code, w.flushed)
		}
		if a.participant == nil || !a.joined || a.binding == nil {
			t.Fatal("actual native Joined was not captured")
		}
		a.document.submissions.mu.Lock()
		issued := a.document.submissions.agents[a.participant.id] == a.participant
		a.document.submissions.mu.Unlock()
		if !issued {
			t.Fatal("handler skipped original scoped agent admission")
		}
		stalled := newAgentToolBufferedWriter(true)
		finished := make(chan struct{})
		go func() { defer close(finished); m.agentTool(stalled, req.Clone(req.Context())) }()
		select {
		case <-stalled.flushStarted:
		case <-time.After(3 * time.Second):
			t.Fatal("actual handler did not reach flush")
		}
		if x.fence.mu.TryLock() {
			x.fence.mu.Unlock()
			t.Fatal("original ingress released before physical flush")
		}
		if a.document.cell.operations.TryLock() {
			a.document.cell.operations.Unlock()
			t.Fatal("original runtime released before physical flush")
		}
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("unchanged physical deadline did not bound stalled flush")
		}
		// A legitimate renewal may briefly own another read lease. Use the
		// actual exclusive restore barrier, not a racy instantaneous TryLock.
		retired := make(chan struct{})
		go func() { x.fence.SuspendBeforeRestore(); close(retired) }()
		select {
		case <-retired:
		case <-time.After(time.Second):
			t.Fatal("finished flush leaked original ingress lease")
		}
	})
}

func TestMooringAgentToolActualHandlerSealsOriginalBeforeNativeDispatch(t *testing.T) {
	withMooringAgentToolHostFixture(t, func(x *collaborationAgentTestFixture, m *collaborationHTTPManager, a *collaborationLiveAgent) {
		read := x.request()
		read.Method = http.MethodGet
		viewReply := newAgentToolBufferedWriter(false)
		m.agentTool(viewReply, read)
		var view struct {
			BlockIDs []string `json:"block_ids"`
		}
		if viewReply.Code != 200 || json.Unmarshal(viewReply.Body.Bytes(), &view) != nil || len(view.BlockIDs) == 0 {
			t.Fatal("actual original view unavailable")
		}
		change := submissionTestChange(t, x.key, "actual-task-original", x.workID, a.participant.id)
		op := &change.Operations[0]
		op.Payload, _ = json.Marshal(map[string]any{"kind": "text.insert", "target_id": view.BlockIDs[0], "at": map[string]string{"kind": "end"}, "text": " original controlled task"})
		preimage, _ := json.Marshal(map[string]any{"domain": "mooring.atomic.v0.4", "scope": a.document.wireScope(), "change_id": change.ID, "task_id": change.TaskID, "operations_without_fingerprints": []any{map[string]any{"op_id": op.ID, "participant_ref": op.Participant, "base_revision": op.BaseRevision, "dependencies": op.Dependencies, "read_preconditions": op.Reads, "payload": op.Payload}}})
		hashes, err := mooring.FingerprintAtomicJSON(preimage)
		if err != nil {
			t.Fatal(err)
		}
		change.MembershipHash = hashes.MembershipHash
		op.Fingerprint = hashes.Operations[op.ID]
		encoded, _ := json.Marshal([]mooring.BatchChange{change})
		var changes []protocol.AtomicChange
		if json.Unmarshal(encoded, &changes) != nil {
			t.Fatal("typed original changes")
		}
		task := protocol.Identifier(x.workID)
		packet, _ := json.Marshal(protocol.SubmitChanges{Type: "submit_changes", RequestID: "actual-task-public", Scope: a.document.wireScope(), SessionID: protocol.Identifier(a.document.activation.SessionID()), Versions: a.document.wireVersions(), TaskID: &task, Changes: changes})
		request := x.request()
		request.Method = http.MethodPost
		binding, err := a.document.humans.storeBinding()
		if err != nil {
			t.Fatal(err)
		}
		var original *collaborationOriginalGroup
		err = a.document.submissions.run(request.Context(), binding, func(ctx context.Context) error {
			var err error
			original, err = a.document.originals.captureAgent(ctx, a, request.WithContext(ctx), packet)
			return err
		})
		if err != nil {
			t.Fatal("genuine original admission/seal", err)
		}
		charged := a.document.originals.bytes
		// Controlled host-correlation fault BEFORE any physical send. This does
		// not revoke/reissue a mandate or claim production recovery. The exact
		// original store handle remains live throughout this negative control.
		a.deliveryMu.Lock() // Exclude the fixture's renewal worker during this fault.
		retainedHandle := a.confirmed.mandate
		a.confirmed.mandate = "missing-original-handle"
		request.Body = io.NopCloser(bytes.NewReader(packet))
		unsent := newAgentToolBufferedWriter(false)
		m.agentTool(unsent, request)
		a.confirmed.mandate = retainedHandle
		a.deliveryMu.Unlock()
		if unsent.Code != 503 || original.sent || a.document.originals.bytes != charged || a.document.cell.runtime.Err() != nil {
			t.Fatal("definite unsent handler failure lost exact retry or poisoned native runtime")
		}
		request.Body = io.NopCloser(bytes.NewReader(packet))
		response := newAgentToolBufferedWriter(false)
		m.agentTool(response, request)
		if !original.sent || a.document.pump.queuedMutations.Load() != 1 || a.document.originals.bytes != charged {
			t.Fatal("exact original retry did not cross native FD once with retained charge")
		}
		// The fixture deliberately refuses SQL application. Only real original
		// admission/sealing/native dispatch is asserted; no Saved is fabricated.
		if response.Code != 503 {
			t.Fatalf("fixture unexpectedly produced an outcome HTTP%d", response.Code)
		}
		registry := a.document.submissions
		registry.mu.Lock()
		entry, found := registry.entries[op.ID]
		registry.mu.Unlock()
		if !found || entry.author.agent == nil || entry.author.human != nil || entry.author.agent.workID != x.workID || entry.membership != change.MembershipHash {
			t.Fatal("actual handler did not seal original agent membership")
		}
		a.document.originals.mu.Lock()
		group := a.document.originals.groups[a.ref+"\x00actual-task-public"]
		captured := group != nil && group.captured
		a.document.originals.mu.Unlock()
		if !captured {
			t.Fatal("sealed group original was not retained")
		}
	})
}
