package api

// Agent proposals (#5137, #5233) on every schema that supports them
// (documents.SchemaAllows). An agent run admitted by a human's explicit
// targeted message naming the Doc's own schema proposes a replacement of one
// block's text; a human with Edit accepts (an ordinary commit bound to the proposal)
// or rejects it. Wire shape: FE-REQ-F3-4 (lib/documents/proposals.ts).
import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"regexp"

	"github.com/crewship-ai/crewship/internal/documents"
)

var documentProposalID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

// documentProposalInput extracts the stored proposal from the engine's
// semantic proof. The proof itself is re-verified by the semantic guard inside
// the writer transaction; this only reads the target's subtree and ancestry.
func documentProposalInput(id, fingerprint, run, target string, schema int, semantic *documentSemanticPatch, hashes map[string]string, ifRev int64, generation *int64) (documents.ProposalInput, error) {
	var in documents.ProposalInput
	if !documents.SchemaAllows(schema, documents.CapPropose) || semantic == nil || semantic.Rich == nil || semantic.Target != target || !documentRichID.MatchString(target) || len(hashes) != 1 || generation == nil {
		return in, documents.ErrInvalidProposal
	}
	before, err := documentRichTreeFor(semantic.Rich.Before, schema)
	if err != nil {
		return in, documents.ErrInvalidProposal
	}
	after, err := documentRichTreeFor(semantic.Rich.After, schema)
	if err != nil {
		return in, documents.ErrInvalidProposal
	}
	base, proposed := documentRichFind(before, target), documentRichFind(after, target)
	ancestors, found := documentRichAncestors(after, target)
	if base == nil || proposed == nil || !found {
		return in, documents.ErrInvalidProposal
	}
	baseJSON, err := json.Marshal(base)
	if err != nil {
		return in, err
	}
	proposedJSON, err := json.Marshal(proposed)
	if err != nil {
		return in, err
	}
	in = documents.ProposalInput{ID: id, Fingerprint: fingerprint, RunID: run, Target: target, Schema: schema, BaseHash: documentRichHash(base), ProposedHash: documentRichHash(proposed),
		Text: semantic.Text, Ancestors: ancestors, Base: baseJSON, Proposed: proposedJSON, IfRev: ifRev, Generation: generation}
	if hashes[target] != in.BaseHash {
		return in, documents.ErrInvalidProposal
	}
	return in, nil
}

// documentRichAncestors returns the block IDs enclosing a target, nearest first.
func documentRichAncestors(root map[string]any, target string) ([]string, bool) {
	var path []string
	var visit func(map[string]any) bool
	visit = func(node map[string]any) bool {
		attrs, _ := node["attrs"].(map[string]any)
		if attrs != nil && attrs["id"] == target {
			return true
		}
		children, _ := node["content"].([]any)
		for _, value := range children {
			child, ok := value.(map[string]any)
			if ok && visit(child) {
				if attrs != nil {
					if id, ok := attrs["id"].(string); ok {
						path = append(path, id)
					}
				}
				return true
			}
		}
		return false
	}
	if !visit(root) {
		return nil, false
	}
	return path, true
}

// documentProposalGuard re-checks, under the writer fence, that this run holds
// a live propose mandate on this Page that names the target, against the head.
func (h *PageHandler) documentProposalGuard(a *artifactRunAuthority, k documents.Key, target string, ifRev int64) documents.MutationGuard {
	return func(ctx context.Context, tx *sql.Tx, actor string, state documents.State) error {
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		selected, err := h.documentIterationRun(ctx, a)
		if err != nil || !selected {
			return errDocumentAccess
		}
		scope, err := h.documentIterationScope(ctx, a, k.PageID)
		if err != nil {
			return err
		}
		if !scope.Propose || !scope.admits(target) {
			return errDocumentAccess
		}
		if k.WorkspaceID != a.WorkspaceID || actor != "agent:"+a.AgentID+":"+a.RunID || ifRev != state.HeadRev || state.HeadRev != scope.Rev || state.Generation != scope.Generation || state.Schema != scope.Schema || !documents.SchemaAllows(state.Schema, documents.CapPropose) {
			return documents.ErrConflict
		}
		return nil
	}
}

