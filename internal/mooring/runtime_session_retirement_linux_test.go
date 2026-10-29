//go:build linux

package mooring

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Real inherited Go child and FULL owner/gate SQL; MOCK participant/application
// semantics. These tests qualify correlation/accounting, not source authority.
type retirementTestPeer struct {
	t      *testing.T
	write  func(any)
	mode   string
	open   map[string]any
	old    map[string]any
	joins  int
	left   bool
	grants int
}

func (p *retirementTestPeer) handle(v map[string]any) {
	switch v["type"] {
	case "session_participant_open":
		p.open = v
		join := v["join"].(map[string]any)
		if p.mode == "retirement-fake-joined" {
			p.joined("invented-gate", "1")
			return
		}
		p.write(map[string]any{"type": "ticket_validate", "request_id": "ticket-" + v["request_id"].(string), "scope": v["scope"], "session_id": v["session_id"], "fence": v["fence"], "ticket": join["ticket"], "validation_nonce": "nonce-" + v["request_id"].(string)})
	case "ticket_validated":
		p.joined(v["gate_id"], v["permission_version"])
		p.joins++
		if p.mode == "retirement-repeated" {
			return
		}
		if p.joins == 1 {
			o := p.open
			p.write(map[string]any{"type": "delivery_renew", "request_id": "old-renew", "scope": o["scope"], "session_id": o["session_id"], "fence": o["fence"], "participant_ref": "stable-participant", "gate_id": v["gate_id"], "permission_version": v["permission_version"], "renewal_nonce": "old-renew-nonce"})
			p.old = p.output("old-output", v["gate_id"], v["permission_version"], map[string]any{"type": "changes_received", "request_id": "original-public", "scope": o["scope"], "session_id": o["session_id"], "change_ids": []string{"original-change"}, "state": "pending"})
			p.write(p.old)
		} else if p.mode == "retirement-late-old" {
			p.old["request_id"] = "late-old-output"
			p.write(p.old)
		}
	case "delivery_grant":
		if p.mode == "retirement-overlap" {
			if p.left || v["gate_id"] != p.old["gate_id"] {
				p.t.Fatal("old delivery grant was physically written after Leave or changed gate")
			}
			p.grants++
		}
	case "protocol_error":
		if p.mode != "retirement-decline" || v["request_id"] != "ticket-"+p.open["request_id"].(string) || v["reason"].(map[string]any)["code"] != "permission_revoked" {
			p.t.Fatal("unexpected negative admission")
		}
		o := p.open
		p.write(map[string]any{"type": "owner_renew", "request_id": "after-negative-physical-write", "scope": o["scope"], "session_id": o["session_id"], "fence": o["fence"], "renewal_nonce": "after-negative", "permission_version": "1"})
	case "session_participant_input":
		if v["message"].(map[string]any)["type"] != "session_leave" {
			p.t.Fatal("unexpected retirement input")
		}
		if p.mode == "retirement-overlap" && p.grants != 1 {
			p.t.Fatal("Leave did not follow the one already reserved grant")
		}
		p.left = true
	}
}
func (p *retirementTestPeer) output(id string, gate, permission any, message any) map[string]any {
	o := p.open
	return map[string]any{"type": "session_participant_output", "request_id": id, "control_profile": PrivateSessionProfile, "scope": o["scope"], "session_id": o["session_id"], "runtime_ref": o["runtime_ref"], "fence": o["fence"], "connection_ref": o["connection_ref"], "participant_ref": "stable-participant", "gate_id": gate, "permission_version": permission, "message": message}
}
func (p *retirementTestPeer) joined(gate, permission any) {
	o := p.open
	join := o["join"].(map[string]any)
	p.write(p.output(o["request_id"].(string), gate, permission, map[string]any{"type": "session_joined", "request_id": join["request_id"], "scope": o["scope"], "session_id": o["session_id"], "participant_ref": "stable-participant", "generation": o["fence"].(map[string]any)["generation"], "permission_version": permission, "versions": sessionTestVersions, "gate_id": gate}))
}
func retirementControl(t *testing.T, s *SessionRuntime) SessionControl {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	c, e := s.NextControl(ctx)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func retirementReply(t *testing.T, f originFixture, c SessionControl, gate DeliveryGrant) {
	t.Helper()
	raw, e := c.Frame()
	if e != nil {
		t.Fatal(e)
	}
	var q protocol.TicketValidationRequest
	if json.Unmarshal(raw, &q) != nil {
		t.Fatal("ticket shape")
	}
	yes := true
	result := protocol.TicketValidationResult{Type: "ticket_validated", RequestID: q.RequestID, Scope: q.Scope, SessionID: q.SessionID, Generation: q.Fence.Generation, ParticipantRef: "stable-participant", PermissionVersion: protocol.Counter(gate.PermissionVersion), Granted: protocol.Capabilities{Read: &yes, Write: &yes}, Versions: f.runtime.wireVersions(), ExpiresInMs: protocol.PositiveCounter(strconv.FormatInt(gate.DurationMilliseconds, 10)), ValidationNonce: q.ValidationNonce, GateID: protocol.Identifier(gate.GateID)}
	raw, e = json.Marshal(result)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.runtime.ReplyControl(t.Context(), c, raw); e != nil {
		t.Fatal(e)
	}
}
func retirementOpen(t *testing.T, f originFixture, id string) protocol.SessionParticipantOpen {
	t.Helper()
	q, _ := participantTransportRequests(t, f)
	q.RequestID = protocol.Identifier(id)
	q.Join, _ = json.Marshal(protocol.SessionJoin{Type: "session_join", RequestID: protocol.Identifier("join-" + id), Ticket: "ticket-" + id, Versions: f.runtime.wireVersions()})
	return q
}
func TestSessionParticipantRetirementRejectsChildJoinedWithoutPhysicalTicket(t *testing.T) {
	f := sessionOriginsFixture(t, "retirement-fake-joined", false)
	pending, e := f.runtime.SendTrackedParticipantOpen(t.Context(), f.activation, retirementOpen(t, f, "initial"))
	if e != nil {
		t.Fatal(e)
	}
	c := retirementControl(t, f.runtime)
	if _, e = f.runtime.CaptureParticipantJoined(f.activation, pending, c); e == nil {
		t.Fatal("child-only Joined became actual participant binding")
	}
}
func TestSessionParticipantRetirementWatermarkDrainsBorrowedOutputs(t *testing.T) {
	for _, mode := range []string{"retirement-valid", "retirement-late-old", "retirement-denied", "retirement-old-grant"} {
		t.Run(mode, func(t *testing.T) {
			f := sessionOriginsFixture(t, mode, false)
			version := "1"
			owners := f.activation.store.config.Owners
			delivery := NewDeliveryStore(owners, true, func(context.Context, *sql.Tx, DeliveryAccess) (string, error) { return version, nil }, 2*time.Second, 16)
			gate, e := delivery.Open(t.Context(), f.owner, "stable-participant", "initial-gate")
			if e != nil {
				t.Fatal(e)
			}
			pending, e := f.runtime.SendTrackedParticipantOpen(t.Context(), f.activation, retirementOpen(t, f, "initial"))
			if e != nil {
				t.Fatal(e)
			}
			retirementReply(t, f, retirementControl(t, f.runtime), gate)
			joined := retirementControl(t, f.runtime)
			binding, e := f.runtime.CaptureParticipantJoined(f.activation, pending, joined)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.runtime.CaptureParticipantJoined(f.activation, pending, joined); e == nil {
				t.Fatal("duplicate Joined capability")
			}
			if e = f.runtime.CompleteControl(joined); e != nil {
				t.Fatal(e)
			}
			renewal, output := retirementControl(t, f.runtime), retirementControl(t, f.runtime)
			if e = f.runtime.CompleteControl(renewal); e == nil {
				t.Fatal("generic request completion weakened")
			}
			version = "2"
			if _, e = delivery.Renew(t.Context(), f.owner, gate.GateID, "epoch-change"); !errors.Is(e, ErrDeliveryClosed) {
				t.Fatal(e)
			}
			retired, e := f.runtime.RetireParticipant(t.Context(), f.activation, binding)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "retirement-old-grant" {
				raw, _ := renewal.Frame()
				var q protocol.DeliveryRenew
				_ = json.Unmarshal(raw, &q)
				grant := protocol.DeliveryGrant{Type: "delivery_grant", RequestID: q.RequestID, Scope: q.Scope, SessionID: q.SessionID, ParticipantRef: q.ParticipantRef, Generation: q.Fence.Generation, PermissionVersion: q.PermissionVersion, RenewalNonce: q.RenewalNonce, GateID: q.GateID, DurationMs: "2000"}
				raw, _ = json.Marshal(grant)
				if err := f.runtime.ReplyControl(t.Context(), renewal, raw); err == nil {
					t.Fatal("old positive delivery grant physically written after Leave")
				}
				if err := f.runtime.Err(); err != nil {
					t.Fatal("refusing late retired renewal killed shared runtime", err)
				}
				// Continue through denial completion, fresh admission and the
				// physical Joined watermark: the owner must remain usable.
			}
			if e = f.runtime.CompleteRetiredDeliveryControl(output, retired); e == nil {
				t.Fatal("notification used as renewal cancellation")
			}
			if e = f.runtime.CompleteRetiredDeliveryControl(renewal, retired); e != nil {
				t.Fatal(e)
			}
			if e = f.runtime.CompleteRetiredDeliveryControl(renewal, retired); e == nil {
				t.Fatal("double cancellation changed accounting")
			}
			if e = f.runtime.ValidateRetiredOutput(output, retired); e != nil {
				t.Fatal(e)
			}
			if mode == "retirement-denied" {
				if retired.Done() == nil {
					t.Fatal("actual retirement has no lifetime signal")
				}
				select {
				case <-retired.Done():
					t.Fatal("denied gate without watermark completed lifetime")
				default:
				}
				defer func() {
					_ = f.runtime.Close()
					select {
					case <-retired.Done():
					default:
						t.Fatal("shutdown did not end retirement lifetime")
					}
				}()

				if e = f.runtime.CompleteControl(output); e != nil {
					t.Fatal(e)
				}
				f.runtime.mu.Lock()
				retained := f.runtime.participantStateLocked(retired.state)
				f.runtime.mu.Unlock()
				if !retained {
					t.Fatal("empty table mistaken for denied-recipient watermark")
				}
				return
			}
			fresh, e := delivery.Open(t.Context(), f.owner, "stable-participant", "fresh-gate")
			if e != nil {
				t.Fatal(e)
			}
			if mode == "retirement-valid" {
				copy := *f.activation
				if _, err := f.runtime.SendParticipantReopen(t.Context(), &copy, retired, retirementOpen(t, f, "copied-activation")); err == nil {
					t.Fatal("copied activation reopened original participant")
				}
			}
			next, e := f.runtime.SendParticipantReopen(t.Context(), f.activation, retired, retirementOpen(t, f, "replacement"))
			if e != nil {
				t.Fatal(e)
			}
			retirementReply(t, f, retirementControl(t, f.runtime), fresh)
			mark := retirementControl(t, f.runtime)
			if mode == "retirement-valid" {
				// A failed predecessor validation must not partially publish Joined.
				f.runtime.mu.Lock()
				retired.state.successor = nil
				f.runtime.mu.Unlock()
				if _, err := f.runtime.CaptureParticipantJoined(f.activation, next, mark); err == nil {
					t.Fatal("broken predecessor became Joined")
				}
				f.runtime.mu.Lock()
				partiallyCaptured := next.state.captured || next.state.participant != "" || next.state.gate != "" || retired.state.watermark != 0
				retired.state.successor = next.state
				f.runtime.mu.Unlock()
				if partiallyCaptured {
					t.Fatal("failed Joined partially published tuple/watermark")
				}
			}
			if _, e = f.runtime.CaptureParticipantJoined(f.activation, next, mark); e != nil {
				t.Fatal(e)
			}
			if e = f.runtime.ValidateRetiredOutput(output, retired); e != nil {
				t.Fatal("borrowed output lost at Joined watermark", e)
			}
			if mode == "retirement-late-old" {
				late := retirementControl(t, f.runtime)
				if e = f.runtime.ValidateRetiredOutput(late, retired); e == nil {
					t.Fatal("post-watermark old tuple accepted")
				}
				return // Protocol failure: do not claim this malicious tail drained.
			}
			if e = f.runtime.CompleteControl(output); e != nil {
				t.Fatal(e)
			}
			f.runtime.mu.Lock()
			released := retired.state.released
			f.runtime.mu.Unlock()
			if !released {
				t.Fatal("watermarked drained retirement leaked its charge")
			}
			if e = f.runtime.ValidateRetiredOutput(output, retired); e == nil {
				t.Fatal("released proof reused")
			}
			if e = f.runtime.CompleteControl(mark); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestSessionParticipantRetirementReleasesAncestryAndShutdown(t *testing.T) {
	f := sessionOriginsFixture(t, "retirement-repeated", false)
	version := "1"
	delivery := NewDeliveryStore(f.activation.store.config.Owners, true, func(context.Context, *sql.Tx, DeliveryAccess) (string, error) { return version, nil }, 2*time.Second, 64)
	gate, err := delivery.Open(t.Context(), f.owner, "stable-participant", "initial")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := f.runtime.SendTrackedParticipantOpen(t.Context(), f.activation, retirementOpen(t, f, "initial"))
	if err != nil {
		t.Fatal(err)
	}
	retirementReply(t, f, retirementControl(t, f.runtime), gate)
	joined := retirementControl(t, f.runtime)
	binding, err := f.runtime.CaptureParticipantJoined(f.activation, pending, joined)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.runtime.CompleteControl(joined); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 24; i++ {
		version = strconv.Itoa(i + 1)
		if _, err = delivery.Renew(t.Context(), f.owner, gate.GateID, "close-"+version); !errors.Is(err, ErrDeliveryClosed) {
			t.Fatal(err)
		}
		retired, e := f.runtime.RetireParticipant(t.Context(), f.activation, binding)
		if e != nil {
			t.Fatal(e)
		}
		gate, err = delivery.Open(t.Context(), f.owner, "stable-participant", "fresh-"+version)
		if err != nil {
			t.Fatal(err)
		}
		pending, err = f.runtime.SendParticipantReopen(t.Context(), f.activation, retired, retirementOpen(t, f, "open-"+version))
		if err != nil {
			t.Fatal(err)
		}
		retirementReply(t, f, retirementControl(t, f.runtime), gate)
		joined = retirementControl(t, f.runtime)
		binding, err = f.runtime.CaptureParticipantJoined(f.activation, pending, joined)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.runtime.CompleteControl(joined); err != nil {
			t.Fatal(err)
		}
		f.runtime.mu.Lock()
		ancestor := binding.state.predecessor
		select {
		case <-retired.Done():
		default:
			t.Fatal("drained metadata lifetime signal open")
		}
		retainedPayload := len(retired.state.open.Join) + len(retired.state.ticketBytes) + len(retired.state.join.Ticket)
		count, charged, states := f.runtime.count, f.runtime.bytes, len(f.runtime.participantStates)
		f.runtime.mu.Unlock()
		if ancestor != nil || retainedPayload != 0 || count != 1 || charged != participantBindingCharge || states != 1 {
			t.Fatalf("transition%d retains released ancestry/payload or leaked accounting: ancestor=%v payload=%d count=%d bytes=%d states=%d", i, ancestor != nil, retainedPayload, count, charged, states)
		}
	}
	_ = f.runtime.Close()
	f.runtime.mu.Lock()
	count, charged, states := f.runtime.count, f.runtime.bytes, len(f.runtime.participantStates)
	released := binding.state.released
	f.runtime.mu.Unlock()
	if count != 0 || charged != 0 || states != 0 || !released {
		t.Fatalf("shutdown retained participant metadata: count=%d bytes=%d states=%d released=%v", count, charged, states, released)
	}
}

func TestSessionParticipantPreparedOpenPublishAndDiscard(t *testing.T) {
	f := sessionOriginsFixture(t, "retirement-repeated", false)
	q := retirementOpen(t, f, "discarded")
	p, err := f.runtime.PrepareParticipantOpen(f.activation, q)
	if err != nil {
		t.Fatal(err)
	}
	// Holding the real single writer excludes all Open I/O while discard races
	// a sender waiting for it. A discarded Pending cannot later become sent.
	<-f.runtime.writer
	result := make(chan error, 1)
	go func() { result <- f.runtime.SendPreparedParticipantOpen(t.Context(), f.activation, p) }()
	if err = f.runtime.DiscardUnsentParticipantOpen(p); err != nil {
		t.Fatal(err)
	}
	f.runtime.writer <- struct{}{}
	if err = <-result; err == nil {
		t.Fatal("discarded Pending reached writer")
	}
	if err = f.runtime.DiscardUnsentParticipantOpen(p); err == nil {
		t.Fatal("discard twice released accounting")
	}
	f.runtime.mu.Lock()
	count, charged := f.runtime.count, f.runtime.bytes
	f.runtime.mu.Unlock()
	if count != 0 || charged != 0 {
		t.Fatalf("discard accounting=%d/%d", count, charged)
	}
	// The exact Pending is published before physical send. The real child's
	// first observed Open must be this one, never the discarded predecessor.
	p, err = f.runtime.PrepareParticipantOpen(f.activation, retirementOpen(t, f, "published"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.runtime.SendPreparedParticipantOpen(t.Context(), f.activation, p); err != nil {
		t.Fatal(err)
	}
	if err = f.runtime.DiscardUnsentParticipantOpen(p); err == nil {
		t.Fatal("physically sent Open became discardable")
	}
	c := retirementControl(t, f.runtime)
	raw, err := c.Frame()
	if err != nil {
		t.Fatal(err)
	}
	var ticket protocol.TicketValidationRequest
	if json.Unmarshal(raw, &ticket) != nil || ticket.Ticket != "ticket-published" {
		t.Fatal("discard emitted stale Open")
	}
	if _, err = f.runtime.CaptureParticipantJoined(f.activation, p, c); err == nil {
		t.Fatal("ticket request became Joined")
	}
}

func TestSessionParticipantPreparedOpenSharedCapacityBeforeIO(t *testing.T) {
	f := sessionOriginsFixture(t, "retirement-repeated", false)
	// Metadata is charged to the existing shared count, not a second queue.
	// Leave a count slot for its transient physical frame; upper bound128 does
	// not promise128 simultaneously tracked participants.
	f.runtime.mu.Lock()
	for i := 0; i < sessionControlCount-1; i++ {
		if !f.runtime.reserveLocked(1) {
			t.Fatal("test reservation")
		}
	}
	f.runtime.mu.Unlock()
	p, err := f.runtime.PrepareParticipantOpen(f.activation, retirementOpen(t, f, "capacity"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.runtime.SendPreparedParticipantOpen(t.Context(), f.activation, p); err == nil {
		t.Fatal("Open bypassed shared full count")
	}
	if p.state.writing || p.state.sent {
		t.Fatal("capacity failure started I/O")
	}
	if err = f.runtime.DiscardUnsentParticipantOpen(p); err != nil {
		t.Fatal(err)
	}
	f.runtime.mu.Lock()
	count, charged := f.runtime.count, f.runtime.bytes
	f.runtime.count -= sessionControlCount - 1
	f.runtime.bytes -= sessionControlCount - 1
	f.runtime.mu.Unlock()
	if count != sessionControlCount-1 || charged != sessionControlCount-1 {
		t.Fatalf("capacity accounting=%d/%d", count, charged)
	}
}

func TestSessionParticipantTicketDeclineIsExactPhysicalNegativeOnly(t *testing.T) {
	f := sessionOriginsFixture(t, "retirement-decline", false)
	pending, err := f.runtime.SendTrackedParticipantOpen(t.Context(), f.activation, retirementOpen(t, f, "declined"))
	if err != nil {
		t.Fatal(err)
	}
	token := retirementControl(t, f.runtime)
	frame, err := token.Frame()
	if err != nil {
		t.Fatal(err)
	}
	var q protocol.TicketValidationRequest
	if json.Unmarshal(frame, &q) != nil {
		t.Fatal("ticket shape")
	}
	raw, _ := json.Marshal(protocol.ProtocolError{Type: "protocol_error", RequestID: q.RequestID, Scope: &q.Scope, Reason: protocol.Rejection{Code: "permission_revoked", Message: "participant permission revoked", Retry: "do_not_retry"}})
	if err = f.runtime.ReplyControl(t.Context(), token, raw); err == nil {
		t.Fatal("generic reply admitted protocol error")
	}
	if err = f.runtime.CompleteControl(token); err == nil {
		t.Fatal("generic completion abandoned ticket")
	}
	copiedActivation := *f.activation
	if err = f.runtime.DeclineParticipantTicket(t.Context(), &copiedActivation, pending, token); err == nil {
		t.Fatal("copied activation declined original")
	}
	copiedPending := *pending
	if err = f.runtime.DeclineParticipantTicket(t.Context(), f.activation, &copiedPending, token); err == nil {
		t.Fatal("copied pending declined original")
	}
	if err = f.runtime.DeclineParticipantTicket(t.Context(), f.activation, pending, SessionControl{}); err == nil {
		t.Fatal("foreign token declined original")
	}
	if err = f.runtime.DeclineParticipantTicket(t.Context(), f.activation, pending, token); err != nil {
		t.Fatal(err)
	}
	// The inherited child emitted this marker only AFTER reading the negative.
	after := retirementControl(t, f.runtime)
	marker, err := after.Frame()
	if err != nil || !strings.Contains(string(marker), "after-negative-physical-write") {
		t.Fatal("no physical negative observed")
	}
	if _, err = token.Frame(); err == nil {
		t.Fatal("negative ticket remained borrowable")
	}
	if err = f.runtime.DeclineParticipantTicket(t.Context(), f.activation, pending, token); err == nil {
		t.Fatal("duplicate decline completed again")
	}
	if _, err = f.runtime.CaptureParticipantJoined(f.activation, pending, after); err == nil {
		t.Fatal("negative invented Joined")
	}
	f.runtime.mu.Lock()
	declined, captured, states := pending.state.declined, pending.state.captured, len(f.runtime.participantStates)
	f.runtime.mu.Unlock()
	if !declined || captured || states != 1 {
		t.Fatal("negative minted or released admission")
	}
	select {
	case <-pending.state.done:
		t.Fatal("negative mistaken for Joined watermark")
	default:
	}
}

func TestSessionParticipantTicketDeclineCanceledWriteNeverCompletes(t *testing.T) {
	f := sessionOriginsFixture(t, "retirement-decline", false)
	pending, err := f.runtime.SendTrackedParticipantOpen(t.Context(), f.activation, retirementOpen(t, f, "canceled"))
	if err != nil {
		t.Fatal(err)
	}
	token := retirementControl(t, f.runtime)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err = f.runtime.DeclineParticipantTicket(ctx, f.activation, pending, token); err == nil {
		t.Fatal("canceled negative succeeded")
	}
	select {
	case <-f.runtime.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("uncertain negative kept child alive")
	}
	f.runtime.mu.Lock()
	entry := f.runtime.child[token.id]
	f.runtime.mu.Unlock()
	if entry == nil {
		t.Fatal("failed negative completed original ticket")
	}
}

func TestSessionParticipantRetirementOverlapsQueuedDeliveryReply(t *testing.T) {
	for _, stop := range []string{"finish", "cancel", "close"} {
		t.Run(stop, func(t *testing.T) { runRetirementDeliveryOverlap(t, stop) })
	}
}

func runRetirementDeliveryOverlap(t *testing.T, stop string) {
	f := sessionOriginsFixture(t, "retirement-overlap", false)
	if dir := os.Getenv("CREWSHIP_TEST_FAILURE_SNAPSHOT_DIR"); dir != "" {
		t.Cleanup(func() {
			if t.Failed() {
				if err := database.SnapshotTo(context.Background(), f.store.db, filepath.Join(dir, "retirement-overlap-"+stop+".db")); err != nil {
					t.Error("preserve overlap failure database", err)
				}
			}
		})
	}
	version := "1"
	delivery := NewDeliveryStore(f.activation.store.config.Owners, true, func(context.Context, *sql.Tx, DeliveryAccess) (string, error) { return version, nil }, 2*time.Second, 16)
	gate, err := delivery.Open(t.Context(), f.owner, "stable-participant", "initial-gate")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := f.runtime.SendTrackedParticipantOpen(t.Context(), f.activation, retirementOpen(t, f, "initial"))
	if err != nil {
		t.Fatal(err)
	}
	retirementReply(t, f, retirementControl(t, f.runtime), gate)
	joined := retirementControl(t, f.runtime)
	binding, err := f.runtime.CaptureParticipantJoined(f.activation, pending, joined)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.runtime.CompleteControl(joined); err != nil {
		t.Fatal(err)
	}
	renewal, output := retirementControl(t, f.runtime), retirementControl(t, f.runtime)
	raw, err := renewal.Frame()
	if err != nil {
		t.Fatal(err)
	}
	var q protocol.DeliveryRenew
	if err = json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	grant := protocol.DeliveryGrant{Type: "delivery_grant", RequestID: q.RequestID, Scope: q.Scope, SessionID: q.SessionID, ParticipantRef: q.ParticipantRef, Generation: q.Fence.Generation, PermissionVersion: q.PermissionVersion, RenewalNonce: q.RenewalNonce, GateID: q.GateID, DurationMs: "2000"}
	raw, err = json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	version = "2"
	if _, err = delivery.Renew(t.Context(), f.owner, gate.GateID, "epoch-change"); !errors.Is(err, ErrDeliveryClosed) {
		t.Fatal(err)
	}
	// Block the existing sole FD writer, then observe the exact borrowed reply
	// reservation. No scheduling delay is used to manufacture the overlap.
	<-f.runtime.writer
	held := true
	retireStarted := false
	ctx, cancel := context.WithCancel(t.Context())
	replyDone := make(chan error, 1)
	retiredDone := make(chan error, 1)
	var retired *SessionRetiredDelivery
	defer func() {
		cancel()
		if held {
			f.runtime.writer <- struct{}{}
		}
		<-replyDone
		if retireStarted {
			<-retiredDone
		}
	}()
	go func() { replyDone <- f.runtime.ReplyControl(t.Context(), renewal, raw) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.runtime.mu.Lock()
		entry := f.runtime.child[renewal.id]
		replying := entry != nil && entry.replying
		f.runtime.mu.Unlock()
		if replying {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reply never reserved the held writer")
		}
		runtime.Gosched()
	}
	retireStarted = true
	go func() {
		var e error
		retired, e = f.runtime.RetireParticipant(ctx, f.activation, binding)
		retiredDone <- e
	}()
	for {
		if err := f.runtime.Err(); err != nil {
			t.Fatal("queued renewal/Leave overlap killed shared owner runtime", err)
		}
		f.runtime.mu.Lock()
		retiring := binding.state.retiring
		f.runtime.mu.Unlock()
		if retiring {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retirement never fenced queued reply")
		}
		runtime.Gosched()
	}
	if stop != "finish" {
		if stop == "cancel" {
			cancel()
		} else {
			_ = f.runtime.Close()
		}
		retireErr := <-retiredDone
		retiredDone <- retireErr
		if retireErr == nil {
			t.Fatal("interrupted retirement succeeded without physical Leave")
		}
		if f.runtime.Err() == nil {
			t.Fatal("interrupted retirement left uncertain runtime live")
		}
		return
	}
	f.runtime.writer <- struct{}{}
	held = false
	replyErr := <-replyDone
	replyDone <- replyErr
	if replyErr != nil {
		t.Fatal("already reserved reply did not complete before Leave", replyErr)
	}
	retireErr := <-retiredDone
	retiredDone <- retireErr
	if retireErr != nil {
		t.Fatal(retireErr)
	}
	if err = f.runtime.CompleteRetiredDeliveryControl(renewal, retired); err == nil {
		t.Fatal("normally completed reply was completed twice")
	}
	if err = f.runtime.ValidateRetiredOutput(output, retired); err != nil {
		t.Fatal(err)
	}
	if err = f.runtime.CompleteControl(output); err != nil {
		t.Fatal(err)
	}
	fresh, err := delivery.Open(t.Context(), f.owner, "stable-participant", "fresh-gate")
	if err != nil {
		t.Fatal(err)
	}
	next, err := f.runtime.SendParticipantReopen(t.Context(), f.activation, retired, retirementOpen(t, f, "replacement"))
	if err != nil {
		t.Fatal(err)
	}
	retirementReply(t, f, retirementControl(t, f.runtime), fresh)
	mark := retirementControl(t, f.runtime)
	if _, err = f.runtime.CaptureParticipantJoined(f.activation, next, mark); err != nil {
		t.Fatal(err)
	}
	if err = f.runtime.CompleteControl(mark); err != nil {
		t.Fatal(err)
	}
	if err = f.runtime.Err(); err != nil {
		t.Fatal("owner runtime failed after overlapped retirement and fresh admission", err)
	}
}
