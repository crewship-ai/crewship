package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"sort"
	"strconv"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

type collaborationSavedOriginal struct {
	cut   mooring.SessionSavedCut
	delta protocol.SessionNativeDelta
}

type collaborationOriginalView struct {
	reference, source string
	order             uint64
	change            mooring.BatchChange
	payload           collaborationHumanOriginalPayload
	basis             collaborationObservedCut
	saved             *collaborationSavedOriginal
	rejected          bool
}

// Snapshot only immutable original DATA under the RAM lock. Historical loads
// acquire SQL through the scoped core accessor after this lock is released.
func (l *collaborationLiveOriginals) prepareViews(change mooring.BatchChange) (collaborationOriginalView, map[string]collaborationOriginalView, error) {
	var empty collaborationOriginalView
	l.mu.Lock()
	defer l.mu.Unlock()
	current := l.changes[change.ID]
	if current == nil || !current.captured || current.captureErr != nil || current.source == nil || current.source.retired || !reflect.DeepEqual(current.change, change) {
		return empty, nil, errCollaborationHTTP
	}
	views := make(map[string]collaborationOriginalView, len(l.changes))
	for id, slot := range l.changes {
		if !slot.captured || slot.captureErr != nil || slot.source == nil || slot.source.connection == nil {
			continue
		}
		view := collaborationOriginalView{reference: slot.reference, source: slot.source.connection.ref, order: slot.order, change: slot.change, payload: slot.payload, basis: slot.basis, saved: slot.saved, rejected: slot.rejected}
		views[id] = view
	}
	return views[change.ID], views, nil
}

