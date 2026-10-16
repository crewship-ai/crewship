package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf16"

	"github.com/crewship-ai/crewship/internal/documents"
)

type documentSemanticEdit struct {
	From   int    `json:"from"`
	To     int    `json:"to"`
	Insert string `json:"insert"`
}

type documentRequestScope struct {
	RequestID string                 `json:"requestID"`
	BaseRev   int64                  `json:"baseRev"`
	Target    documentVerifiedTarget `json:"target"`
}

type documentSemanticPatch struct {
	Target    string                   `json:"target"`
	Text      string                   `json:"text"`
	Edit      *documentSemanticEdit    `json:"edit"`
	Rich      *documentRichProof       `json:"rich,omitempty"`
	Structure *documentStructuralProof `json:"structure,omitempty"`
}

func validDocumentTarget(t documentVerifiedTarget) bool {
	if t.Structure != nil {
		return false
	}
	u := utf16.Encode([]rune(t.Text))
	q := utf16.Encode([]rune(t.Quote))
	boundary := func(n int) bool {
		return n >= 0 && n <= len(u) && (n == 0 || n == len(u) || !(u[n-1] >= 0xD800 && u[n-1] <= 0xDBFF && u[n] >= 0xDC00 && u[n] <= 0xDFFF))
	}
	return t.ID != "" && t.Hash != "" && t.From < t.To && boundary(t.From) && boundary(t.To) && len(q) > 0 && string(utf16.Decode(u[t.From:t.To])) == t.Quote
}

