//go:build linux

package api

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Actual inherited-FD MOCK child plus real pump and real output callback. No
// current SQL rights/native/Saved qualification. A valid transport output reaches
// the callback with a different original document binding and must fail there.
func TestMooringHostFirstFailureActualPumpBeforeCancellation(t *testing.T) {
	o := new(collaborationHostFailure)
	d := &collaborationHTTPDocument{manager: &collaborationHTTPManager{hostFailure: o}}
	observed := make(chan error, 1)
	r := newConfiguredPumpTestRig(t, "output", "", "", func(h *collaborationSessionPumpHandlers) {
		h.hostFailure = o
		h.OutputControl = func(ctx context.Context, token mooring.SessionControl, q protocol.SessionParticipantOutput) error {
			err := d.outputControl(ctx, token, q)
			if err != errCollaborationHTTP {
				t.Error("original callback error identity changed")
			}
			if o.first.Load() != uint32(hostOutputBinding) {
				t.Error("primary callback was not observed while context live")
			}
			observed <- err
			return err
		}
	})
	if pumpWait(t, r.ctx, observed) != errCollaborationHTTP {
		t.Fatal("callback did not reject original mismatch")
	}
	// Wait on the rig's existing bound; no new sleep, deadline, or early cleanup.
	err := pumpWait(t, r.ctx, r.done)
	r.done <- err // Preserve the rig's original cleanup consumer.
	if err != errCollaborationSessionPump {
		t.Fatal("original pump error changed", err)
	}
	if o.first.Load() != uint32(hostOutputBinding) {
		t.Fatal("pump/runtime cleanup overwrote primary failure")
	}
}