func (l *collaborationLiveOriginals) prepareHuman(ctx context.Context, tx *sql.Tx, access mooring.SessionHumanPrepareAccess) (*protocol.SessionHumanPrepareContext, error) {
	if l == nil || l.document == nil || l.document.cell == nil || tx == nil || ctx == nil || ctx.Err() != nil || !access.FromStore(l.document.cell.store) {
		return nil, errCollaborationHTTP
	}
	d := l.document
	owner, err := access.Lease()
	if err != nil || owner != d.owner || d.activation.RequireHumanNativePrepare() != nil {
		return nil, errCollaborationHTTP
	}
	change, err := access.Change()
	if err != nil {
		return nil, err
	}
	if change.TaskID != "" {
		// Genuine sealed agent commands use their existing application path. This
		// does not fabricate a human writer or an agent live-history source.
		return nil, nil
	}
	current, views, err := l.prepareViews(change)
	if err != nil {
		return nil, err
	}
	prepared, err := access.PreparedCandidates()
	if err != nil || !collaborationOriginalRevision(prepared.TransactionRevision) || !collaborationStateDigest(prepared.TransactionStateDigest) {
		return nil, errCollaborationHTTP
	}
	next, err := collaborationNextUnsettled(current, views, prepared.Candidates)
	if err != nil {
		return nil, err
	}
	replay := current.basis
	for _, view := range views {
		if view.source == current.source && view.order < current.order && collaborationRevisionLess(view.basis.revision, replay.revision) {
			replay = view.basis
		}
	}
	load := func(cut collaborationObservedCut) (protocol.Checkpoint, error) {
		cp, digest, e := access.LoadCheckpoint(ctx, cut.revision)
		if e != nil || digest != cut.stateDigest || cut.fullDigest != "" && string(cp.Digest) != cut.fullDigest || cp.Scope != d.wireScope() || cp.Versions != d.wireVersions() {
			return protocol.Checkpoint{}, errCollaborationHTTP
		}
		return cp, nil
	}
	original, err := load(current.basis)
	if err != nil {
		return nil, err
	}
	human := &protocol.SessionHumanPrepareContext{SessionID: protocol.Identifier(d.activation.SessionID()), RuntimeRef: protocol.Identifier(d.cell.runtime.RuntimeRef()), Fence: d.wireFence(), OpID: protocol.Identifier(change.Operations[0].ID), SourceRef: protocol.Identifier(current.source), OriginalReference: protocol.Identifier(current.reference), Origin: protocol.PositiveCounter(current.payload.Origin), SourceOrder: protocol.PositiveCounter(strconv.FormatUint(current.order, 10)), NextUnsettledOrder: protocol.PositiveCounter(strconv.FormatUint(next, 10)), OriginalBasis: original, SavedTranscript: []protocol.SessionSavedNativeDelta{}, OwnPrefix: []protocol.SessionSavedHumanPrefix{}, OwnHistory: []protocol.SessionSavedNativeDelta{}}
	if replay.revision != current.basis.revision {
		cp, e := load(replay)
		if e != nil {
			return nil, e
		}
		human.ReplayBasis = &cp
	}
	ordered := make([]collaborationOriginalView, 0, len(views))
	for _, view := range views {
		if view.saved != nil && !collaborationRevisionLess(prepared.TransactionRevision, view.saved.cut.Revision) {
			ordered = append(ordered, view)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		return collaborationRevisionLess(ordered[i].saved.cut.Revision, ordered[j].saved.cut.Revision)
	})
	prior, digest := replay.revision, replay.stateDigest
	for _, view := range ordered {
		entry := view.savedDelta()
		if collaborationRevisionLess(replay.revision, view.saved.cut.Revision) {
			if !collaborationRevisionLess(prior, view.saved.cut.Revision) {
				return nil, errCollaborationHTTP
			}
			human.SavedTranscript = append(human.SavedTranscript, entry)
			prior, digest = view.saved.cut.Revision, view.saved.cut.StateDigest
			if view.source == current.source && view.order < current.order {
				atomic, e := collaborationWireOriginal(view.change)
				if e != nil {
					return nil, e
				}
				human.OwnPrefix = append(human.OwnPrefix, protocol.SessionSavedHumanPrefix{Reference: protocol.Identifier(view.reference), SourceRef: protocol.Identifier(view.source), SourceOrder: entry.SourceOrder, Change: atomic})
			}
		}
		if view.source == current.source && view.order < current.order {
			human.OwnHistory = append(human.OwnHistory, entry)
		}
	}
	if prior != prepared.TransactionRevision || digest != prepared.TransactionStateDigest {
		return nil, errCollaborationHTTP
	}
	if len(prepared.Candidates) != 0 {
		group := &protocol.SessionPreparedPrefix{TransactionRevision: protocol.Counter(prepared.TransactionRevision), TransactionStateDigest: protocol.Digest(prepared.TransactionStateDigest), Deltas: []protocol.SessionPreparedNativeDelta{}, OwnPrefix: []protocol.SessionPreparedHumanPrefix{}}
		for _, candidate := range prepared.Candidates {
			view, found := views[candidate.Change.ID]
			if !found || !reflect.DeepEqual(view.change, candidate.Change) || !collaborationMatchesHumanDelta(view, candidate.NativeDelta) {
				return nil, errCollaborationHTTP
			}
			entry := protocol.SessionPreparedNativeDelta{Reference: protocol.Identifier(view.reference), SourceRef: protocol.Identifier(view.source), SourceOrder: protocol.PositiveCounter(strconv.FormatUint(view.order, 10)), WriterOrigin: candidate.NativeDelta.WriterOrigin, Update: candidate.NativeDelta.Update, Targets: append([]protocol.Identifier{}, candidate.NativeDelta.Targets...), CheckpointStateDigest: protocol.Digest(candidate.StateDigest), ProspectiveRevision: protocol.PositiveCounter(candidate.Revision)}
			group.Deltas = append(group.Deltas, entry)
			if view.source == current.source && view.order < current.order {
				atomic, e := collaborationWireOriginal(view.change)
				if e != nil {
					return nil, e
				}
				group.OwnPrefix = append(group.OwnPrefix, protocol.SessionPreparedHumanPrefix{Reference: entry.Reference, SourceRef: entry.SourceRef, SourceOrder: entry.SourceOrder, Change: atomic})
			}
		}
		human.PreparedPrefix = group
	}
	if err := collaborationHumanHistoryBounds(human); err != nil {
		return nil, err
	}
	return human, nil
}

func (view collaborationOriginalView) savedDelta() protocol.SessionSavedNativeDelta {
	return protocol.SessionSavedNativeDelta{Reference: protocol.Identifier(view.reference), SourceRef: protocol.Identifier(view.source), SourceOrder: protocol.PositiveCounter(strconv.FormatUint(view.order, 10)), WriterOrigin: view.saved.delta.WriterOrigin, Update: view.saved.delta.Update, Targets: append([]protocol.Identifier{}, view.saved.delta.Targets...), Revision: protocol.PositiveCounter(view.saved.cut.Revision), CheckpointStateDigest: protocol.Digest(view.saved.cut.StateDigest)}
}

func collaborationRevisionLess(a, b string) bool {
	left, le := strconv.ParseUint(a, 10, 64)
	right, re := strconv.ParseUint(b, 10, 64)
	return le == nil && re == nil && left < right
}