// The callback runs after receipt replay, within Store's SQLite writer fence.
// Live principal/delegation checks still run before every replay or mutation.
func (h *PageHandler) documentRequestGuard(rctx context.Context, page, operation, id string, ifRev int64, hashes map[string]string, semantic *documentSemanticPatch, projection json.RawMessage) documents.MutationGuard {
	a, ok := rctx.Value(artifactRunKey{}).(*artifactRunAuthority)
	if !ok {
		return nil
	}
	return func(ctx context.Context, tx *sql.Tx, actor string, state documents.State) error {
		var request string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(document_request_id,'') FROM page_artifact_run_delegations WHERE page_id=? AND run_id=?`, page, a.RunID).Scan(&request); err != nil {
			return errDocumentAccess
		}
		if request == "" {
			if semantic != nil && semantic.Structure != nil {
				return errDocumentAccess
			}
			var bound int
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM document_requests WHERE run_id=?) OR EXISTS(SELECT 1 FROM page_artifact_run_delegations WHERE run_id=? AND document_request_id IS NOT NULL)`, a.RunID, a.RunID).Scan(&bound); err != nil || bound != 0 {
				return errDocumentAccess
			}
			return nil // Only genuinely unbound generic runs retain their separate contract.
		}
		var base int64
		var raw, status, run string
		if err := tx.QueryRowContext(ctx, `SELECT base_rev,targets_json,status,COALESCE(run_id,'') FROM document_requests WHERE id=? AND page_id=?`, request, page).Scan(&base, &raw, &status, &run); err != nil || status != "running" || run != a.RunID {
			return errDocumentAccess
		}
		var targets []documentVerifiedTarget
		if json.Unmarshal([]byte(raw), &targets) != nil || len(targets) != 1 {
			return documents.ErrConflict
		}
		if operation == "checkpoint" {
			var count int
			if id != request+"-checkpoint" || ifRev != base+1 || state.HeadRev != base+1 {
				return documents.ErrConflict
			}
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM document_receipts WHERE workspace_id=? AND page_id=? AND actor=? AND kind='agent' AND rev=?`, a.WorkspaceID, page, actor, base+1).Scan(&count); err != nil || count != 1 {
				return documents.ErrConflict
			}
			return nil
		}
		if operation != "commit" || ifRev != base || state.HeadRev != base || semantic == nil {
			return documents.ErrConflict
		}
		if targets[0].Structure != nil {
			if id != request+"-patch" {
				return documents.ErrConflict
			}
			return documentStructuralGuard(ctx, tx, a.WorkspaceID, page, request, targets[0], semantic, hashes, projection, state)
		}
		if len(hashes) != 1 || semantic.Structure != nil {
			return documents.ErrConflict
		}
		var target *documentVerifiedTarget
		for i := range targets {
			if targets[i].ID == semantic.Target {
				target = &targets[i]
				break
			}
		}
		if target == nil || !validDocumentTarget(*target) || hashes[target.ID] != target.Hash {
			return documents.ErrConflict
		}
		original := utf16.Encode([]rune(target.Text))
		edit := semantic.Edit
		if edit == nil || edit.From < target.From || edit.To > target.To || edit.To < edit.From {
			return documents.ErrConflict
		}
		boundary := func(n int) bool {
			return n >= 0 && n <= len(original) && (n == 0 || n == len(original) || !(original[n-1] >= 0xD800 && original[n-1] <= 0xDBFF && original[n] >= 0xDC00 && original[n] <= 0xDFFF))
		}
		if !boundary(edit.From) || !boundary(edit.To) {
			return documents.ErrConflict
		}
		expected := string(utf16.Decode(original[:edit.From])) + edit.Insert + string(utf16.Decode(original[edit.To:]))
		if expected != semantic.Text {
			return documents.ErrConflict
		}
		replacement := utf16.Encode([]rune(semantic.Text))
		prefix, suffix := string(utf16.Decode(original[:target.From])), string(utf16.Decode(original[target.To:]))
		if len(replacement) < target.From+len(original)-target.To || !strings.HasPrefix(semantic.Text, prefix) || !strings.HasSuffix(semantic.Text, suffix) {
			return documents.ErrConflict
		}
		var before, after []DocumentBlockHash
		if json.Unmarshal(state.Projection, &before) != nil || json.Unmarshal(projection, &after) != nil || len(before) != len(after) {
			return documents.ErrConflict
		}
		allowed := map[string]bool{target.ID: true}
		for _, ancestor := range target.Ancestors {
			allowed[ancestor] = true
		}
		prior := map[string]string{}
		for _, b := range before {
			prior[b.ID] = b.Hash
		}
		seen := map[string]bool{}
		for _, b := range after {
			old, exists := prior[b.ID]
			if !exists || seen[b.ID] || (!allowed[b.ID] && old != b.Hash) {
				return documents.ErrConflict
			}
			seen[b.ID] = true
		}
		if prior[target.ID] != target.Hash {
			return documents.ErrConflict
		}
		return nil
	}
}

// checkDocumentRunPageScope applies immutable selected-job boundaries to every
// format sharing a run principal. An empty Page ID means standalone creation.
func (h *PageHandler) checkDocumentRunPageScope(ctx context.Context, page string, a *artifactRunAuthority) error {
	q := h.pageAuthReader(ctx)
	var selected string
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(slides_request_origin,'') FROM artifact_run_principals WHERE run_id=?`, a.RunID).Scan(&selected); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return errDocumentAccess
	}
	var associated bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM slides_requests WHERE original_run_id=?) OR EXISTS(SELECT 1 FROM page_artifact_run_delegations WHERE run_id=? AND slides_request_id IS NOT NULL)`, a.RunID, a.RunID).Scan(&associated); err != nil {
		return errDocumentAccess
	}
	if selected != "" || associated {
		transport, ok := ctx.Value(slidesSelectedTransportKey{}).(slidesSelectedTransport)
		if !ok || selected == "" || transport.RequestID != selected || transport.PageID != page {
			return errDocumentAccess
		}
		var matches bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM slides_requests WHERE id=? AND page_id=? AND original_run_id=?)`, selected, page, a.RunID).Scan(&matches); err != nil || !matches {
			return errDocumentAccess
		}
	}
	var other int
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM document_requests WHERE run_id=? AND page_id<>?) OR EXISTS(SELECT 1 FROM page_artifact_run_delegations WHERE run_id=? AND document_request_id IS NOT NULL AND page_id<>?)`, a.RunID, page, a.RunID, page).Scan(&other)
	if err != nil || other != 0 {
		return errDocumentAccess
	}
	return nil
}

// Read scope and document state share Store.Read's authorization transaction.
func (h *PageHandler) documentRequestReadScope(ctx context.Context, tx *sql.Tx, page string) (*documentRequestScope, error) {
	a, ok := ctx.Value(artifactRunKey{}).(*artifactRunAuthority)
	if !ok {
		return nil, nil
	}
	var origin string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(document_request_id,'') FROM page_artifact_run_delegations WHERE page_id=? AND run_id=?`, page, a.RunID).Scan(&origin); err != nil {
		return nil, errDocumentAccess
	}
	if origin == "" {
		return nil, nil
	}
	var base int64
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT base_rev,targets_json FROM document_requests WHERE id=? AND page_id=? AND run_id=? AND status='running'`, origin, page, a.RunID).Scan(&base, &raw); err != nil {
		return nil, errDocumentAccess
	}
	var targets []documentVerifiedTarget
	if json.Unmarshal([]byte(raw), &targets) != nil || len(targets) != 1 || (!validDocumentTarget(targets[0]) && !validDocumentStructureTarget(targets[0])) {
		return nil, documents.ErrConflict
	}
	return &documentRequestScope{RequestID: origin, BaseRev: base, Target: targets[0]}, nil
}
