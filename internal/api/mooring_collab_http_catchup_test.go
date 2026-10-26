package api

import (
	"context"
	"encoding/json"
	"errors"
	protocolschema "github.com/crewship-ai/crewship/mooring/protocol"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Synthetic published RAM DATA, not SQL/native/recipient authority evidence.
func savedCatchupFixture(t *testing.T) (*collaborationHTTPConnection, *collaborationLiveOriginal) {
	t.Helper()
	l, g, changes, result, cuts, deltas := originalCommittedDataFixture(t)
	if err := l.publishCommitted(g, changes, result, cuts, deltas); err != nil {
		t.Fatal(err)
	}
	c := g.source.connection
	c.document.owner.Generation = 1
	return c, l.changes[changes[0].ID]
}

func TestMooringSavedCatchupSnapshotOwnsExactPrivateReceiptAndPayload(t *testing.T) {
	c, slot := savedCatchupFixture(t)
	latest, frames, err := c.savedFramesLocked()
	if err != nil || latest != "0" || len(frames) != 1 {
		t.Fatal(latest, frames, err)
	}
	chain, err := collaborationSavedChain(latest, "1", frames)
	if err != nil || len(chain) != 1 {
		t.Fatal(chain, err)
	}
	got := chain[0].update
	if got.RequestID != "actor-update:1" || got.FromRevision != "0" || got.ToRevision != "1" || got.Receipt.RequestID != "private-actor" || got.Receipt.Changes[0].ChangeID != protocol.Identifier(slot.change.ID) {
		t.Fatal(got)
	}
	before, _ := json.Marshal(got)
	slot.group.privateReceipt.Changes[0].Operations[0][0] = 'x'
	*slot.group.privateReceipt.Changes[0].FirstRevision = "99"
	slot.saved.delta.Targets[0] = "changed"
	after, _ := json.Marshal(got)
	if string(before) != string(after) {
		t.Fatal("snapshot aliases live receipts/targets")
	}
	if !strings.Contains(got.Data, `"targets":["p"]`) {
		t.Fatal(got.Data)
	}

}

func TestMooringSavedCatchupSnapshotRejectsUnqualifiedPublications(t *testing.T) {
	for _, mode := range []string{"missing-receipt", "uncaptured", "capture-failure", "unsent", "rejected", "membership", "cut-change", "cut-digest", "first", "last", "op-id", "op-status", "op-count", "duplicate-change"} {
		t.Run(mode, func(t *testing.T) {
			c, slot := savedCatchupFixture(t)
			switch mode {
			case "missing-receipt":
				slot.group.privateReceipt = nil
			case "uncaptured":
				slot.captured = false
			case "capture-failure":
				slot.captureErr = errCollaborationHTTP
			case "unsent":
				slot.group.sent = false
			case "rejected":
				slot.rejected = true
			case "membership":
				slot.saved.cut.MembershipHash = "other"
			case "cut-change":
				slot.saved.cut.ChangeID = "other"
			case "cut-digest":
				slot.saved.cut.StateDigest = "bad"
			case "first":
				*slot.group.privateReceipt.Changes[0].FirstRevision = "01"
			case "last":
				*slot.group.privateReceipt.Changes[0].LastRevision = "2"
			case "op-id":
				slot.group.privateReceipt.Changes[0].Operations[0] = json.RawMessage(`{"op_id":"other","status":"saved"}`)
			case "op-status":
				slot.group.privateReceipt.Changes[0].Operations[0] = json.RawMessage(`{"op_id":"a_op","status":"rejected"}`)
			case "op-count":
				slot.group.privateReceipt.Changes[0].Operations = nil
			case "duplicate-change":
				slot.group.privateReceipt.Changes = append(slot.group.privateReceipt.Changes, slot.group.privateReceipt.Changes[0])
			}
			if _, _, err := c.savedFramesLocked(); err == nil {
				t.Fatal("accepted", mode)
			}
		})
	}
	c, slot := savedCatchupFixture(t)
	slot.saved = nil // Provisional/unsaved data never becomes a catch-up frame.
	_, frames, err := c.savedFramesLocked()
	if err != nil || len(frames) != 0 {
		t.Fatal(frames, err)
	}
	if _, err = collaborationSavedChain("0", "1", frames); err == nil {
		t.Fatal("missing cut skipped")
	}
	c.document.originals.retire(c)
	latest, _, err := c.savedFramesLocked()
	if err != nil || latest != "" {
		t.Fatal("retired source supplied an observed cursor", latest, err)
	}
}

func TestMooringSavedCatchupKeepsMultiOperationAtomicInterval(t *testing.T) {
	c, slot := savedCatchupFixture(t)
	// DATA-only two-operation atomic; unlike a human-only fixture, a typed agent
	// atomic may occupy two revisions but has exactly one actual Saved cut.
	slot.change.Operations = append(slot.change.Operations, mooring.BatchOperation{ID: "second"})
	second := protocol.Counter("2")
	slot.group.privateReceipt.Changes[0].LastRevision = &second
	slot.group.privateReceipt.Revision = second
	slot.group.privateReceipt.Changes[0].Operations = append(slot.group.privateReceipt.Changes[0].Operations, json.RawMessage(`{"op_id":"second","status":"saved","revision":"2"}`))
	slot.saved.cut.Revision = "2"
	latest, frames, err := c.savedFramesLocked()
	if err != nil || len(frames) != 1 || frames[0].update.FromRevision != "0" || frames[0].update.ToRevision != "2" {
		t.Fatal(frames, err)
	}
	chain, err := collaborationSavedChain(latest, "2", frames)
	if err != nil || len(chain) != 1 {
		t.Fatal(chain, err)
	}
	if _, err = collaborationSavedChain(latest, "1", frames); err == nil {
		t.Fatal("fabricated intermediate cut1")
	}
}

func TestMooringSavedCatchupChainRequiresExactContiguousAtomicCuts(t *testing.T) {
	frame := func(from, to string) collaborationSavedFrame {
		return collaborationSavedFrame{update: protocol.DocumentUpdate{FromRevision: protocol.Counter(from), ToRevision: protocol.Counter(to)}}
	}
	// A multi-op 0->2 cut followed by 2->3 and 3->4. Incoming 3->4 must first
	// replay precisely 0->2,2->3; the incoming frame itself remains unchanged.
	base := []collaborationSavedFrame{frame("0", "2"), frame("2", "3"), frame("3", "4")}
	chain, err := collaborationSavedChain("0", "3", base)
	if err != nil || len(chain) != 2 || chain[0].update.ToRevision != "2" || chain[1].update.FromRevision != "2" {
		t.Fatal(chain, err)
	}
	for _, tc := range []struct {
		name, latest, target string
		frames               []collaborationSavedFrame
	}{
		{"missing", "0", "3", base[1:]},
		{"ambiguous", "0", "3", []collaborationSavedFrame{base[0], base[0], base[1]}},
		{"overlap", "1", "3", base},
		{"intermediate", "0", "1", base},
		{"unknown-tail", "0", "5", base},
		{"zero-width", "0", "3", []collaborationSavedFrame{frame("0", "0"), base[1]}},
		{"leading-zero", "00", "3", base},
		{"overflow", "0", "18446744073709551616", base},
		{"capacity", "0", "3", make([]collaborationSavedFrame, collaborationOriginalRecordLimit+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := collaborationSavedChain(tc.latest, tc.target, tc.frames); err == nil {
				t.Fatal("accepted incomplete/ambiguous chain")
			}
		})
	}
	chain, err = collaborationSavedChain("4", "3", base)
	if err != nil || len(chain) != 0 {
		t.Fatal("newer delivered snapshot regressed", chain, err)
	}
}

// Exact planning path called by the real afterJoined callback. Synthetic RAM
// records prove quiet-reader DATA selection, not a SQL/native/currentgate grant.
func TestMooringSavedCatchupQuietReaderNoFutureWriteAndUninitialized(t *testing.T) {
	c, _ := savedCatchupFixture(t)
	chain, err := c.savedCatchupPlanLocked()
	if err != nil || len(chain) != 1 || chain[0].update.FromRevision != "0" || chain[0].update.ToRevision != "1" {
		t.Fatal(chain, err)
	}
	if len(c.document.originals.sources[c].observed) != 1 {
		t.Fatal("planning advanced cursor")
	}
	// Initial Joined may rotate before any physically delivered FULL checkpoint.
	// Its metadata continuation cannot invent a basis or cancel a healthy reader.
	delete(c.document.originals.sources, c)
	chain, err = c.savedCatchupPlanLocked()
	if err != nil || len(chain) != 0 || len(c.document.originals.sources) != 0 {
		t.Fatal("uninitialized reader was invented/failed", chain, err)
	}
}

func TestMooringSavedCatchupCancelledClosedAndNewerRotationNoAuthority(t *testing.T) {
	c, _ := savedCatchupFixture(t)
	var cancellations atomic.Int32
	c.document.cancel = func() { cancellations.Add(1) }
	before := len(c.document.originals.sources[c].observed)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.catchUpAfterJoined(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := c.sendSavedChain(ctx, []collaborationSavedFrame{{}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	c.closed = true
	if err := c.catchUpAfterJoined(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := c.ensureCurrentGateForOutput(t.Context(), true); err != errCollaborationRecipientDenied {
		t.Fatal(err)
	}
	if cancellations.Load() != 0 || len(c.document.originals.sources[c].observed) != before {
		t.Fatal("closed/cancelled DATA changed document/cursor")
	}
	c.closed = false
	// Leaf state after ensure returned but BEFORE the next c.mu critical section:
	// a new legitimate transition owns its future Joined catch-up obligation.
	c.transition = &collaborationGateTransition{ready: make(chan struct{})}
	if !c.savedRecipientDeferredLocked() {
		t.Fatal("newer rotation not deferred")
	}
	if err := c.catchUpAfterJoined(t.Context()); err != nil {
		t.Fatal(err)
	}
	if cancellations.Load() != 0 || len(c.document.originals.sources[c].observed) != before {
		t.Fatal("newer transition changed document/cursor")
	}
}

func TestMooringSavedCatchupEachFrameRechecksDeliveredCursor(t *testing.T) {
	frame := collaborationSavedFrame{update: protocol.DocumentUpdate{FromRevision: "2", ToRevision: "4"}}
	if needed, err := collaborationSavedFrameNeeded("2", frame); err != nil || !needed {
		t.Fatal(needed, err)
	}
	// A concurrent current FULL checkpoint has covered this atomic while locks
	// were released for management. The whole cut is suppressed, never rewound.
	if needed, err := collaborationSavedFrameNeeded("5", frame); err != nil || needed {
		t.Fatal(needed, err)
	}
	for _, latest := range []string{"", "1", "3"} {
		if _, err := collaborationSavedFrameNeeded(latest, frame); err == nil {
			t.Fatal("skipped missing/partial interval", latest)
		}
	}
}

func TestMooringSavedCatchupConstructedFrameClosedPublicSchema(t *testing.T) {
	c, _ := savedCatchupFixture(t)
	schema, err := mooring.NewWireSchema(protocolschema.JSON())
	if err != nil {
		t.Fatal(err)
	}
	c.document.manager = &collaborationHTTPManager{schema: schema}
	_, frames, err := c.savedFramesLocked()
	if err != nil {
		t.Fatal(err)
	}
	frame := frames[0]
	frame.update.SessionID = "document-session"
	raw, err := c.encodeSavedFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	var decoded protocol.DocumentUpdate
	if json.Unmarshal(raw, &decoded) != nil || !collaborationEqualJSONReceipt(decoded.Receipt, &frame.update.Receipt) {
		t.Fatal("receipt altered")
	}
	for _, mode := range []string{"invalid-session", "invalid-generation", "oversize", "raw-extra", "raw-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			bad := frame
			switch mode {
			case "invalid-session":
				bad.update.SessionID = ""
			case "invalid-generation":
				bad.update.Generation = "01"
			case "oversize":
				bad.update.Data = strings.Repeat("x", collaborationHTTPFrameLimit)
			case "raw-extra":
				bad.raw = append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"extra":true}`)...)
			case "raw-mismatch":
				bad.raw = raw
				bad.update.ToRevision = "2"
			}
			if _, err := c.encodeSavedFrame(bad); err == nil {
				t.Fatal("bad public wire accepted")
			}
		})
	}
}