// DocumentProposalTarget names the blocks a proposal changes.
type DocumentProposalTarget struct {
	BlockIDs []string `json:"block_ids"`
}

// DocumentProposalWire is one proposal as the review UI and CLI read it.
type DocumentProposalWire struct {
	ID              string                 `json:"id"`
	Author          string                 `json:"author"`
	RunID           string                 `json:"run_id,omitempty"`
	Target          DocumentProposalTarget `json:"target"`
	BaseRev         int64                  `json:"base_rev"`
	Status          string                 `json:"status"`
	Text            string                 `json:"text"`
	CreatedAt       string                 `json:"created_at"`
	DecidedBy       string                 `json:"decided_by,omitempty"`
	AcceptedRev     *int64                 `json:"accepted_rev,omitempty"`
	BaseContent     json.RawMessage        `json:"base_content,omitempty"`
	ProposedContent json.RawMessage        `json:"proposed_content,omitempty"`
}

func documentProposalWire(p documents.Proposal) DocumentProposalWire {
	w := DocumentProposalWire{ID: p.ID, Author: p.Actor, RunID: p.RunID, BaseRev: p.BaseRev, Status: p.Status, Text: p.Text, CreatedAt: p.CreatedAt, DecidedBy: p.DecidedBy, AcceptedRev: p.AcceptedRev, BaseContent: p.Base, ProposedContent: p.Proposed}
	w.Target = DocumentProposalTarget{BlockIDs: []string{p.Target}}
	return w
}

// DocumentProposalList is GET .../document/proposals.
type DocumentProposalList struct {
	PageID    string                 `json:"page_id"`
	Proposals []DocumentProposalWire `json:"proposals"`
}

// DocumentProposalDecision is the accept or reject request body.
type DocumentProposalDecision struct {
	RequestID   string `json:"request_id"`
	ExpectedRev int64  `json:"expected_rev"`
}

