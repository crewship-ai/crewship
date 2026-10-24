package api

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"sync"

	"github.com/crewship-ai/crewship/internal/mooring"
)

const collaborationOriginalRecordLimit = 256
const collaborationOriginalByteLimit = 262144

// These volatile records preserve original intent, never durable source rights.
// They cannot survive document/runtime replacement or be restored from SQL.
// Authority and native application qualification are separate mandatory steps.
type collaborationObservedCut struct {
	revision, stateDigest, fullDigest string
}

type collaborationOriginalSource struct {
	connection *collaborationHTTPConnection
	order      uint64
	retired    bool
	observed   map[string]collaborationObservedCut
}

type collaborationLiveOriginal struct {
	source     *collaborationOriginalSource
	reference  string
	order      uint64
	change     mooring.BatchChange
	payload    collaborationHumanOriginalPayload
	packet     []byte
	basis      collaborationObservedCut
	ready      chan struct{}
	captured   bool
	captureErr error
}

type collaborationLiveOriginals struct {
	mu         sync.Mutex
	document   *collaborationHTTPDocument
	sources    map[*collaborationHTTPConnection]*collaborationOriginalSource
	changes    map[string]*collaborationLiveOriginal
	operations map[string]*collaborationLiveOriginal
	bytes      int
}

func newCollaborationLiveOriginals(d *collaborationHTTPDocument) *collaborationLiveOriginals {
	return &collaborationLiveOriginals{document: d, sources: make(map[*collaborationHTTPConnection]*collaborationOriginalSource), changes: make(map[string]*collaborationLiveOriginal), operations: make(map[string]*collaborationLiveOriginal)}
}

