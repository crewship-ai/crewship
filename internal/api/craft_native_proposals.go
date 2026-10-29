package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// Agent proposals are reviewed only through an admitted native session, the
// same X-Craft-Session proof every other native document route requires.
type craftNativeProposalResponse struct {
	ID            string                    `json:"id"`
	Author        craftNativeHolderResponse `json:"author"`
	BaseVersion   int64                     `json:"base_version"`
	Summary       string                    `json:"summary"`
	CreatedAt     string                    `json:"created_at"`
	State         string                    `json:"state"`
	ResultVersion int64                     `json:"result_version,omitempty"`
}

type craftNativeProposalListResponse struct {
	Proposals []craftNativeProposalResponse `json:"proposals"`
}

type craftNativeProposalDocumentResponse struct {
	Proposal    craftNativeProposalResponse `json:"proposal"`
	Bytes       []byte                      `json:"bytes"`
	ContentHash string                      `json:"content_hash"`
	EnginePin   string                      `json:"engine_pin"`
}

type craftNativeProposalAcceptResponse struct {
	Proposal craftNativeProposalResponse `json:"proposal"`
	Receipt  mooring.NativeSaveReceipt   `json:"receipt"`
}

func (d *craftNativeDocumentsHTTP) proposalView(r *http.Request, workspaceID string, p mooring.NativeProposal) craftNativeProposalResponse {
	agent := strings.TrimPrefix(p.AuthorActorID, "agent:")
	name := "Agent"
	// A removed agent keeps the generic label; any other failure is logged
	// and still answers with the label rather than failing the review.
	if err := d.human.pages.db.QueryRowContext(r.Context(), `SELECT COALESCE(NULLIF(name,''),'Agent') FROM agents WHERE id=? AND workspace_id=?`, agent, workspaceID).Scan(&name); err != nil {
		name = "Agent"
		if !errors.Is(err, sql.ErrNoRows) && d.human.pages.logger != nil {
			d.human.pages.logger.Warn("craft native: proposal author name lookup failed", "proposal", p.ID, "error", err)
		}
	}
	return craftNativeProposalResponse{ID: p.ID, Author: craftNativeHolderResponse{ActorID: p.AuthorActorID, DisplayName: name, Kind: "agent", AgentID: agent}, BaseVersion: p.BaseVersion, Summary: p.Summary, CreatedAt: p.CreatedAt, State: p.State, ResultVersion: p.ResultVersion}
}

func craftNativeProposalError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mooring.ErrNativeProposalNotFound):
		replyError(w, 404, "native Craft proposal not found")
	case errors.Is(err, mooring.ErrNativeProposalDecided):
		replyError(w, 409, "native Craft proposal already decided")
	default:
		craftNativeDocumentError(w, err)
	}
}

func (d *craftNativeDocumentsHTTP) listProposals(w http.ResponseWriter, r *http.Request) {
	r, entry, s, _, err := d.bind(r)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	list, err := s.Proposals(r.Context(), entry.scope)
	if err != nil {
		craftNativeProposalError(w, err)
		return
	}
	out := make([]craftNativeProposalResponse, 0, len(list))
	for _, p := range list {
		out = append(out, d.proposalView(r, entry.scope.WorkspaceID, p))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, craftNativeProposalListResponse{Proposals: out})
}

// proposalDocument returns the candidate next to its base so a human can
// compare before deciding. It never changes the document or the lease.
func (d *craftNativeDocumentsHTTP) proposalDocument(w http.ResponseWriter, r *http.Request) {
	r, entry, s, _, err := d.bind(r)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	p, err := s.ProposalContent(r.Context(), entry.scope, r.PathValue("proposalID"))
	if err != nil {
		craftNativeProposalError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, craftNativeProposalDocumentResponse{Proposal: d.proposalView(r, entry.scope.WorkspaceID, p), Bytes: p.Content, ContentHash: p.ContentHash, EnginePin: s.EnginePin()})
}

type craftNativeProposalDecisionBody struct {
	SessionID  string `json:"sessionId"`
	Generation string `json:"generation"`
}

func (d *craftNativeDocumentsHTTP) decideProposal(w http.ResponseWriter, r *http.Request, accept bool) {
	r, entry, s, _, err := d.bind(r)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	var body craftNativeProposalDecisionBody
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		replyError(w, 400, "invalid proposal decision")
		return
	}
	id := r.PathValue("proposalID")
	w.Header().Set("Cache-Control", "no-store")
	if !accept {
		p, err := s.RejectProposal(r.Context(), entry.scope, id)
		if err != nil {
			craftNativeProposalError(w, err)
			return
		}
		writeJSON(w, 200, d.proposalView(r, entry.scope.WorkspaceID, p))
		return
	}
	// Accepting is a write: it needs this admission's own current lease.
	generation, err := craftNativeGeneration(body.Generation)
	if err != nil || body.SessionID != entry.id {
		replyError(w, 400, "proposal decision binding mismatch")
		return
	}
	lease := mooring.EditLease{Scope: entry.scope, Holder: craftNativePrincipal(entry), Generation: generation}
	p, receipt, err := s.AcceptProposal(r.Context(), lease, id)
	if err != nil {
		if p.State == "stale" {
			writeJSON(w, 409, map[string]any{"error": "the document changed after this proposal; it was not applied", "proposal": d.proposalView(r, entry.scope.WorkspaceID, p)})
			return
		}
		craftNativeProposalError(w, err)
		return
	}
	writeJSON(w, 200, craftNativeProposalAcceptResponse{Proposal: d.proposalView(r, entry.scope.WorkspaceID, p), Receipt: receipt})
}
func (d *craftNativeDocumentsHTTP) acceptProposal(w http.ResponseWriter, r *http.Request) {
	d.decideProposal(w, r, true)
}
func (d *craftNativeDocumentsHTTP) rejectProposal(w http.ResponseWriter, r *http.Request) {
	d.decideProposal(w, r, false)
}
