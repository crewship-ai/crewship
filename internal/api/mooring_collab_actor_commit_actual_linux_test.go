//go:build linux && mooring_rust_integration

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Actual pinned Rust actor, SQL gate and BOOTTIME. HTTP authentication and
// confirmed work contexts are synthetic fixtures. No content is delivered here.
type actualActorSQLParticipant struct {
	x        *collaborationAgentTestFixture
	cell     *collaborationSessionStore
	humans   *collaborationHumanRegistry
	owner    mooring.OwnerLease
	joined   collaborationHumanJoined
	opening  protocol.SessionParticipantOpen
	versions protocol.Versions
}

func withActualActorSQLParticipant(t *testing.T, call func(*actualActorSQLParticipant)) {
	withActualAgentReadSession(t, func(x *collaborationAgentTestFixture, cell *collaborationSessionStore, humans *collaborationHumanRegistry, owner mooring.OwnerLease) {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		runtime := cell.runtime
		ticket, admission, err := humans.issue(x.human, owner, true)
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
		canRead, canWrite := true, true
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
		call(&actualActorSQLParticipant{x: x, cell: cell, humans: humans, owner: owner, joined: joined, opening: opening, versions: versions})
	})
}

// Actual actor/FIFO/native preparation/FULL SQL. Original structured commands
// and their human seals remain trusted synthetic ingress, not source issuance.
// Output is a private test collector, never external delivery authority.
func TestCollaborationActualActorBPrecedesAAckAndCommitsOriginalSQL(t *testing.T) {
	withActualActorSQLParticipant(t, func(f *actualActorSQLParticipant) {
		started := time.Now()
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		request := httptest.NewRequest("POST", "/host-only-actor-test", nil).WithContext(f.x.human)
		submissions, err := newCollaborationSubmissionRegistry(f.humans)
		if err != nil {
			t.Fatal(err)
		}
		binding, err := f.humans.storeBinding()
		if err != nil {
			t.Fatal(err)
		}
		committer, err := newCollaborationSessionCommitter(ctx, f.cell, submissions, f.owner)
		if err != nil {
			t.Fatal(err)
		}
		var snapshot mooring.FullCheckpointSnapshot
		err = f.humans.runHumanOperation(request, f.joined.admission.participant, func(bound context.Context) error {
			var e error
			snapshot, e = f.cell.store.ReadFullCheckpoint(bound, f.owner)
			return e
		})
		if err != nil || len(snapshot.Targets) == 0 {
			t.Fatal("original full snapshot", err)
		}
		makeChange := func(id, text, dependency string) mooring.BatchChange {
			change := submissionTestChange(t, f.cell.scope, id, "", f.joined.admission.participant)
			op := &change.Operations[0]
			op.Payload, _ = json.Marshal(map[string]any{"kind": "text.insert", "target_id": snapshot.Targets[0].TargetID, "at": map[string]string{"kind": "end"}, "text": text})
			zero := "0"
			op.Reads = []mooring.BatchReadPrecondition{{TargetID: snapshot.Targets[0].TargetID, Revision: "0", StructureRevision: &zero}}
			if dependency != "" {
				op.Dependencies = []string{dependency}
			}
			preimage, _ := json.Marshal(map[string]any{"domain": "mooring.atomic.v0.4", "scope": committer.scope, "change_id": change.ID, "operations_without_fingerprints": []any{map[string]any{"op_id": op.ID, "participant_ref": op.Participant, "base_revision": op.BaseRevision, "dependencies": op.Dependencies, "read_preconditions": op.Reads, "payload": op.Payload}}})
			hashes, e := mooring.FingerprintAtomicJSON(preimage)
			if e != nil {
				t.Fatal(e)
			}
			change.MembershipHash = hashes.MembershipHash
			op.Fingerprint = hashes.Operations[op.ID]
			return change
		}
		a := makeChange("actor-a", " actual A", "")
		b := makeChange("actor-b", " actual B", a.Operations[0].ID)
		c := makeChange("actor-c", " retained C", b.Operations[0].ID)
		err = f.humans.runHumanOperation(request, f.joined.admission.participant, func(bound context.Context) error {
			return submissions.run(bound, binding, func(bound context.Context) error { return submissions.sealHuman(bound, []mooring.BatchChange{a, b, c}) })
		})
		if err != nil {
			t.Fatal("original structured seals", err)
		}
		t.Logf("original seals ready elapsed=%s", time.Since(started))
		aSeen := make(chan struct{})
		releaseA := make(chan struct{})
		results := make(chan protocol.SessionCommitResult, 2)
		probe := make(chan protocol.SessionSubmitBatch, 1)
		outputs := make(chan protocol.SessionParticipantOutput, 16)
		var once sync.Once
		defer once.Do(func() { close(releaseA) })
		pump, err := newCollaborationSessionPump(f.cell.runtime, collaborationSessionPumpHandlers{
			Submit: func(bound context.Context, submit protocol.SessionSubmitBatch) (protocol.SessionCommitResult, error) {
				if len(submit.Changes) != 1 {
					return protocol.SessionCommitResult{}, errors.New("unexpected grouped test changes")
				}
				if submit.Changes[0].ChangeID == protocol.Identifier(c.ID) {
					probe <- submit
					return protocol.SessionCommitResult{}, errors.New("intentional unknown C: no SQL execution")
				}
				if submit.Changes[0].ChangeID == protocol.Identifier(a.ID) {
					close(aSeen)
					select {
					case <-releaseA:
					case <-bound.Done():
						return protocol.SessionCommitResult{}, bound.Err()
					}
				}
				if submit.Changes[0].ChangeID == protocol.Identifier(b.ID) && submit.ConfirmedBasis.Revision != "0" {
					return protocol.SessionCommitResult{}, errors.New("B emitted only after A acknowledgement")
				}
				t.Logf("submit %s basis=%s elapsed=%s", submit.Changes[0].ChangeID, submit.ConfirmedBasis.Revision, time.Since(started))
				result, e := committer.submit(bound, submit)
				t.Logf("commit %s error=%v revision=%s elapsed=%s", submit.Changes[0].ChangeID, e, result.Receipt.Revision, time.Since(started))
				if e == nil {
					results <- result
				}
				return result, e
			},
			Status: func(context.Context, protocol.SessionStatusRequest) (protocol.SessionStatusResult, error) {
				return protocol.SessionStatusResult{}, errors.New("status not installed in test")
			},
			Output: func(bound context.Context, out protocol.SessionParticipantOutput) error {
				t.Logf("native output elapsed=%s bytes=%d", time.Since(started), len(out.Message))
				select {
				case outputs <- out:
					return nil
				case <-bound.Done():
					return bound.Err()
				}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		pumpDone := make(chan error, 1)
		pumpStopped := make(chan struct{})
		go func() { defer close(pumpStopped); pumpDone <- pump.run(ctx) }()
		defer func() {
			cancel()
			select {
			case <-pumpStopped:
			case <-time.After(5 * time.Second):
				t.Error("pump failed to stop")
			}
		}()
		send := func(change mooring.BatchChange, id string) {
			raw, _ := json.Marshal([]mooring.BatchChange{change})
			var changes []protocol.AtomicChange
			if e := json.Unmarshal(raw, &changes); e != nil {
				t.Fatal(e)
			}
			inner, _ := json.Marshal(protocol.SubmitChanges{Type: "submit_changes", RequestID: protocol.Identifier(id), Scope: f.opening.Scope, SessionID: f.opening.SessionID, Versions: f.versions, Changes: changes})
			e := f.cell.runtime.SendParticipantInput(ctx, f.cell.activation, protocol.SessionParticipantInput{Type: "session_participant_input", RequestID: protocol.Identifier("private-" + id), ControlProfile: mooring.PrivateSessionProfile, Scope: f.opening.Scope, SessionID: f.opening.SessionID, RuntimeRef: f.opening.RuntimeRef, Fence: f.opening.Fence, ConnectionRef: f.opening.ConnectionRef, ParticipantRef: protocol.Identifier(f.joined.admission.participant), GateID: protocol.Identifier(f.joined.gate.GateID), PermissionVersion: protocol.Counter(f.joined.gate.PermissionVersion), Message: inner})
			if e != nil {
				t.Fatal("actual input", e)
			}
		}
		send(a, "submit-a")
		select {
		case <-aSeen:
		case <-ctx.Done():
			t.Fatal("A not dispatched", ctx.Err())
		}
		send(b, "submit-b")
		// This is a direct witness from the SINGLE original FD reader after
		// validated B was placed on its bounded FIFO, before releasing A.
		// A retained basis alone would not establish arrival before ACK.
		waitB := time.NewTimer(time.Second)
		defer waitB.Stop()
		pollB := time.NewTicker(time.Millisecond)
		defer pollB.Stop()
		for pump.queuedMutations.Load() < 2 {
			select {
			case <-pollB.C:
			case <-waitB.C:
				t.Fatal("B did not arrive on the original FD before A acknowledgement")
			case e := <-pumpDone:
				t.Fatal("pump closed before B enqueue witness", e)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		t.Logf("single-reader B enqueue witnessed before releasing A elapsed=%s", time.Since(started))

		var rows int
		if e := f.x.f.rig.db.QueryRow(`SELECT COUNT(*) FROM mooring_collab_operations WHERE page_id=?`, f.cell.scope.ArtifactID).Scan(&rows); e != nil || rows != 0 {
			t.Fatal("held A published SQL", rows, e)
		}
		once.Do(func() { close(releaseA) })
		var last protocol.SessionCommitResult
		for _, revision := range []protocol.Counter{"1", "2"} {
			select {
			case result := <-results:
				last = result
				if result.Checkpoint.Revision != revision || result.Receipt.Revision != revision || len(result.Receipt.Changes) != 1 || result.Receipt.Changes[0].Status != "saved" {
					t.Fatal("wrong durable receipt", revision, result)
				}
			case e := <-pumpDone:
				t.Fatal("pump failed before SQL settlement", e)
			case <-ctx.Done():
				t.Fatal("SQL settlement timeout", ctx.Err())
			}
		}
		saved := 0
		for saved < 2 {
			select {
			case out := <-outputs:
				var inner struct {
					Type     string           `json:"type"`
					Revision protocol.Counter `json:"revision"`
				}
				if e := json.Unmarshal(out.Message, &inner); e != nil {
					t.Fatal(e)
				}
				if inner.Type == "commit_result" {
					saved++
					if inner.Revision != protocol.Counter(strconv.Itoa(saved)) {
						t.Fatal("native forwarded wrong Saved order", inner)
					}
				}
			case e := <-pumpDone:
				t.Fatal("native settlement closed", e)
			case <-ctx.Done():
				t.Fatal("native Saved timeout", ctx.Err())
			}
		}
		// A subsequent native request must carry B's exact verified FULL basis,
		// proving actor settlement rather than only forwarding host receipt bytes.
		send(c, "probe-c")
		select {
		case next := <-probe:
			if next.ConfirmedBasis.Revision != last.Checkpoint.Revision || next.ConfirmedBasis.Digest != last.Checkpoint.Digest {
				t.Fatal("native confirmed identity did not install B", next.ConfirmedBasis, last.Checkpoint.Digest)
			}
		case <-ctx.Done():
			t.Fatal("native confirmed-basis probe timeout", ctx.Err())
		}
		select {
		case e := <-pumpDone:
			if e == nil {
				t.Fatal("unknown C fabricated success")
			}
		case <-ctx.Done():
			t.Fatal("uncertain C did not close actual child", ctx.Err())
		}
		// C is still an immutable original in this test/client and has no SQL
		// operation or terminal Saved. Its uncertain transport is not rejection.
		if e := f.x.f.rig.db.QueryRow(`SELECT COUNT(*) FROM mooring_collab_operations WHERE page_id=?`, f.cell.scope.ArtifactID).Scan(&rows); e != nil || rows != 2 {
			t.Fatal("durable operations", rows, e)
		}
	})
}

func TestCollaborationProductionActorRefusesIncompatibleSessionOffer(t *testing.T) {
	path := os.Getenv("MOORING_SESSION_BINARY")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
		t.Fatal("bounded pinned binary missing", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	if hex.EncodeToString(hash[:]) != os.Getenv("MOORING_SESSION_BINARY_SHA256") {
		t.Fatal("binary digest mismatch")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runtime, err := mooring.StartSessionRuntime(ctx, mooring.SessionRuntimeConfig{Runtime: mooring.RuntimeConfig{Enabled: true, Binary: path, Versions: mooring.RuntimeVersions{Protocol: "0.4.0", Model: "0.2.0", Engine: "99.0.0"}, ControlProfile: mooring.PrivateSessionProfile}, Scope: mooring.SessionKey{WorkspaceID: "synthetic-workspace", ArtifactID: "incompatible-offer", ApplicationID: "wordcraft"}})
	if err == nil || runtime != nil {
		if runtime != nil {
			_ = runtime.Close()
		}
		t.Fatal("incompatible production session offer reached readiness")
	}
}
