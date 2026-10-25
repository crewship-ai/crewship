package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Controlled existing production failure paths; no SQL/native grant or Saved
// qualification. The caller cancels immediately after return, as the pump does.
func TestMooringHostFirstFailurePrecedesCallerCleanup(t *testing.T) {
	for _, mode := range []string{"output-binding", "send-precondition", "closed-nil-request"} {
		t.Run(mode, func(t *testing.T) {
			observer := new(collaborationHostFailure)
			d := &collaborationHTTPDocument{manager: &collaborationHTTPManager{hostFailure: observer}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var err error
			want := hostOutputBinding
			if mode == "output-binding" {
				// Wrong original scope fails before accessing any missing runtime or SQL.
				err = d.outputControl(ctx, mooring.SessionControl{}, protocol.SessionParticipantOutput{Scope: protocol.Scope{ArtifactID: "foreign"}})
			} else {
				want = hostSendPrecondition
				c := &collaborationHTTPConnection{document: d, closed: true}
				if mode == "closed-nil-request" {
					err = c.checkedSendLocked(nil) // Original c.request is nil; old early refusal must not panic.
				} else {
					err = c.checkedSendRequestLocked(httptest.NewRequest("GET", "/", nil).WithContext(ctx), nil)
				}
			}
			if err != errCollaborationHTTP {
				t.Fatal("original error identity changed")
			}
			before := observer.first.Load()
			if before != uint32(want) {
				t.Fatal("inner failure not captured before return", before)
			}
			cancel()
			observer.cleanup.Store(true)
			p := &collaborationSessionPump{hostFailure: observer}
			if got := p.observeHostFailure(ctx, hostPumpOutput, err); got != err {
				t.Fatal("pump changed error identity")
			}
			if observer.first.Load() != before {
				t.Fatal("caller cancellation/cleanup replaced primary failure")
			}
		})
	}
}

func TestMooringHostFirstFailureNoErrorOrUnknownPayloadRetention(t *testing.T) {
	o := new(collaborationHostFailure)
	p := &collaborationSessionPump{hostFailure: o}
	if p.observeHostFailure(t.Context(), hostPumpOutput, nil) != nil || o.first.Load() != 0 {
		t.Fatal("success recorded")
	}
	secret := errors.New("synthetic-private-content-never-retained")
	if p.observeHostFailure(t.Context(), hostPumpOutput, secret) != secret {
		t.Fatal("error replaced")
	}
	if collaborationHostStageName(o.first.Load()) != "pump_output" {
		t.Fatal("wrong finite stage")
	}
	typ := reflect.TypeOf(o).Elem()
	if typ.NumField() != 2 || typ.Field(0).Type.String() != "atomic.Uint32" || typ.Field(1).Type.String() != "atomic.Bool" {
		t.Fatal("observer can retain variable/raw data")
	}
	o.record(t.Context(), collaborationHostStage(1<<28))
	if o.first.Load() != uint32(hostPumpOutput) {
		t.Fatal("unknown stage overwrote first")
	}
	if collaborationHostStageName(1<<28) != "invalid" {
		t.Fatal("unknown value formatted")
	}
	var absent *collaborationHostFailure
	absent.record(t.Context(), hostPumpOutput)
}

func TestMooringHostFirstFailureCancelledCleanupIsExplicitAndBounded(t *testing.T) {
	o := new(collaborationHostFailure)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	o.cleanup.Store(true)
	o.record(ctx, hostPumpRead)
	want := uint32(hostPumpRead) | hostCancelledBit | hostCleanupBit
	if o.first.Load() != want {
		t.Fatal("cleanup observation disguised as primary")
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1000 {
				o.record(t.Context(), hostOutputSchema)
			}
		}()
	}
	wg.Wait()
	if o.first.Load() != want {
		t.Fatal("first observation replaced under contention")
	}
}

// Actual transition helper's existing refusal and cancel path, without SQL or a
// synthesized grant. The cancel function inspects the observer BEFORE cancelling.
func TestMooringHostFirstFailureGateRecordedBeforeExistingCancel(t *testing.T) {
	for _, trigger := range []bool{false, true} {
		o := new(collaborationHostFailure)
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		d := &collaborationHTTPDocument{manager: &collaborationHTTPManager{hostFailure: o}}
		d.cancel = func() {
			calls++
			if o.first.Load() != uint32(hostGateTransitionFailure) || ctx.Err() != nil {
				t.Error("primary boundary lost before existing cancel")
			}
			cancel()
		}
		c := &collaborationHTTPConnection{document: d, closed: true}
		err := c.ensureCurrentGateForOutput(ctx, trigger)
		if trigger {
			if err != errCollaborationRecipientDenied || calls != 0 || o.first.Load() != 0 {
				t.Fatal("known recipient denial changed")
			}
		} else if err != errCollaborationHTTP || calls != 1 || ctx.Err() != context.Canceled {
			t.Fatal("original refusal/cancel changed")
		}
		cancel()
	}
}