// DocumentProposalReceipt is the accept or reject outcome.
type DocumentProposalReceipt struct {
	PageID    string `json:"page_id"`
	Proposal  string `json:"proposal"`
	ID        string `json:"id"`
	Rev       int64  `json:"rev"`
	Kind      string `json:"kind"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

func (h *PageHandler) documentProposalPage(w http.ResponseWriter, r *http.Request) (*pageRecord, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if agent, _ := pageGrantCallerIsAgent(r); agent {
		replyError(w, 403, "human document credentials required")
		return nil, false
	}
	rec, ok := h.documentPage(w, r)
	if !ok || !h.auditPrivateOwnerAccess(w, r, rec) {
		return nil, false
	}
	return rec, true
}

func (h *PageHandler) forwardDocumentProposal(w http.ResponseWriter, r *http.Request, rec *pageRecord, operation string, body any) ([]byte, bool) {
	raw, status, err := h.documentCall(r.Context(), WorkspaceIDFromContext(r.Context()), "Bearer "+extractToken(r), rec.ID, operation, body)
	if err != nil {
		replyError(w, 503, "document engine unavailable; nothing was decided")
		return nil, false
	}
	if status >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(raw)
		return nil, false
	}
	return raw, true
}

// ListDocumentProposals serves GET /api/v1/pages/{slug}/document/proposals.
func (h *PageHandler) ListDocumentProposals(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.documentProposalPage(w, r)
	if !ok {
		return
	}
	raw, ok := h.forwardDocumentProposal(w, r, rec, "proposals", map[string]any{})
	if !ok {
		return
	}
	var value struct {
		Proposals []documents.Proposal `json:"proposals"`
	}
	if json.Unmarshal(raw, &value) != nil || value.Proposals == nil {
		replyError(w, 502, "invalid document engine response")
		return
	}
	out := DocumentProposalList{PageID: rec.ID, Proposals: make([]DocumentProposalWire, 0, len(value.Proposals))}
	for _, p := range value.Proposals {
		p.Base, p.Proposed = nil, nil
		out.Proposals = append(out.Proposals, documentProposalWire(p))
	}
	writeJSON(w, 200, out)
}

// ReadDocumentProposal serves GET /api/v1/pages/{slug}/document/proposals/{proposal}.
func (h *PageHandler) ReadDocumentProposal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("proposal")
	if !documentProposalID.MatchString(id) {
		replyError(w, 400, "proposal ID required")
		return
	}
	rec, ok := h.documentProposalPage(w, r)
	if !ok {
		return
	}
	raw, ok := h.forwardDocumentProposal(w, r, rec, "proposal", map[string]any{"proposal": id})
	if !ok {
		return
	}
	var p documents.Proposal
	if json.Unmarshal(raw, &p) != nil || p.ID != id || len(p.Base) == 0 || len(p.Proposed) == 0 {
		replyError(w, 502, "invalid document engine response")
		return
	}
	writeJSON(w, 200, documentProposalWire(p))
}

// AcceptDocumentProposal serves POST .../document/proposals/{proposal}/accept.
func (h *PageHandler) AcceptDocumentProposal(w http.ResponseWriter, r *http.Request) {
	h.decideDocumentProposal(w, r, "proposal-accept")
}

// RejectDocumentProposal serves POST .../document/proposals/{proposal}/reject.
func (h *PageHandler) RejectDocumentProposal(w http.ResponseWriter, r *http.Request) {
	h.decideDocumentProposal(w, r, "proposal-reject")
}

// A decision is closed: request_id is the idempotency key (and must equal an
// Idempotency-Key header when one is sent) and expected_rev is the head the
// reviewer saw. A changed head or target returns 409 and decides nothing.
func (h *PageHandler) decideDocumentProposal(w http.ResponseWriter, r *http.Request, operation string) {
	id := r.PathValue("proposal")
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	fields, shapeErr := documentDraftObject(raw)
	var input DocumentProposalDecision
	if err != nil || shapeErr != nil || len(fields) != 2 || fields["request_id"] == nil || fields["expected_rev"] == nil || json.Unmarshal(raw, &input) != nil ||
		!documentProposalID.MatchString(id) || !documentProposalID.MatchString(input.RequestID) || input.ExpectedRev < 0 || input.ExpectedRev >= 9007199254740991 {
		replyError(w, 400, "closed decision with request_id and expected_rev required")
		return
	}
	if key := r.Header.Get("Idempotency-Key"); key != "" && key != input.RequestID {
		replyError(w, 400, "Idempotency-Key must equal request_id")
		return
	}
	rec, ok := h.documentProposalPage(w, r)
	if !ok {
		return
	}
	result, ok := h.forwardDocumentProposal(w, r, rec, operation, map[string]any{"id": input.RequestID, "proposal": id, "ifRev": input.ExpectedRev})
	if !ok {
		return
	}
	var receipt struct {
		ID        string `json:"id"`
		Rev       int64  `json:"rev"`
		Kind      string `json:"kind"`
		Duplicate bool   `json:"duplicate"`
	}
	want := "human"
	if operation == "proposal-reject" {
		want = "proposal-reject"
	}
	if json.Unmarshal(result, &receipt) != nil || receipt.ID != input.RequestID || receipt.Kind != want {
		replyError(w, 502, "proposal decision confirmation unavailable; retry the same request_id")
		return
	}
	writeJSON(w, 200, DocumentProposalReceipt{PageID: rec.ID, Proposal: id, ID: receipt.ID, Rev: receipt.Rev, Kind: receipt.Kind, Duplicate: receipt.Duplicate})
}