// observe is called only after actual current-reader checked physical delivery.
// A later cut cannot refresh an already recorded revision or an original slot.
func (l *collaborationLiveOriginals) observe(c *collaborationHTTPConnection, cut collaborationObservedCut) error {
	if l == nil || l.document == nil || c == nil || c.document != l.document || !collaborationOriginalRevision(cut.revision) || !collaborationStateDigest(cut.stateDigest) || cut.fullDigest != "" && !collaborationStateDigest(cut.fullDigest) {
		return errCollaborationHTTP
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	source := l.sources[c]
	if source == nil {
		if len(l.sources) >= collaborationHTTPParticipantLimit {
			return mooring.ErrBatchCapacity
		}
		source = &collaborationOriginalSource{connection: c, observed: make(map[string]collaborationObservedCut)}
		l.sources[c] = source
	}
	if source.retired {
		return errCollaborationHTTP
	}
	if prior, ok := source.observed[cut.revision]; ok {
		if prior.stateDigest != cut.stateDigest || prior.fullDigest != "" && cut.fullDigest != "" && prior.fullDigest != cut.fullDigest {
			return errCollaborationHTTP
		}
		return nil
	}
	if len(source.observed) >= collaborationOriginalRecordLimit {
		return mooring.ErrBatchCapacity
	}
	source.observed[cut.revision] = cut
	return nil
}

func (l *collaborationLiveOriginals) retire(c *collaborationHTTPConnection) {
	if l == nil || l.document == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if source := l.sources[c]; source != nil {
		source.retired = true
	} else if c != nil && c.document == l.document && len(l.sources) < collaborationHTTPParticipantLimit {
		// Closing before the first checkpoint send must not allow an in-flight
		// observe to create a new live source after retirement.
		l.sources[c] = &collaborationOriginalSource{connection: c, retired: true}
	}
}

func collaborationOriginalRevision(revision string) bool {
	n, err := strconv.ParseUint(revision, 10, 64)
	return err == nil && strconv.FormatUint(n, 10) == revision
}

// reserve freezes bounded DATA before any SQL operation. It never holds mu
// while acquiring a SQL writer: prepare callbacks acquire SQL before mu.
// A failed capture burns these reservations until the process dies; no eviction
// can reuse their source order, original reference or intent identity.
func (l *collaborationLiveOriginals) reserve(c *collaborationHTTPConnection, changes []mooring.BatchChange) ([]*collaborationLiveOriginal, []*collaborationLiveOriginal, error) {
	if l == nil || l.document == nil || c == nil || c.document != l.document || len(changes) == 0 || len(changes) > collaborationOriginalRecordLimit {
		return nil, nil, errCollaborationHTTP
	}
	ownedChanges, err := mooring.NormalizeSessionIngress(l.document.key, changes)
	if err != nil {
		return nil, nil, err
	}
	changes = ownedChanges
	type candidate struct {
		change    mooring.BatchChange
		payload   collaborationHumanOriginalPayload
		packet    []byte
		reference string
		bytes     int
	}
	prepared := make([]candidate, 0, len(changes))
	for _, change := range changes {
		raw, err := json.Marshal(change)
		if err != nil || len(raw) > collaborationOriginalByteLimit {
			return nil, nil, mooring.ErrBatchCapacity
		}
		var owned mooring.BatchChange
		if json.Unmarshal(raw, &owned) != nil {
			return nil, nil, errCollaborationHTTP
		}
		payload, err := c.parseOriginalHumanChange(owned)
		if err != nil {
			return nil, nil, err
		}
		ref, err := collaborationOpaqueID()
		if err != nil {
			return nil, nil, err
		}
		// Charge the retained packet and the separately decoded string copies.
		// No full checkpoint is retained per original record.
		n := len(raw) + len(payload.Update) + len(payload.AuthoredFrontier) + len(payload.AuthoredCheckpointStateDigest)
		for _, target := range payload.Targets {
			n += len(target)
		}
		prepared = append(prepared, candidate{owned, payload, raw, ref, n})
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	source := l.sources[c]
	if source == nil || source.retired {
		return nil, nil, errCollaborationHTTP
	}
	added, extra := 0, 0
	seen := make(map[string]bool, len(prepared))
	seenOps := make(map[string]bool, len(prepared))
	for _, candidate := range prepared {
		opID := candidate.change.Operations[0].ID
		if seen[candidate.change.ID] || seenOps[opID] {
			return nil, nil, errCollaborationHTTP
		}
		seen[candidate.change.ID] = true
		seenOps[opID] = true
		if prior := l.operations[opID]; prior != nil && prior.change.ID != candidate.change.ID {
			return nil, nil, errCollaborationHTTP
		}
		if prior := l.changes[candidate.change.ID]; prior != nil {
			if prior.source != source || !bytes.Equal(prior.packet, candidate.packet) {
				return nil, nil, errCollaborationHTTP
			}
			continue
		}
		if _, ok := source.observed[candidate.change.Operations[0].BaseRevision]; !ok {
			return nil, nil, errCollaborationHTTP
		}
		added++
		extra += candidate.bytes
	}
	if len(l.changes)+added > collaborationOriginalRecordLimit || extra > collaborationOriginalByteLimit-l.bytes {
		return nil, nil, mooring.ErrBatchCapacity
	}
	all := make([]*collaborationLiveOriginal, 0, len(prepared))
	fresh := make([]*collaborationLiveOriginal, 0, added)
	for _, candidate := range prepared {
		if prior := l.changes[candidate.change.ID]; prior != nil {
			all = append(all, prior)
			continue
		}
		source.order++ // bounded by the permanent per-process record limit
		slot := &collaborationLiveOriginal{source: source, reference: candidate.reference, order: source.order, change: candidate.change, payload: candidate.payload, packet: candidate.packet, basis: source.observed[candidate.change.Operations[0].BaseRevision], ready: make(chan struct{})}
		l.changes[slot.change.ID] = slot
		l.operations[slot.change.Operations[0].ID] = slot
		l.bytes += candidate.bytes
		all = append(all, slot)
		fresh = append(fresh, slot)
	}
	return all, fresh, nil
}

// capture publishes an original slot only after the actual authenticated human
// seal writer commits, under the original runtime/ingress operation lease.
// Native application semantics remain unqualified DATA until session prepare.
func (l *collaborationLiveOriginals) capture(ctx context.Context, c *collaborationHTTPConnection, changes []mooring.BatchChange) ([]*collaborationLiveOriginal, error) {
	if l == nil || l.document == nil || l.document.originals != l || l.document.manager == nil || l.document.humans == nil || l.document.submissions == nil || ctx == nil || ctx.Err() != nil || c == nil || c.document != l.document {
		return nil, errCollaborationHTTP
	}
	c.mu.Lock()
	request, valid := c.request, !c.closed && c.used && c.joinedDelivered && c.admission.write
	c.mu.Unlock()
	if !valid || request == nil {
		return nil, errCollaborationHTTP
	}
	d := l.document
	n, originErr := strconv.ParseUint(c.origin, 10, 53)
	d.manager.mu.Lock()
	_, allocated := d.manager.usedOrigins[d.key][n]
	issuedDocument := d.manager.docs[d.key] == d && !d.manager.closed
	d.manager.mu.Unlock()
	d.mu.Lock()
	issuedConnection := d.connections[c.ref] == c && !d.closed
	d.mu.Unlock()
	if originErr != nil || !collaborationHumanOrigin(c.origin) || !mooring.ValidWireIdentifier(c.ref) || !allocated || !issuedDocument || !issuedConnection {
		return nil, errCollaborationHTTP
	}
	all, fresh, err := l.reserve(c, changes)
	if err != nil {
		return nil, err
	}
	sealed := make([]mooring.BatchChange, len(all))
	for i, slot := range all {
		sealed[i] = slot.change
	}
	finished := false
	defer func() {
		if !finished {
			l.finishCapture(fresh, errCollaborationHTTP)
		}
	}()
	binding, err := d.humans.storeBinding()
	if err != nil {
		return nil, err
	}
	operationCtx, cancel := context.WithCancel(request.Context())
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	defer cancel()
	err = d.humans.runHumanOperation(request.Clone(operationCtx), c.admission.participant, func(bound context.Context) error {
		return d.submissions.run(bound, binding, func(bound context.Context) error {
			if e := d.submissions.sealHuman(bound, sealed); e != nil {
				return e
			}
			l.finishCapture(fresh, nil)
			finished = true
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	if err = l.waitCaptured(ctx, all); err != nil {
		return nil, err
	}
	return all, nil
}

// authorizeOriginal precedes EVERY outcome lookup and early reason. It proves
// original publication/bytes only; it deliberately does not substitute current
// reader/write checks or erase a captured pending proof on source retirement.
func (l *collaborationLiveOriginals) authorizeOriginal(access mooring.SessionBatchAccess) error {
	if l == nil || l.document == nil || l.document.cell == nil || access.Purpose != mooring.SessionProcessSealedOutcome || access.Access.Intent != "outcome" || access.Access.Operation == nil || access.Lease != l.document.owner || access.Access.Scope != l.document.key || !access.Access.FromStore(l.document.cell.store) {
		return errCollaborationHTTP
	}
	if access.Access.TaskID != "" {
		return errCollaborationHTTP
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	slot := l.changes[access.ChangeID]
	if slot == nil || !slot.captured || slot.captureErr != nil || slot.source == nil || slot.source.connection == nil || slot.source.connection.document != l.document || slot.source.connection.admission.participant != access.Access.Participant || slot.source.connection.origin != slot.payload.Origin || slot.change.MembershipHash != access.MembershipHash || len(slot.change.Operations) != 1 || !reflect.DeepEqual(slot.change.Operations[0], *access.Access.Operation) {
		return errCollaborationHTTP
	}
	return nil
}

func (l *collaborationLiveOriginals) finishCapture(fresh []*collaborationLiveOriginal, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, slot := range fresh {
		if slot.source.retired && err == nil {
			err = errCollaborationHTTP
		}
		slot.captureErr = err
		slot.captured = err == nil
		if err != nil {
			slot.source.retired = true
		}
		close(slot.ready)
	}
}

func (l *collaborationLiveOriginals) waitCaptured(ctx context.Context, all []*collaborationLiveOriginal) error {
	if l == nil || ctx == nil {
		return errCollaborationHTTP
	}
	for _, slot := range all {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-slot.ready:
		}
		l.mu.Lock()
		valid := slot.captured && slot.captureErr == nil && !slot.source.retired
		l.mu.Unlock()
		if !valid {
			return errCollaborationHTTP
		}
	}
	return nil
}
