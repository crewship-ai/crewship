package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/documents"
)

func documentDraftPayload(t *testing.T, id, before, after string, rev int64, lease documents.Lease) map[string]any {
	t.Helper()
	tree := func(text string) json.RawMessage {
		b, e := json.Marshal(map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "attrs": map[string]any{"id": "paragraph"}, "content": []any{map[string]any{"type": "text", "text": text}}}}})
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	rawBefore, rawAfter := tree(before), tree(after)
	first, e := documentRichTree(rawBefore)
	if e != nil {
		t.Fatal(e)
	}
	last, e := documentRichTree(rawAfter)
	if e != nil {
		t.Fatal(e)
	}
	return map[string]any{"id": id, "fingerprint": id, "schema": 3, "kind": "agent", "generation": 0, "ifRev": rev, "epoch": lease.Epoch, "leaseToken": lease.Writer, "leaseOwner": lease.Owner, "snapshot": []byte("synthetic candidate " + after), "update": []byte("synthetic delta " + after), "blocks": json.RawMessage(richTestProjection(t, last)), "ifHashes": map[string]string{"paragraph": documentRichHash(documentRichFind(first, "paragraph"))}, "semantic": documentSemanticPatch{Target: "paragraph", Text: after, Edit: &documentSemanticEdit{From: 0, To: len(before), Insert: after}, Rich: &documentRichProof{Before: rawBefore, After: rawAfter}}}
}
func callDocumentDraftAuthority(t *testing.T, f *artifactPrincipalFixture, h *documentAuthority, token, page, op string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, e := json.Marshal(body)
	if e != nil {
		t.Fatal(e)
	}
	request := documentDraftRequest(f, token, "POST", string(raw))
	request.SetPathValue("page", page)
	request.SetPathValue("operation", op)
	response := httptest.NewRecorder()
	checked, ok := f.pages.artifactAgentRequest(response, request)
	if !ok {
		t.Fatalf("run control:%d", response.Code)
	}
	h.operation(response, checked)
	return response
}
func initializeDocumentDraftProjection(t *testing.T, f *artifactPrincipalFixture, page string) {
	t.Helper()
	p := documentDraftPayload(t, "fixture", "initial", "draft one", 0, documents.Lease{})
	s := p["semantic"].(documentSemanticPatch)
	tree, e := documentRichTree(s.Rich.Before)
	if e != nil {
		t.Fatal(e)
	}
	mustExec(t, f.rig.db, `UPDATE documents SET projection=? WHERE page_id=?`, []byte(richTestProjection(t, tree)), page)
}
func documentDraftLease(t *testing.T, f *artifactPrincipalFixture, h *documentAuthority, token, page string) documents.Lease {
	t.Helper()
	w := callDocumentDraftAuthority(t, f, h, token, page, "lease", map[string]any{"owner": "synthetic-engine"})
	wantCode(t, w, 200, "draft lease")
	var l struct {
		Token, Owner string
		Epoch        int64
	}
	if json.Unmarshal(w.Body.Bytes(), &l) != nil {
		t.Fatal("invalid lease")
	}
	return documents.Lease{Writer: l.Token, Owner: l.Owner, Epoch: l.Epoch}
}
func TestDocumentDraftCommitCursorReplayAndCheckpoint(t *testing.T) {
	f, h, token, created := newDocumentDraftFixture(t, true)
	f.rig.db.SetMaxOpenConns(1)
	initializeDocumentDraftProjection(t, f, created.PageID)
	lease := documentDraftLease(t, f, h, token, created.PageID)
	first := documentDraftPayload(t, "draft-one", "initial", "draft one", 0, lease)
	wantCode(t, callDocumentDraftAuthority(t, f, h, token, created.PageID, "commit", first), 200, "first draft")
	checkpoint := map[string]any{"id": "draft-one-checkpoint", "fingerprint": "first pin", "generation": 0, "ifRev": 1, "epoch": lease.Epoch, "leaseToken": lease.Writer, "leaseOwner": lease.Owner, "snapshot": []byte("synthetic candidate draft one")}
	wantCode(t, callDocumentDraftAuthority(t, f, h, token, created.PageID, "checkpoint", checkpoint), 200, "confirmed pin")
	wantCode(t, callDocumentDraftAuthority(t, f, h, token, created.PageID, "commit", documentDraftPayload(t, "draft-two", "draft one", "draft two", 1, lease)), 200, "second draft")
	retry := callDocumentDraftAuthority(t, f, h, token, created.PageID, "commit", first)
	wantCode(t, retry, 200, "original exact retry after cursor advance")
	var receipt documents.Receipt
	if json.Unmarshal(retry.Body.Bytes(), &receipt) != nil || receipt.Rev != 1 {
		t.Fatal("retry rewrote original receipt")
	}
	first["fingerprint"] = "changed original payload"
	wantCode(t, callDocumentDraftAuthority(t, f, h, token, created.PageID, "commit", first), 409, "changed same-ID payload")
	var head, cursor, receipts, pins, creations int
	if e := f.rig.db.QueryRow(`SELECT d.head_rev,c.draft_rev,(SELECT COUNT(*) FROM document_receipts WHERE page_id=d.page_id),(SELECT COUNT(*) FROM document_checkpoints WHERE page_id=d.page_id),(SELECT COUNT(*) FROM document_creations WHERE page_id=d.page_id) FROM documents d JOIN document_creation_consents c ON c.result_page_id=d.page_id WHERE d.page_id=?`, created.PageID).Scan(&head, &cursor, &receipts, &pins, &creations); e != nil || head != 2 || cursor != 2 || receipts != 4 || pins != 1 || creations != 1 {
		t.Fatalf("head=%d cursor=%d receipts=%d pins=%d creations=%d err=%v", head, cursor, receipts, pins, creations, e)
	}
}
func TestDocumentDraftFailedCommitRollsBackServerCursor(t *testing.T) {
	for _, failure := range []string{"receipt", "head", "rich proof", "stale generation", "stale revision"} {
		t.Run(failure, func(t *testing.T) {
			f, h, token, created := newDocumentDraftFixture(t, true)
			initializeDocumentDraftProjection(t, f, created.PageID)
			lease := documentDraftLease(t, f, h, token, created.PageID)
			p := documentDraftPayload(t, "rejected", "initial", "draft one", 0, lease)
			want := 409
			switch failure {
			case "receipt":
				mustExec(t, f.rig.db, `CREATE TRIGGER reject_draft_receipt BEFORE INSERT ON document_receipts WHEN NEW.kind='agent' BEGIN SELECT RAISE(ABORT,'synthetic receipt fault'); END`)
				want = 500
			case "head":
				mustExec(t, f.rig.db, `CREATE TRIGGER reject_draft_head BEFORE UPDATE OF head_rev ON documents BEGIN SELECT RAISE(ABORT,'synthetic head fault'); END`)
				want = 500
			case "rich proof":
				s := p["semantic"].(documentSemanticPatch)
				s.Text = "forged"
				p["semantic"] = s
			case "stale generation":
				p["generation"] = 1
			case "stale revision":
				p["ifRev"] = 1
			}
			wantCode(t, callDocumentDraftAuthority(t, f, h, token, created.PageID, "commit", p), want, "draft rollback")
			var head, cursor, count int
			if e := f.rig.db.QueryRow(`SELECT head_rev,(SELECT draft_rev FROM document_creation_consents WHERE result_page_id=documents.page_id),(SELECT COUNT(*) FROM document_receipts WHERE id='rejected') FROM documents WHERE page_id=?`, created.PageID).Scan(&head, &cursor, &count); e != nil || head != 0 || cursor != 0 || count != 0 {
				t.Fatalf("failed draft persisted head=%d cursor=%d receipt=%d %v", head, cursor, count, e)
			}
		})
	}
}
func TestDocumentDraftAuthorityRevocationAndHumanChanges(t *testing.T) {
	for _, change := range []string{"consent revoked", "principal expired", "run ended", "policy disabled", "owner changed", "human suspended", "human edited", "generation changed"} {
		t.Run(change, func(t *testing.T) {
			f, h, token, created := newDocumentDraftFixture(t, true)
			switch change {
			case "consent revoked":
				mustExec(t, f.rig.db, `UPDATE document_creation_consents SET revoked_at='2026-10-18T00:00:00Z'`)
			case "principal expired":
				mustExec(t, f.rig.db, `UPDATE artifact_run_principals SET expires_at='2000-01-01T00:00:00Z'`)
			case "run ended":
				mustExec(t, f.rig.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,crew_id,trace_id,entry_type,actor_type,summary) VALUES('draft-ended',?,?,?,?,'run.completed','system','ended')`, f.a.WorkspaceID, f.a.AgentID, f.a.CrewID, f.a.RunID)
			case "policy disabled":
				mustExec(t, f.rig.db, `UPDATE workspace_artifact_creation_policy SET enabled=0`)
			case "owner changed":
				mustExec(t, f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id=?`, f.rig.adminID, created.PageID)
			case "human suspended":
				mustExec(t, f.rig.db, `UPDATE users SET suspended_at='2026-10-18T00:00:00Z' WHERE id=?`, f.a.HumanUserID)
			case "human edited":
				mustExec(t, f.rig.db, `UPDATE documents SET head_rev=head_rev+1 WHERE page_id=?`, created.PageID)
			case "generation changed":
				mustExec(t, f.rig.db, `UPDATE documents SET generation=generation+1 WHERE page_id=?`, created.PageID)
			}
			w := callDocumentDraftAuthority(t, f, h, token, created.PageID, "state", map[string]any{})
			want := 403
			if change == "human edited" || change == "generation changed" {
				want = 409
			}
			wantCode(t, w, want, "revoked drafting scope")
			var cursor int
			if e := f.rig.db.QueryRow(`SELECT draft_rev FROM document_creation_consents WHERE run_id=?`, f.a.RunID).Scan(&cursor); e != nil || cursor != 0 {
				t.Fatalf("revocation moved cursor:%d %v", cursor, e)
			}
		})
	}
}
func TestDocumentDraftProtectedWireIsBoundedAndKeepsCheckpointEvidence(t *testing.T) {
	f, _, token, created := newDocumentDraftFixture(t, true)
	hash := strings.Repeat("a", 64)
	f.pages.documentCall = func(_ context.Context, _, bearer, page, op string, body any) ([]byte, int, error) {
		if !strings.HasPrefix(bearer, "Bearer agtv2.") || page != created.PageID {
			t.Fatal("original scoped bearer/Page lost")
		}
		if op == "read" {
			return []byte(fmt.Sprintf(`{"schema":3,"rev":0,"generation":0,"epoch":"opaque","snapshot":"PRIVATE_BINARY_MUST_NOT_ESCAPE","outline":[{"id":"paragraph","type":"paragraph","parent":null,"hash":%q,"text":"draft"},{"id":"code","type":"codeBlock","parent":null,"hash":%q,"text":"code"}]}`, hash, hash)), 200, nil
		}
		return []byte(`{"id":"pin","rev":0,"epoch":"opaque","kind":"checkpoint","duplicate":true}`), 200, nil
	}
	w := httptest.NewRecorder()
	f.pages.GetDocumentDraftAgent(w, documentDraftRequest(f, token, "GET", ""))
	wantCode(t, w, 200, "scoped view")
	var view DocumentDraftView
	if json.Unmarshal(w.Body.Bytes(), &view) != nil || len(view.Targets) != 1 || view.PageID != created.PageID || strings.Contains(w.Body.String(), "PRIVATE_BINARY") {
		t.Fatalf("unsafe scoped view:%s", w.Body.String())
	}
	envelope := fmt.Sprintf(`{"operation":"checkpoint","generation":0,"request":{"id":"pin","schema":3,"page_id":%q,"epoch":"opaque","ifRev":0}}`, created.PageID)
	w = httptest.NewRecorder()
	f.pages.MutateDocumentDraftAgent(w, documentDraftRequest(f, token, "POST", envelope))
	wantCode(t, w, 200, "duplicate pin evidence")
	var receipt DocumentMutationResponse
	if json.Unmarshal(w.Body.Bytes(), &receipt) != nil || receipt.Checkpoint != "pin" {
		t.Fatal("duplicate checkpoint lost confirmed marker")
	}
	f.pages.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
		return []byte(`{"error":"original conflict"}`), 409, nil
	}
	w = httptest.NewRecorder()
	f.pages.MutateDocumentDraftAgent(w, documentDraftRequest(f, token, "POST", envelope))
	wantCode(t, w, 409, "backend conflict status")
	f.pages.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
		return nil, 0, errors.New("synthetic unknown send")
	}
	w = httptest.NewRecorder()
	f.pages.MutateDocumentDraftAgent(w, documentDraftRequest(f, token, "POST", envelope))
	wantCode(t, w, 503, "unknown sent outcome")
	for _, body := range []string{strings.Replace(envelope, `"generation":0`, `"generation":0,"generation":1`, 1), strings.Replace(envelope, `"request":{`, `"request":{"kind":"human",`, 1), strings.Replace(envelope, `"operation":"checkpoint"`, `"operation":"restore"`, 1), strings.Replace(envelope, `"page_id":`+fmt.Sprintf("%q", created.PageID), `"page_id":"other-page"`, 1)} {
		w = httptest.NewRecorder()
		f.pages.MutateDocumentDraftAgent(w, documentDraftRequest(f, token, "POST", body))
		if w.Code != 400 && w.Code != 409 {
			t.Fatalf("unsafe envelope admitted:%d", w.Code)
		}
	}
}
func TestDocumentDraftPinFailureDoesNotPretendCommitRolledBack(t *testing.T) {
	f, h, token, created := newDocumentDraftFixture(t, true)
	initializeDocumentDraftProjection(t, f, created.PageID)
	lease := documentDraftLease(t, f, h, token, created.PageID)
	wantCode(t, callDocumentDraftAuthority(t, f, h, token, created.PageID, "commit", documentDraftPayload(t, "committed", "initial", "draft one", 0, lease)), 200, "durable edit")
	mustExec(t, f.rig.db, `CREATE TRIGGER reject_draft_pin BEFORE INSERT ON document_checkpoints BEGIN SELECT RAISE(ABORT,'synthetic pin fault'); END`)
	pin := map[string]any{"id": "committed-checkpoint", "fingerprint": "pin", "generation": 0, "ifRev": 1, "epoch": lease.Epoch, "leaseToken": lease.Writer, "leaseOwner": lease.Owner, "snapshot": []byte("synthetic candidate draft one")}
	wantCode(t, callDocumentDraftAuthority(t, f, h, token, created.PageID, "checkpoint", pin), 500, "pin failure")
	var head, cursor, receipts, pins int
	if e := f.rig.db.QueryRow(`SELECT head_rev,(SELECT draft_rev FROM document_creation_consents WHERE result_page_id=documents.page_id),(SELECT COUNT(*) FROM document_receipts WHERE id='committed'),(SELECT COUNT(*) FROM document_checkpoints WHERE page_id=documents.page_id) FROM documents WHERE page_id=?`, created.PageID).Scan(&head, &cursor, &receipts, &pins); e != nil || head != 1 || cursor != 1 || receipts != 1 || pins != 0 {
		t.Fatalf("committed point misreported:%d %d %d %d %v", head, cursor, receipts, pins, e)
	}
}