func collaborationWireOriginal(change mooring.BatchChange) (protocol.AtomicChange, error) {
	var atomic protocol.AtomicChange
	raw, err := json.Marshal(change)
	if err != nil {
		return atomic, err
	}
	err = json.Unmarshal(raw, &atomic)
	return atomic, err
}

func collaborationMatchesHumanDelta(view collaborationOriginalView, delta protocol.SessionNativeDelta) bool {
	if string(delta.WriterOrigin) != view.payload.Origin || delta.Update != view.payload.Update || len(delta.Targets) != len(view.payload.Targets) {
		return false
	}
	for i, target := range delta.Targets {
		if string(target) != view.payload.Targets[i] {
			return false
		}
	}
	for _, allocated := range delta.AllocationOrigins {
		if allocated != delta.WriterOrigin {
			return false
		}
	}
	return true
}

func collaborationHumanHistoryBounds(human *protocol.SessionHumanPrepareContext) error {
	count, rawBytes := len(human.SavedTranscript)+len(human.OwnHistory), 0
	add := func(encoded string) bool {
		raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil || base64.StdEncoding.EncodeToString(raw) != encoded || len(raw) > collaborationOriginalByteLimit-rawBytes {
			return false
		}
		rawBytes += len(raw)
		return true
	}
	for _, entry := range append(append([]protocol.SessionSavedNativeDelta{}, human.SavedTranscript...), human.OwnHistory...) {
		if !add(entry.Update) {
			return mooring.ErrBatchCapacity
		}
	}
	if human.PreparedPrefix != nil {
		count += len(human.PreparedPrefix.Deltas)
		for _, entry := range human.PreparedPrefix.Deltas {
			if !add(entry.Update) {
				return mooring.ErrBatchCapacity
			}
		}
	}
	encoded, err := json.Marshal(human)
	if err != nil || count > collaborationOriginalRecordLimit || len(encoded) > mooring.MaxPrivatePrepareRPCFrame {
		return mooring.ErrBatchCapacity
	}
	return nil
}

func (l *collaborationLiveOriginals) validateNative(ctx context.Context, tx *sql.Tx, access mooring.SessionNativeCandidateAccess) error {
	if l == nil || l.document == nil || l.document.cell == nil || tx == nil || ctx == nil || ctx.Err() != nil || !access.FromStore(l.document.cell.store) {
		return errCollaborationHTTP
	}
	d := l.document
	owner, err := access.Lease()
	if err != nil || owner != d.owner || d.activation.RequireHumanNativePrepare() != nil {
		return errCollaborationHTTP
	}
	candidate, err := access.Candidate()
	if err != nil || candidate.Change.TaskID != "" {
		// Agent live source/ref/order must come from the actual fixed-task
		// handoff. A native number or child task label cannot create it.
		return errCollaborationHTTP
	}
	view, _, err := l.prepareViews(candidate.Change)
	if err != nil || !collaborationMatchesHumanDelta(view, candidate.NativeDelta) {
		return errCollaborationHTTP
	}
	origin, err := strconv.ParseUint(view.payload.Origin, 10, 53)
	if err != nil {
		return errCollaborationHTTP
	}
	d.manager.mu.Lock()
	defer d.manager.mu.Unlock()
	_, reserved := d.manager.usedOrigins[d.key][origin]
	if d.manager.closed || d.manager.docs[d.key] != d || !reserved {
		return errCollaborationHTTP
	}
	return nil
}

// Earlier candidates have been qualified and written ONLY inside this active
// SQL writer. They may advance this invocation's preparation cursor, but are
// still carried as a distinct PreparedPrefix, never as externally Saved. An
// original with no exact candidate/committed history continues to block gaps.
func collaborationNextUnsettled(current collaborationOriginalView, views map[string]collaborationOriginalView, prepared []mooring.SessionPreparedCandidate) (uint64, error) {
	if current.order == 0 || current.source == "" || len(prepared) > collaborationOriginalRecordLimit {
		return 0, errCollaborationHTTP
	}
	qualified := make(map[string]bool, len(prepared))
	for _, candidate := range prepared {
		view, ok := views[candidate.Change.ID]
		if !ok || candidate.Change.ID == current.change.ID || qualified[candidate.Change.ID] || view.saved != nil || view.rejected || !reflect.DeepEqual(view.change, candidate.Change) || !collaborationMatchesHumanDelta(view, candidate.NativeDelta) {
			return 0, errCollaborationHTTP
		}
		qualified[candidate.Change.ID] = true
	}
	next := current.order
	for id, view := range views {
		if view.source == current.source && view.order < next && view.saved == nil && !view.rejected && !qualified[id] {
			next = view.order
		}
	}
	return next, nil
}
