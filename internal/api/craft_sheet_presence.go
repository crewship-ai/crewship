package api

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
)

const (
	craftSheetPresenceTTL   = 12 * time.Second
	craftSheetPresenceLimit = 128
	craftSheetRangeLimit    = 32
)

type craftSheetRange struct {
	Sheet string `json:"sheet"`
	A1    string `json:"a1"`
}

type craftSheetPresenceAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type craftSheetPresenceEvent struct {
	OpID      string                  `json:"op_id"`
	RunID     string                  `json:"run_id"`
	Agent     craftSheetPresenceAgent `json:"agent"`
	Phase     string                  `json:"phase"`
	Ranges    []craftSheetRange       `json:"ranges"`
	TTL       int64                   `json:"ttl_ms"`
	Truncated bool                    `json:"truncated,omitempty"`
}

type craftSheetPresenceSnapshot struct {
	Operations []craftSheetPresenceEvent `json:"operations"`
}

type craftSheetPresenceScope struct {
	page mooring.EditLeaseScope
	chat string
}

type craftSheetPresenceOperation struct {
	scope   craftSheetPresenceScope
	entry   *craftNativeAgentEntry
	ctx     context.Context
	expires time.Time
	event   craftSheetPresenceEvent
}

type craftSheetPresence struct {
	mu     sync.Mutex
	active map[string]craftSheetPresenceOperation
	change chan struct{}
}

func (p *craftSheetPresence) signal() {
	if p.change != nil {
		close(p.change)
		p.change = nil
	}
}

func (p *craftSheetPresence) prune(now time.Time) {
	for id, op := range p.active {
		if !now.Before(op.expires) || op.ctx.Err() != nil {
			delete(p.active, id)
		}
	}
}

// observe subscribes before returning the snapshot, so a start or end during
// stream authorization cannot be lost. Only the stream writes to its client.
func (p *craftSheetPresence) observe(scope craftSheetPresenceScope) ([]craftSheetPresenceOperation, <-chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune(time.Now())
	if p.change == nil {
		p.change = make(chan struct{})
	}
	var out []craftSheetPresenceOperation
	for _, op := range p.active {
		if op.scope == scope {
			out = append(out, op)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].event.OpID < out[j].event.OpID })
	return out, p.change
}

func (p *craftSheetPresence) start(op craftSheetPresenceOperation) craftSheetPresenceHandle {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune(time.Now())
	if len(p.active) >= craftSheetPresenceLimit {
		return craftSheetPresenceHandle{}
	}
	if p.active == nil {
		p.active = make(map[string]craftSheetPresenceOperation)
	}
	id := op.event.OpID
	p.active[id] = op
	p.signal()
	return craftSheetPresenceHandle{p, id}
}

type craftSheetPresenceHandle struct {
	p  *craftSheetPresence
	id string
}

func (h craftSheetPresenceHandle) update(ranges []craftSheetRange) {
	if h.p == nil || len(ranges) == 0 {
		return
	}
	h.p.mu.Lock()
	defer h.p.mu.Unlock()
	if op, ok := h.p.active[h.id]; ok && len(op.event.Ranges) == 0 {
		op.event.Ranges, op.event.Truncated = craftClipSheetPresenceRanges(ranges)
		h.p.active[h.id] = op
		h.p.signal()
	}
}

func (h craftSheetPresenceHandle) end() {
	if h.p == nil {
		return
	}
	h.p.mu.Lock()
	defer h.p.mu.Unlock()
	if _, ok := h.p.active[h.id]; ok {
		delete(h.p.active, h.id)
		h.p.signal()
	}
}

func (m *craftNativeAgentManager) sheetPresence(ctx context.Context, e *craftNativeAgentEntry, ranges []craftSheetRange) craftSheetPresenceHandle {
	if e.chat == nil || ctx.Err() != nil {
		return craftSheetPresenceHandle{}
	}
	id, err := craftNativeOpaque()
	if err != nil {
		return craftSheetPresenceHandle{}
	}
	a := e.chat.authority
	var name string
	if err = m.human.pages.db.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(substr(name,1,128),''),'Agent') FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, a.AgentID, a.WorkspaceID).Scan(&name); err != nil {
		return craftSheetPresenceHandle{}
	}
	if ranges == nil {
		ranges = []craftSheetRange{}
	}
	ranges, truncated := craftClipSheetPresenceRanges(ranges)
	return m.human.sheetPresence.start(craftSheetPresenceOperation{
		scope: craftSheetPresenceScope{page: e.grant.Scope, chat: a.ChatID}, ctx: ctx,
		entry: e, expires: time.Now().Add(craftNativeChatRunWriteWindow),
		event: craftSheetPresenceEvent{OpID: id, RunID: a.RunID, Agent: craftSheetPresenceAgent{ID: a.AgentID, Name: name}, Phase: "started", Ranges: ranges, Truncated: truncated, TTL: craftSheetPresenceTTL.Milliseconds()},
	})
}

func craftSheetPresenceWriteRanges(commands json.RawMessage) []craftSheetRange {
	ops, ok := craftNativeAgentResolvedOps(commands)
	if !ok {
		return nil
	}
	reads, ok := craftUndoReads(ops)
	if !ok {
		return nil
	}
	ranges := make([]craftSheetRange, 0, len(reads))
	for _, read := range reads {
		if r, ok := craftSheetPresenceRange(read.sheet, read.rng); ok {
			ranges = append(ranges, r)
		}
	}
	return ranges
}

func craftSheetPresenceReadRanges(query json.RawMessage) []craftSheetRange {
	var op craftUndoOp
	if json.Unmarshal(query, &op) != nil {
		return nil
	}
	var key string
	switch op.Command {
	case "cell.get":
		key = "cell"
	case "sheet.read":
		key = "range"
	default:
		return nil
	}
	address, err := nativeAgentSheetAddress("", op.Params, key)
	sheet, _, ok := craftSheetAddress(address)
	if err != nil || !ok {
		return nil
	}
	rng := address[strings.LastIndex(address, "!")+1:]
	if adjusted, ok := craftSheetPresenceRange(sheet, rng); ok {
		return []craftSheetRange{adjusted}
	}
	return nil
}

func craftSheetPresenceResolved(view json.RawMessage) []craftSheetRange {
	type read struct {
		Sheet string `json:"sheet"`
		Range string `json:"range"`
	}
	var answer struct {
		Result struct {
			read
			Columns []read `json:"columns"`
		} `json:"result"`
	}
	if json.Unmarshal(view, &answer) != nil {
		return nil
	}
	if r, ok := craftSheetPresenceRange(answer.Result.Sheet, answer.Result.Range); ok {
		return []craftSheetRange{r}
	}
	ranges := make([]craftSheetRange, 0, min(len(answer.Result.Columns), craftSheetRangeLimit+1))
	for _, column := range answer.Result.Columns {
		if r, ok := craftSheetPresenceRange(column.Sheet, column.Range); ok {
			ranges = append(ranges, r)
			if len(ranges) > craftSheetRangeLimit {
				break
			}
		}
	}
	return ranges
}

func craftClipSheetPresenceRanges(ranges []craftSheetRange) ([]craftSheetRange, bool) {
	if len(ranges) > craftSheetRangeLimit {
		return ranges[:craftSheetRangeLimit], true
	}
	return ranges, false
}

func craftSheetPresenceRange(sheet, rng string) (craftSheetRange, bool) {
	if pos := strings.LastIndex(rng, "!"); pos >= 0 {
		rng = rng[pos+1:]
	}
	r := craftSheetRange{Sheet: sheet, A1: rng}
	_, ok := craftWireSheetRange(r)
	return r, ok
}
