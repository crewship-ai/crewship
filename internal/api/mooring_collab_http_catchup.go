package api

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// A snapshot owns its receipt graph and encoded DATA. It is copied only from
// post-COMMIT publications, never from provisional candidates or client input.
// One receipt is copied per original group, not per atomic in that group.
type collaborationSavedFrame struct {
	update   protocol.DocumentUpdate
	observed collaborationObservedCut
	raw      []byte // Only an already validated incoming frame; never aliases its token.
}

// Caller holds c.mu. RAM is released before every SQL/physical-send operation.
func (c *collaborationHTTPConnection) savedFramesLocked() (string, []collaborationSavedFrame, error) {
	l := c.document.originals
	if l == nil {
		return "", nil, errCollaborationHTTP
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	source := l.sources[c]
	if len(l.changes) > collaborationOriginalRecordLimit {
		return "", nil, errCollaborationHTTP
	}
	latest := ""
	if source != nil && !source.retired {
		for revision := range source.observed {
			if latest == "" || collaborationRevisionLess(latest, revision) {
				latest = revision
			}
		}
	}
	receipts := make(map[*collaborationOriginalGroup]*protocol.CommitResult)
	frames := make([]collaborationSavedFrame, 0, len(l.changes))
	for _, slot := range l.changes {
		if slot.saved == nil {
			continue
		}
		group := slot.group
		if !slot.captured || slot.captureErr != nil || slot.rejected || group == nil || group.privateReceipt == nil || !group.captured || !group.sent {
			return "", nil, errCollaborationHTTP
		}
		receipt := receipts[group]
		if receipt == nil {
			raw, err := json.Marshal(group.privateReceipt)
			if err != nil || len(raw) > collaborationHTTPFrameLimit {
				return "", nil, errCollaborationHTTP
			}
			receipt = new(protocol.CommitResult)
			if json.Unmarshal(raw, receipt) != nil {
				return "", nil, errCollaborationHTTP
			}
			receipts[group] = receipt
		}
		saved := slot.saved
		if saved.cut.ChangeID != slot.change.ID || saved.cut.MembershipHash != slot.change.MembershipHash || !collaborationOriginalRevision(saved.cut.Revision) || !collaborationStateDigest(saved.cut.StateDigest) {
			return "", nil, errCollaborationHTTP
		}
		var outcome *protocol.ChangeResult
		for i := range receipt.Changes {
			if string(receipt.Changes[i].ChangeID) == slot.change.ID {
				if outcome != nil {
					return "", nil, errCollaborationHTTP
				}
				outcome = &receipt.Changes[i]
			}
		}
		if outcome == nil || outcome.Status != "saved" || len(slot.change.Operations) == 0 || outcome.FirstRevision == nil || outcome.LastRevision == nil || string(*outcome.LastRevision) != saved.cut.Revision || len(outcome.Operations) != len(slot.change.Operations) {
			return "", nil, errCollaborationHTTP
		}
		first, e1 := strconv.ParseUint(string(*outcome.FirstRevision), 10, 64)
		last, e2 := strconv.ParseUint(string(*outcome.LastRevision), 10, 64)
		if e1 != nil || e2 != nil || first == 0 || last < first || !collaborationOriginalRevision(string(*outcome.FirstRevision)) || last-first != uint64(len(slot.change.Operations)-1) {
			return "", nil, errCollaborationHTTP
		}
		for i, raw := range outcome.Operations {
			var bound struct {
				OpID   protocol.Identifier `json:"op_id"`
				Status string              `json:"status"`
			}
			if json.Unmarshal(raw, &bound) != nil || string(bound.OpID) != slot.change.Operations[i].ID || bound.Status != "saved" {
				return "", nil, errCollaborationHTTP
			}
		}
		data, err := json.Marshal(struct {
			Kind    string                `json:"kind"`
			Update  string                `json:"update"`
			Targets []protocol.Identifier `json:"targets"`
		}{"wordcraft.yrs-update.v1", saved.delta.Update, append([]protocol.Identifier{}, saved.delta.Targets...)})
		if err != nil {
			return "", nil, errCollaborationHTTP
		}
		update := protocol.DocumentUpdate{Type: "document_update", RequestID: protocol.Identifier("actor-update:" + saved.cut.Revision), Scope: c.document.wireScope(), SessionID: protocol.Identifier(c.document.activation.SessionID()), Generation: c.document.wireFence().Generation, FromRevision: protocol.Counter(strconv.FormatUint(first-1, 10)), ToRevision: protocol.Counter(saved.cut.Revision), Receipt: *receipt, Encoding: "json", Data: string(data)}
		frames = append(frames, collaborationSavedFrame{update: update, observed: collaborationObservedCut{revision: saved.cut.Revision, stateDigest: saved.cut.StateDigest}})
	}
	sort.Slice(frames, func(i, j int) bool {
		return collaborationRevisionLess(string(frames[i].update.ToRevision), string(frames[j].update.ToRevision))
	})
	return latest, frames, nil
}

// Plan the entire bounded chain before sending any prefix. Atomic intervals can
// span multiple operation revisions; no nonexistent intermediate cut is made up.
func collaborationSavedChain(latest, target string, frames []collaborationSavedFrame) ([]collaborationSavedFrame, error) {
	if !collaborationOriginalRevision(latest) || !collaborationOriginalRevision(target) || len(frames) > collaborationOriginalRecordLimit {
		return nil, errCollaborationHTTP
	}
	if !collaborationRevisionLess(latest, target) {
		return nil, nil
	}
	chain := make([]collaborationSavedFrame, 0, len(frames))
	cursor := latest
	for _, frame := range frames {
		from, to := string(frame.update.FromRevision), string(frame.update.ToRevision)
		if !collaborationOriginalRevision(from) || !collaborationOriginalRevision(to) || !collaborationRevisionLess(from, to) {
			return nil, errCollaborationHTTP
		}
		if !collaborationRevisionLess(latest, to) {
			continue
		}
		if collaborationRevisionLess(target, to) {
			break
		}
		if from != cursor {
			return nil, errCollaborationHTTP
		}
		chain = append(chain, frame)
		cursor = to
	}
	if cursor != target {
		return nil, errCollaborationHTTP
	}
	return chain, nil
}

// encodeSavedFrame preserves the same bounded, closed public-wire boundary used
// for native DATA. Schema validity never supplies receipt or reader authority.
func (c *collaborationHTTPConnection) encodeSavedFrame(frame collaborationSavedFrame) ([]byte, error) {
	if c.document.manager == nil || c.document.manager.schema == nil {
		return nil, errCollaborationHTTP
	}
	raw := frame.raw
	if raw == nil {
		var err error
		raw, err = json.Marshal(frame.update)
		if err != nil {
			return nil, err
		}
	}
	if len(raw) > collaborationHTTPFrameLimit {
		return nil, errCollaborationHTTP
	}
	canonical, err := c.document.manager.schema.ValidateE3(raw)
	if err != nil || !collaborationEqualJSON(canonical, frame.update) {
		return nil, errCollaborationHTTP
	}
	return raw, nil
}

// Observed state is rechecked for EACH frame: a concurrent current FULL
// checkpoint may already cover it, but no missing atomic interval can be skipped.
func collaborationSavedFrameNeeded(latest string, frame collaborationSavedFrame) (bool, error) {
	if !collaborationOriginalRevision(latest) {
		return false, errCollaborationHTTP
	}
	if !collaborationRevisionLess(latest, string(frame.update.ToRevision)) {
		return false, nil
	}
	if latest != string(frame.update.FromRevision) {
		return false, errCollaborationHTTP
	}
	return true, nil
}

// Caller holds c.mu. A newer pending rotation owns its own future Joined
// continuation. The old metadata trigger sends nothing and moves no cursor.
func (c *collaborationHTTPConnection) savedRecipientDeferredLocked() bool {
	return c.closed || c.transition != nil
}

func (c *collaborationHTTPConnection) savedCatchupPlanLocked() ([]collaborationSavedFrame, error) {
	latest, frames, err := c.savedFramesLocked()
	if err != nil {
		return nil, err
	}
	// Initial bootstrap has not physically delivered any observed basis yet.
	// Completing Joined metadata must not manufacture one or cancel the document.
	if latest == "" {
		return nil, nil
	}
	target := latest
	if len(frames) > 0 && collaborationRevisionLess(target, string(frames[len(frames)-1].update.ToRevision)) {
		target = string(frames[len(frames)-1].update.ToRevision)
	}
	return collaborationSavedChain(latest, target, frames)
}

// The output FIFO owns ordering, not a long-lived recipient lock. Yield between
// frames so serial management can renew/rotate; each send+observe remains atomic
// with checkpoint load/send/observe and rechecks the CURRENT SQL reader gate.
func (c *collaborationHTTPConnection) sendSavedChain(ctx context.Context, chain []collaborationSavedFrame) (failure error) {
	stage := hostCatchupContext
	defer func() { c.document.observeHostFailure(ctx, stage, failure) }()
	for _, frame := range chain {
		stage = hostCatchupContext
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.mu.Lock()
		deferred := c.savedRecipientDeferredLocked()
		c.mu.Unlock()
		if deferred {
			return nil
		}
		stage = hostCatchupGate
		if err := c.ensureCurrentGateForOutput(ctx, true); err != nil {
			if err == errCollaborationRecipientDenied {
				return nil
			}
			return err
		}
		deferredDuringSend := false
		err := func() error {
			c.mu.Lock()
			defer c.mu.Unlock()
			stage = hostCatchupContext
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if c.savedRecipientDeferredLocked() {
				deferredDuringSend = true
				return nil
			}
			l := c.document.originals
			l.mu.Lock()
			latest := ""
			if source := l.sources[c]; source != nil && !source.retired {
				for revision := range source.observed {
					if latest == "" || collaborationRevisionLess(latest, revision) {
						latest = revision
					}
				}
			}
			l.mu.Unlock()
			stage = hostCatchupCursor
			needed, err := collaborationSavedFrameNeeded(latest, frame)
			if err != nil || !needed {
				return err
			}
			stage = hostCatchupEncode
			raw, err := c.encodeSavedFrame(frame)
			if err != nil {
				return err
			}
			// Add pump cancellation to the ORIGINAL request context. No new credential,
			// expiry, origin, source or author authority is captured here.
			operation, cancel := context.WithCancel(c.request.Context())
			stop := context.AfterFunc(ctx, cancel)
			defer stop()
			defer cancel()
			stage = hostCatchupContext
			if ctx.Err() != nil {
				return ctx.Err()
			}
			stage = hostCatchupPhysical
			if err = c.checkedSendRequestLocked(c.request.WithContext(operation), raw); err != nil {
				return err
			}
			stage = hostCatchupObserve
			return l.observe(c, frame.observed)
		}()
		if err != nil || deferredDuringSend {
			return err
		}
	}
	return nil
}

// This owned DATA continuation is created only after opaque Joined capture.
// Its old envelope/gate need not survive retirement pruning. Current rights are
// established again by every checked send, never by this scheduling obligation.
func (c *collaborationHTTPConnection) catchUpAfterJoined(ctx context.Context) (failure error) {
	stage := hostJoinedCatchup
	defer func() { c.document.observeHostFailure(ctx, stage, failure) }()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	c.mu.Lock()
	deferred := c.savedRecipientDeferredLocked()
	c.mu.Unlock()
	if deferred {
		return nil
	}
	if err := c.ensureCurrentGateForOutput(ctx, true); err != nil {
		if err == errCollaborationRecipientDenied {
			return nil
		}
		return err
	}
	c.mu.Lock()
	if c.savedRecipientDeferredLocked() {
		c.mu.Unlock()
		return nil
	}
	if !c.joinedDelivered || c.participantBinding == nil {
		c.mu.Unlock()
		return errCollaborationHTTP
	}
	chain, err := c.savedCatchupPlanLocked()
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return c.sendSavedChain(ctx, chain)
}
