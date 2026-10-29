package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// This slot belongs to the original live task, never a bearer-selected document.
// An uncertain request retains its correlation tombstone until its exact reply
// or runtime closure. Output never waits for callMu or for an abandoned caller.
type collaborationAgentPageRead struct {
	id                      protocol.Identifier
	revision                protocol.Counter
	limit                   int
	ready                   chan struct{}
	raw                     []byte
	done, abandoned, failed bool
}

type collaborationAgentPageArgs struct {
	cursor   *string
	revision *protocol.Counter
	limit    int
}

func collaborationPageCounter(raw string) (protocol.Counter, error) {
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || strconv.FormatUint(n, 10) != raw {
		return "", errCollaborationAgentAccess
	}
	return protocol.Counter(raw), nil
}

func collaborationPageQuery(r *http.Request, workspace string) (*collaborationAgentPageArgs, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, errCollaborationAgentAccess
	}
	for key, values := range query {
		if len(values) != 1 {
			return nil, errCollaborationAgentAccess
		}
		switch key {
		case "workspace_id":
			if values[0] != workspace {
				return nil, errCollaborationAgentAccess
			}
		case "page", "cursor", "revision", "max_bytes":
		default:
			return nil, errCollaborationAgentAccess
		}
	}
	if query.Get("page") == "" {
		if len(query) != 0 && !(len(query) == 1 && query.Get("workspace_id") == workspace) {
			return nil, errCollaborationAgentAccess
		}
		return nil, nil // The original empty-object whole-D1 read.
	}
	if query.Get("page") != "1" {
		return nil, errCollaborationAgentAccess
	}
	args := &collaborationAgentPageArgs{limit: 128 << 10}
	if values, present := query["cursor"]; present {
		if values[0] == "" || len(values[0]) > 4096 {
			return nil, errCollaborationAgentAccess
		}
		args.cursor = &values[0]
	}
	if values, present := query["revision"]; present {
		revision, err := collaborationPageCounter(values[0])
		if err != nil {
			return nil, err
		}
		args.revision = &revision
	}
	if args.cursor != nil && args.revision == nil {
		return nil, errCollaborationAgentAccess
	}
	if values, present := query["max_bytes"]; present {
		counter, err := collaborationPageCounter(values[0])
		if err != nil {
			return nil, err
		}
		limit, _ := strconv.ParseUint(string(counter), 10, 64)
		if limit < 1024 || limit > 256<<10 {
			return nil, errCollaborationAgentAccess
		}
		args.limit = int(limit)
	}
	return args, nil
}

func (a *collaborationLiveAgent) abandonPageRead(p *collaborationAgentPageRead) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pageRead == p {
		p.abandoned = true
		p.raw = nil
	}
}

