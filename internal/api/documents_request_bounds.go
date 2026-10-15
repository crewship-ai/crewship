package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"unicode/utf16"

	"github.com/crewship-ai/crewship/internal/documents"
)

type documentSemanticPatch struct {
	Target string `json:"target"`
	Text   string `json:"text"`
}

func validDocumentTarget(t documentVerifiedTarget) bool {
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
		if operation != "commit" || ifRev != base || state.HeadRev != base || semantic == nil || len(hashes) != 1 {
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
