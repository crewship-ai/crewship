package api

import (
	"encoding/json"
	"testing"
)

// Reuse the real departure/transfer routes: document bytes must not become
// ownerless when a member leaves or deletes their account.
func TestDocumentCustodyRequiresExplicitSuccessor(t *testing.T) {
	f := custodyRouterFixture(t, "artifact")
	mustExec(t, f.db, `DELETE FROM page_artifact_revisions WHERE page_id='private-output'`)
	mustExec(t, f.db, `DELETE FROM page_artifacts WHERE page_id='private-output'`)
	mustExec(t, f.db, `DELETE FROM pages WHERE id='regular-page'`)
	mustExec(t, f.db, `INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES('ws-old','private-output',1,X'00ff','[]')`)
	var memberID string
	if err := f.db.QueryRow(`SELECT id FROM workspace_members WHERE workspace_id='ws-old' AND user_id='departing'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	departurePath := "/api/v1/workspaces/ws-old/members/" + memberID
	before := custodySnapshot(t, f.db, "documents", "pages", "workspace_members", "page_grants")
	response := f.do(f.wsAdmin, "DELETE", departurePath, "")
	wantCode(t, response, 409, "document departure conflict")
	var conflict struct {
		Pages []pageOwnershipSummary `json:"pages"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &conflict); err != nil || len(conflict.Pages) != 1 || conflict.Pages[0].Type != "document" {
		t.Fatalf("document custody conflict %s %v", response.Body, err)
	}
	if after := custodySnapshot(t, f.db, "documents", "pages", "workspace_members", "page_grants"); after != before {
		t.Fatal("blocked departure changed confirmed document or authority")
	}
	transfer := f.do(f.wsAdmin, "POST", "/api/v1/workspaces/ws-old/page-ownership/private-output", `{"expected_owner_user_id":"departing","successor_user_id":"reader"}`)
	wantCode(t, transfer, 200, "explicit document owner transfer")
	wantCode(t, f.do(f.wsAdmin, "DELETE", departurePath, ""), 200, "departure after document handoff")
	var owner string
	var snapshot []byte
	if err := f.db.QueryRow(`SELECT p.owner_user_id,d.snapshot FROM documents d JOIN pages p ON p.id=d.page_id WHERE d.page_id='private-output'`).Scan(&owner, &snapshot); err != nil || owner != "reader" || len(snapshot) != 2 || snapshot[1] != 255 {
		t.Fatalf("document handoff lost content or owner: %q %v %v", owner, snapshot, err)
	}
}
