package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

const craftNativeProvenanceProposal = "ncp_00000000000000000000beef"

// craftNativeProvenanceFixture saves version 2 as the owner and records it as
// the owner's acceptance of an agent proposal, the row AcceptProposal commits.
func craftNativeProvenanceFixture(t *testing.T) (*pagesTransferRig, *craftNativeHTTP, *craftNativeDocumentsHTTP, context.Context, string, map[string]any) {
	t.Helper()
	f, h, d, owner, ownerToken, _, body := craftNativeDocumentsFixture(t)
	craftNativeEnableVersionHistory(t, f.db, true)
	if w := craftNativeDocumentCall(t, d, owner, ownerToken, "save", body); w.Code != 200 {
		t.Fatalf("owner save %d %s", w.Code, w.Body)
	}
	mustExec(t, f.db, `INSERT INTO craft_native_proposals(id,page_id,workspace_id,application_id,engine_pin,author_actor_id,request_id,base_version,summary,content_hash,content,state,decided_by,decided_at,result_version) VALUES(?,'native-page',?,'wordcraft',?,'agent:provenance-agent','run:provenance',1,'',?,?,'accepted',?,strftime('%Y-%m-%dT%H:%M:%fZ','now'),2)`,
		craftNativeProvenanceProposal, f.wsID, strings.Repeat("d", 64), fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(`{"v":2}`))), []byte(`{"v":2}`), "user:"+f.userID)
	return f, h, d, owner, ownerToken, body
}

type craftNativeProvenanceBody struct {
	Versions []craftNativeVersionResponse `json:"versions"`
}

func craftNativeProvenanceOf(t *testing.T, w *httptest.ResponseRecorder) map[string]*craftNativeVersionProposalResponse {
	t.Helper()
	var body craftNativeProvenanceBody
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatalf("history %d %s", w.Code, w.Body)
	}
	out := map[string]*craftNativeVersionProposalResponse{}
	for _, v := range body.Versions {
		out[v.Version] = v.Proposal
	}
	return out
}

// #5599 review (a): an admission issued while the editor could read loses the
// provenance with its read rights. Over HTTP the history is refused without
// any proposal; and the provenance read itself, reached with a store bound
// before the revocation, re-checks live authority and releases nothing.
func TestCraftNativeProposalProvenanceRefusedAfterReadRevoked(t *testing.T) {
	f, h, d, _, _, _ := craftNativeProvenanceFixture(t)
	editor := craftNativeVersionsUser(t, f, "native-provenance-editor", "Editor Eva", true, "write")
	token, _ := craftNativeIssued(t, h, editor)
	if p := craftNativeProvenanceOf(t, craftNativeVersionsCall(d, editor, token)); p["2"] == nil || p["2"].ID != craftNativeProvenanceProposal || p["2"].Author.Kind != "agent" {
		t.Fatalf("editor before revoke: %+v", p)
	}
	r := httptest.NewRequest("GET", "/api/v1/pages/native-page/craft/document/versions", nil).WithContext(editor)
	r.SetPathValue("slug", "native-page")
	r.Header.Set("X-Craft-Session", token)
	bound, entry, store, _, err := d.bind(r)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.db, `DELETE FROM page_grants WHERE page_id='native-page' AND subject_id='native-provenance-editor'`)
	if origins, err := store.AcceptedProposalOrigins(bound.Context(), entry.scope, []int64{1, 2}); err == nil || len(origins) != 0 {
		t.Fatalf("revoked reader got provenance: %v %v", origins, err)
	}
	for name, w := range map[string]*httptest.ResponseRecorder{"list": craftNativeVersionsCall(d, editor, token), "version": craftNativeVersionReadCall(d, editor, token, "2")} {
		if w.Code == 200 || strings.Contains(w.Body.String(), craftNativeProvenanceProposal) || strings.Contains(w.Body.String(), "provenance-agent") {
			t.Fatalf("%s after revoke %d %s", name, w.Code, w.Body)
		}
	}
}

// #5599 review (b): an administrator's history read shows the provenance and
// is audited exactly once per read, like any other history read.
func TestCraftNativeProposalProvenanceAdminReadAuditedOnce(t *testing.T) {
	f, h, d, _, _, _ := craftNativeProvenanceFixture(t)
	mustExec(t, f.db, `UPDATE pages SET private_audience=0 WHERE id='native-page'`)
	admin := craftNativeVersionsMember(t, f, "native-provenance-admin", "Admin Adam", "ADMIN", "")
	token, _ := craftNativeIssued(t, h, admin)
	audits := func() int {
		var n int
		if err := f.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action IN ('artifact.private_owner_accessed','artifact.workspace_admin_accessed') AND user_id='native-provenance-admin' AND entity_id='native-page'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	p := craftNativeProvenanceOf(t, craftNativeVersionsCall(d, admin, token))
	if p["2"] == nil || p["2"].ID != craftNativeProvenanceProposal || p["2"].Author.ActorID != "agent:provenance-agent" || p["1"] != nil {
		t.Fatalf("admin history provenance: %+v", p)
	}
	if n := audits(); n != 1 {
		t.Fatalf("admin history read audited %d times, want 1", n)
	}
	one := craftNativeVersionReadCall(d, admin, token, "2")
	var single craftNativeVersionDocumentResponse
	if one.Code != 200 || json.Unmarshal(one.Body.Bytes(), &single) != nil || single.Proposal == nil || single.Proposal.ID != craftNativeProvenanceProposal {
		t.Fatalf("admin version read %d %s", one.Code, one.Body)
	}
	if n := audits(); n != 2 {
		t.Fatalf("admin version read audited %d times in total, want 2", n)
	}
}

// #5599 review (c): the decider's own later plain save is the decider's
// version only; it never inherits the accepted proposal's agent.
func TestCraftNativeProposalProvenanceNotOnDecidersLaterSave(t *testing.T) {
	_, _, d, owner, ownerToken, body := craftNativeProvenanceFixture(t)
	raw := []byte(`{"v":3}`)
	body["baseVersion"], body["bytes"], body["contentHash"], body["editorRevision"] = "2", raw, fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), "10:47:4"
	if w := craftNativeDocumentCall(t, d, owner, ownerToken, "save", body); w.Code != 200 {
		t.Fatalf("owner save v3 %d %s", w.Code, w.Body)
	}
	p := craftNativeProvenanceOf(t, craftNativeVersionsCall(d, owner, ownerToken))
	if len(p) != 3 || p["3"] != nil || p["1"] != nil || p["2"] == nil || p["2"].ID != craftNativeProvenanceProposal {
		t.Fatalf("only version 2 came from the proposal: %+v", p)
	}
	var single craftNativeVersionDocumentResponse
	if w := craftNativeVersionReadCall(d, owner, ownerToken, "3"); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &single) != nil || single.Proposal != nil {
		t.Fatalf("version 3 read claims a proposal: %d %s", w.Code, w.Body)
	}
}