func (a *collaborationLiveAgent) readPage(w http.ResponseWriter, r *http.Request, args *collaborationAgentPageArgs) {
	d := a.document
	revision := args.revision
	if args.cursor == nil {
		snapshot, err := d.cell.agentCheckpoint(r, a.confirmed.store, a.confirmed.mandate, d.owner)
		if err != nil {
			replyError(w, 403, "document task denied")
			return
		}
		if revision != nil && *revision != snapshot.Checkpoint.Revision {
			replyError(w, 409, "document revision changed; start a new read")
			return
		}
		current := snapshot.Checkpoint.Revision
		revision = &current
	}
	id, err := collaborationOpaqueID()
	if err != nil || revision == nil {
		replyError(w, 503, "document read unavailable")
		return
	}
	p := &collaborationAgentPageRead{id: protocol.Identifier(id), revision: *revision, limit: args.limit, ready: make(chan struct{})}
	inner, err := json.Marshal(protocol.ReadRequest{Type: "read_request", RequestID: p.id, Scope: d.wireScope(), SessionID: protocol.Identifier(d.activation.SessionID()), Revision: revision, Cursor: args.cursor, MaxBytes: protocol.PositiveCounter(strconv.Itoa(args.limit))})
	if err != nil {
		replyError(w, 503, "document read unavailable")
		return
	}
	a.mu.Lock()
	if a.pageRead != nil && !a.pageRead.done {
		a.mu.Unlock()
		replyError(w, 503, "previous document read is still unresolved")
		return
	}
	a.pageRead = p // BEFORE any possible physical request write.
	a.mu.Unlock()
	defer a.abandonPageRead(p)
	attempted := false
	err = a.run(r.Context(), func(ctx context.Context) error {
		return d.submissions.withWriter(ctx, func(tx *sql.Tx) error {
			if err := a.checkGate(ctx, tx, "read", 0); err != nil {
				return err
			}
			attempted = true
			err := d.cell.runtime.SendParticipantInput(ctx, d.activation, protocol.SessionParticipantInput{Type: "session_participant_input", RequestID: p.id, ControlProfile: mooring.PrivateSessionProfile, Scope: d.wireScope(), SessionID: protocol.Identifier(d.activation.SessionID()), RuntimeRef: protocol.Identifier(d.cell.runtime.RuntimeRef()), Fence: d.wireFence(), ConnectionRef: protocol.Identifier(a.ref), ParticipantRef: protocol.Identifier(a.participant.id), GateID: protocol.Identifier(a.gate.GateID), PermissionVersion: protocol.Counter(a.gate.PermissionVersion), Message: inner})
			return err
		})
	})
	if err != nil {
		if !attempted {
			a.mu.Lock()
			if a.pageRead == p {
				a.pageRead = nil // Proven unsent, no late reply can own this slot.
			}
			a.mu.Unlock()
		}
		replyError(w, 503, "document read unavailable")
		return
	}
	select {
	case <-r.Context().Done():
		replyError(w, 503, "document read unresolved; retain the same cursor")
		return
	case <-d.ctx.Done():
		replyError(w, 503, "document read unavailable")
		return
	case <-p.ready:
	}
	a.mu.Lock()
	raw, failed := p.raw, p.failed
	a.mu.Unlock()
	if len(raw) == 0 {
		replyError(w, 503, "document read unavailable")
		return
	}
	status := http.StatusOK
	if failed {
		status = http.StatusConflict
	}
	// The existing final current-rights writer/lease gate spans HTTP Flush.
	_ = a.sendStatus(r, w, raw, status)
}

func (a *collaborationLiveAgent) capturePageRead(ctx context.Context, token mooring.SessionControl, q protocol.SessionParticipantOutput, raw []byte, retired *mooring.SessionRetiredDelivery) error {
	d := a.document
	var reply struct {
		Type      string              `json:"type"`
		RequestID protocol.Identifier `json:"request_id"`
		Scope     protocol.Scope      `json:"scope"`
		Revision  *protocol.Counter   `json:"revision"`
	}
	if json.Unmarshal(raw, &reply) != nil || q.Scope != d.wireScope() || q.SessionID != protocol.Identifier(d.activation.SessionID()) || q.RuntimeRef != protocol.Identifier(d.cell.runtime.RuntimeRef()) || q.Fence != d.wireFence() || reply.Scope != d.wireScope() {
		return errCollaborationAgentAccess
	}
	a.mu.Lock()
	p := a.pageRead
	valid := p != nil && !p.done && q.RequestID == p.id && reply.RequestID == p.id &&
		(reply.Type == "protocol_error" && len(raw) <= 256<<10 ||
			reply.Type == "read_result" && len(raw) <= p.limit && reply.Revision != nil && *reply.Revision == p.revision)
	a.mu.Unlock()
	if !valid {
		return errCollaborationAgentAccess
	}
	if retired != nil {
		return nil // The caller has validated the exact retired wrapper/token.
	}
	err := a.run(ctx, func(ctx context.Context) error {
		return d.submissions.withWriter(ctx, func(tx *sql.Tx) error { return a.checkGate(ctx, tx, "read", 0) })
	})
	if err != nil {
		if !errors.Is(err, errCollaborationAuthorChanged) {
			return err
		}
		a.deliveryMu.Lock()
		err = a.retireClosedGate(ctx)
		a.deliveryMu.Unlock()
		if err != nil {
			return err
		}
		a.mu.Lock()
		proof := a.retired
		a.mu.Unlock()
		return d.cell.runtime.ValidateRetiredOutput(token, proof)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pageRead != p || p.done {
		return errCollaborationAgentAccess
	}
	p.failed = reply.Type == "protocol_error"
	if !p.abandoned {
		p.raw = append([]byte(nil), raw...)
	}
	p.done = true
	close(p.ready)
	return nil // Pump alone completes the charged control token exactly once.
}
