//go:build linux && mooring_rust_integration

package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/work"
	protocolschema "github.com/crewship-ai/crewship/mooring/protocol"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Real native transport/admission and FULL SQL; the original HTTP and confirmed
// process are controlled host fixtures. This does NOT qualify an actual agent,
// model, Docker environment or a Saved result from synthetic work rows.
func withMooringAgentToolHostFixture(t *testing.T, call func(*collaborationAgentTestFixture, *collaborationHTTPManager, *collaborationLiveAgent), intent ...string) {
	t.Helper()
	withMooringAgentToolHostFixtureOutput(t, call, nil, intent...)
}

// Configure only test scheduling before the real pump starts, never authority.
func withMooringAgentToolHostFixtureOutput(t *testing.T, call func(*collaborationAgentTestFixture, *collaborationHTTPManager, *collaborationLiveAgent), configure func(*collaborationSessionPumpHandlers), intent ...string) {
	t.Helper()
	withActualAgentReadSession(t, func(x *collaborationAgentTestFixture, cell *collaborationSessionStore, humans *collaborationHumanRegistry, owner mooring.OwnerLease) {
		var handle string
		if len(intent) == 1 && intent[0] == "read" {
			var pending *collaborationAgentPending
			x.tx(t, func(tx *sql.Tx) {
				var accepted work.Receipt
				var err error
				pending, accepted, err = x.store.StartTask(x.human, tx, x.key, "read", x.f.a.MessageID, x.f.receipt)
				if err != nil {
					t.Fatal("original read task", err)
				}
				x.workID = accepted.WorkID
			})
			handle = x.attach(t, pending)
		} else {
			if len(intent) != 0 {
				t.Fatal("unknown fixture task intent")
			}
			mustExec(t, x.f.rig.db, `UPDATE page_grants SET level='write' WHERE page_id=? AND subject_type='agent'`, x.key.ArtifactID)
			handle = x.attach(t, x.prepare(t))
		}
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
		handlers := collaborationSessionPumpHandlers{Submit: func(context.Context, protocol.SessionSubmitBatch) (protocol.SessionCommitResult, error) {
			return protocol.SessionCommitResult{}, errors.New("fixture does not execute SQL submissions")
		}, Status: func(context.Context, protocol.SessionStatusRequest) (protocol.SessionStatusResult, error) {
			return protocol.SessionStatusResult{}, errors.New("fixture has no status operation")
		}, TicketControl: d.ticketControl, DeliveryControl: d.deliveryControl, OutputControl: d.outputControl, JoinedControl: d.joinedControl}
		if configure != nil {
			configure(&handlers)
		}
		pump, err := newCollaborationSessionPump(cell.runtime, handlers)
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
	t.Run("D3-paged-native-read", testMooringActualAgentD3PageReassembly)
	t.Run("paged-timeout-late-reply", testMooringActualAgentPageTimeout)
	t.Run("paged-output-correlation", testMooringActualAgentPageCorrelation)
	t.Run("paged-rights-before-capture-and-flush", testMooringActualAgentPageRevocation)
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
		assertRetainedAgentTicketGrant(t, a, true)
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

	// Same catalog top, new actual subcases. No extra process/model source is minted.
	t.Run("read-task-D1", func(t *testing.T) { runMooringReadTaskTool(t) })
	t.Run("read-task-D3", func(t *testing.T) {
		t.Setenv("MOORING_SESSION_CHECKPOINT", os.Getenv("MOORING_D3_CHECKPOINT"))
		t.Setenv("MOORING_SESSION_CHECKPOINT_SHA256", os.Getenv("MOORING_D3_CHECKPOINT_SHA256"))
		runMooringReadTaskTool(t)
	})
	t.Run("read-task-synthetic-retirement", func(t *testing.T) { runReadTaskSyntheticRetirement(t) })
}

// Native admission and actual FULL SQL read/renew/retire. Process/run/source
// attachment is a controlled host fixture, not a paid agent or user/model proof.
func runMooringReadTaskTool(t *testing.T) {
	withMooringAgentToolHostFixture(t, func(x *collaborationAgentTestFixture, m *collaborationHTTPManager, a *collaborationLiveAgent) {
		req := x.request()
		req.Method = http.MethodGet
		reply := newAgentToolBufferedWriter(false)
		m.agentTool(reply, req)
		var view struct {
			Native      json.RawMessage `json:"native"`
			Participant string          `json:"participant_ref"`
		}
		if reply.Code != 200 || !reply.flushed || json.Unmarshal(reply.Body.Bytes(), &view) != nil || len(view.Native) == 0 || view.Participant == "" || a.participant == nil || a.participant.captured.intent != "read" || !a.joined || a.binding == nil {
			t.Fatal("actual attached read task did not join/read/flush", reply.Code)
		}
		assertRetainedAgentTicketGrant(t, a, false)
		// Exercise the SAME DeliveryStore authority closure used by native renew.
		// This is real SQL renewal, not a claim of a separately observed native RPC.
		if err := a.run(t.Context(), func(ctx context.Context) error {
			_, err := a.delivery.Renew(ctx, a.document.owner, a.gate.GateID, "read-task-test-renewal")
			return err
		}); err != nil {
			t.Fatal("read task current renewal denied", err)
		}
		for _, body := range []string{`{"kind":"text.insert","text":"must-not-submit"}`, `{"kind":"wordcraft.yrs-update.v1","update":"AA==","targets":[]}`} {
			spy := &readTaskDeniedBody{raw: []byte(body)}
			post := x.request()
			post.Method = http.MethodPost
			post.Body = spy
			denied := newAgentToolBufferedWriter(false)
			m.agentTool(denied, post)
			if denied.Code != 403 || spy.reads != 0 {
				t.Fatal("read task entered body/capture instead of refusal", denied.Code, spy.reads)
			}
			a.document.originals.mu.Lock()
			slots, groups, bytes := len(a.document.originals.changes), len(a.document.originals.groups), a.document.originals.bytes
			a.document.originals.mu.Unlock()
			a.document.submissions.mu.Lock()
			seals := len(a.document.submissions.entries)
			a.document.submissions.mu.Unlock()
			if slots != 0 || groups != 0 || bytes != 0 || seals != 0 || a.document.pump.queuedMutations.Load() != 0 {
				t.Fatal("reader created original slots/seals/native submission")
			}
		}
		var operations int
		if err := x.f.rig.db.QueryRow(`SELECT count(*) FROM mooring_collab_operations WHERE page_id=?`, x.key.ArtifactID).Scan(&operations); err != nil || operations != 0 {
			t.Fatal("read-only fixture changed SQL operations", operations, err)
		}

		// Canonical grant API revoke and ABA; SQL trigger closes this exact gate.
		// Exclude concurrent native renew/retire only while checking original denial.
		a.deliveryMu.Lock()
		defer a.deliveryMu.Unlock()
		mutation := pagesGrantCall(t, x.f.pages, "DELETE", "/api/v1/pages/agent-shared-page/grants?subject_type=agent&subject="+x.f.a.AgentID+"&level=read", x.key.WorkspaceID, x.f.a.HumanUserID, "MEMBER", "agent-shared-page", "")
		if mutation.Code != 200 {
			t.Fatal("canonical read grant revoke failed", mutation.Code)
		}
		denyReadAndRenew := func(label string) {
			denied := newAgentToolBufferedWriter(false)
			m.agentTool(denied, req.Clone(req.Context()))
			if denied.Code != 403 {
				t.Fatal("original read task survived "+label, denied.Code)
			}
			if err := a.run(t.Context(), func(ctx context.Context) error {
				_, err := a.delivery.Renew(ctx, a.document.owner, a.gate.GateID, "after-"+label)
				return err
			}); err == nil {
				t.Fatal("read delivery renewed after " + label)
			}
		}
		denyReadAndRenew("revoke")
		restored := pagesGrantCall(t, x.f.pages, "PUT", "/api/v1/pages/agent-shared-page/grants", x.key.WorkspaceID, x.f.a.HumanUserID, "MEMBER", "agent-shared-page", `{"subject_type":"agent","subject":"`+x.f.a.AgentID+`","level":"read"}`)
		if restored.Code != 200 {
			t.Fatal("canonical read grant ABA restore failed", restored.Code)
		}
		denyReadAndRenew("aba")
		var closed bool
		if err := x.f.rig.db.QueryRow(`SELECT closed FROM mooring_delivery_gates WHERE id=?`, a.gate.GateID).Scan(&closed); err != nil || !closed {
			t.Fatal("canonical trigger did not close exact read gate", err)
		}
		if err := a.retireClosedGate(t.Context()); err != nil {
			t.Fatal("revoked read-only binding retirement denied", err)
		}
		if a.retired == nil || a.binding != nil || !a.closed || a.document.cell.runtime.Err() != nil {
			t.Fatal("read-only recipient did not retire cleanly")
		}

	}, "read")
}

type readTaskDeniedBody struct {
	raw   []byte
	reads int
}

func (b *readTaskDeniedBody) Read(p []byte) (int, error) { b.reads++; return copy(p, b.raw), io.EOF }
func (b *readTaskDeniedBody) Close() error               { return nil }

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

// Read-only TEST introspection of the actual retained physically-sent ticket.
// This is NOT a supported runtime API. CaptureJoined correlates that ticket to
// this binding. No unsafe/Set/Interface dump is used. Read before any deliberate
// renewal/revocation/retirement; callback lifetime excludes fixture cleanup.
// Unexpected concurrent runtime Close remains a real race-test failure.
func assertRetainedAgentTicketGrant(t *testing.T, a *collaborationLiveAgent, wantWrite bool) {
	t.Helper()
	if a.document.cell.runtime.Err() != nil {
		t.Fatal("runtime not live for grant oracle")
	}
	a.deliveryMu.Lock()
	a.mu.Lock()
	defer a.deliveryMu.Unlock()
	defer a.mu.Unlock()
	if !a.joined || a.binding == nil || a.closed {
		t.Fatal("grant oracle requires live captured Joined")
	}
	v := reflect.ValueOf(a.binding)
	deref := func(value reflect.Value) reflect.Value {
		if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() {
			t.Fatal("retained grant pointer shape changed")
		}
		value = value.Elem()
		if value.Kind() != reflect.Struct {
			t.Fatal("retained grant struct shape changed")
		}
		return value
	}
	v = deref(v)
	v = deref(v.FieldByName("state"))
	ticket := v.FieldByName("ticket")
	if !ticket.IsValid() || ticket.Kind() != reflect.Struct {
		t.Fatal("retained ticket missing")
	}
	grant := ticket.FieldByName("Granted")
	if !grant.IsValid() || grant.Kind() != reflect.Struct {
		t.Fatal("retained capabilities missing")
	}
	readBool := func(name string) bool {
		value := grant.FieldByName(name)
		if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() || value.Elem().Kind() != reflect.Bool {
			t.Fatal("retained capability bool pointer missing")
		}
		return value.Elem().Bool()
	}
	if !readBool("Read") || readBool("Write") != wantWrite {
		t.Fatal("actual native ticket capabilities differ from original task mode")
	}
}

// SYNTHETIC historical-vector leaf, NOT a reachable canonical mutation claim.
// Canonical revoke above closes SQL via triggers and exercises the shortcut.
// Here ONLY two matching trusted RAM vector copies differ from current SQL;
// no SQL rights/gate/credential/attempt/trigger is changed or reopened. After
// this counterfactual setup perform denial-only retirement, never external DATA.
func runReadTaskSyntheticRetirement(t *testing.T) {
	withMooringAgentToolHostFixture(t, func(x *collaborationAgentTestFixture, m *collaborationHTTPManager, a *collaborationLiveAgent) {
		req := x.request()
		req.Method = http.MethodGet
		reply := newAgentToolBufferedWriter(false)
		m.agentTool(reply, req)
		if reply.Code != 200 {
			t.Fatal("baseline actual read before synthetic leaf failed", reply.Code)
		}
		assertRetainedAgentTicketGrant(t, a, false)
		a.deliveryMu.Lock()
		defer a.deliveryMu.Unlock()
		var closed bool
		var expires int64
		if err := x.f.rig.db.QueryRow(`SELECT closed,expires_ns FROM mooring_delivery_gates WHERE id=?`, a.gate.GateID).Scan(&closed, &expires); err != nil || closed {
			t.Fatal("synthetic leaf needs original live gate", err)
		}
		_, now, err := m.clock.Now()
		if err != nil || int64(now) >= expires {
			t.Fatal("synthetic leaf gate expired", err)
		}
		x.store.mu.Lock()
		entry := x.store.attached[a.confirmed.mandate]
		original := entry.binding.task.authorVector
		synthetic := original
		synthetic.agentRevision = "1"
		if original.agentRevision == "1" {
			synthetic.agentRevision = "2"
		}
		entry.binding.task.authorVector = synthetic
		x.store.attached[a.confirmed.mandate] = entry
		x.store.mu.Unlock()
		a.mu.Lock()
		a.participant.captured.authorVector = synthetic
		a.mu.Unlock()
		err = a.run(t.Context(), func(ctx context.Context) error {
			return a.document.submissions.withWriter(ctx, func(tx *sql.Tx) error {
				decision, e := x.store.DecideCaptured(ctx, tx, a.participant.captured, x.key, "read")
				if e != nil || decision.kind != collaborationAgentCanonicalPermissionChanged {
					t.Fatal("synthetic vector did not reach read canonical-change branch", e)
				}
				if _, e = x.store.DecideCaptured(ctx, tx, a.participant.captured, x.key, "task"); e == nil {
					t.Fatal("synthetic read capture became task authority")
				}
				return nil
			})
		})
		if err != nil {
			t.Fatal(err)
		}
		if err = a.retireClosedGate(t.Context()); err != nil {
			t.Fatal("synthetic read live-gate branch failed", err)
		}
		if err = x.f.rig.db.QueryRow(`SELECT closed FROM mooring_delivery_gates WHERE id=?`, a.gate.GateID).Scan(&closed); err != nil || !closed || a.retired == nil || a.binding != nil || !a.closed {
			t.Fatal("synthetic read retirement did not close exact gate and original binding", err)
		}
	}, "read")
}
