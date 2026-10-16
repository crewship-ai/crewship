//go:build linux && (docs_foundation || conformance)

package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
)

// Uses the foundation suite's actual separate Go authority and Node engine;
// no computation or durable-response mocks participate in these assertions.
func exerciseDocumentRichVersions(t *testing.T, h *PageHandler, workspace, user, human, agent, node, root, sourcePage string, call func(string, string, string, map[string]any, int) map[string]any) (string, map[string]any) {
	t.Helper()
	const page = "rich-legacy-page"
	wmSeedPage(t, h.db, workspace, page, "rich-legacy", "Legacy document", user, "")
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'agent','artifact-agent','write',?,strftime('%Y-%m-%dT%H:%M:%SZ','now'))`, page, user)
	script := `import {create,snapshot,outline} from './tools/docs-service/schema.mjs';
const doc=create({type:'doc',content:[{type:'paragraph',attrs:{id:'legacy-intro'},content:[{type:'text',text:'Legacy text'}]}]},1);
console.log(JSON.stringify({snapshot:snapshot(doc),blocks:(await outline(doc,1)).map(({id,hash})=>({id,hash}))}));doc.destroy();`
	command := exec.Command(node, "--input-type=module", "-e", script)
	command.Dir = root
	raw, err := command.Output()
	if err != nil {
		t.Fatalf("legacy binary fixture: %v", err)
	}
	var fixture struct {
		Snapshot string          `json:"snapshot"`
		Blocks   json.RawMessage `json:"blocks"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	binary, err := base64.StdEncoding.DecodeString(fixture.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	authorize := func(ctx context.Context, tx *sql.Tx) (string, error) {
		var owner string
		err := tx.QueryRowContext(ctx, `SELECT owner_user_id FROM pages WHERE id=? AND workspace_id=?`, page, workspace).Scan(&owner)
		return owner, err
	}
	if _, err = documents.New(h.db, documents.Options{}).InitRequest(t.Context(), documents.Key{WorkspaceID: workspace, PageID: page}, "legacy-seed", "synthetic-schema1-binary", 1, binary, fixture.Blocks, authorize); err != nil {
		t.Fatal(err)
	}
	read := func() map[string]any { return call(page, "read", human, map[string]any{}, 200) }
	state := read()
	if state["schema"] != float64(1) {
		t.Fatalf("legacy schema lost: %v", state)
	}
	call(page, "comment", human, map[string]any{"id": "legacy-comment", "schema": 1, "epoch": state["epoch"], "ifRev": state["rev"], "block": "legacy-intro", "from": 0, "to": 6, "text": "Legacy comment"}, 200)
	call(page, "checkpoint", human, map[string]any{"id": "legacy-one", "schema": 1, "epoch": state["epoch"], "ifRev": state["rev"]}, 200)
	block := state["outline"].([]any)[0].(map[string]any)
	// A legacy schema2 client can still edit stored schema1, as before this cut.
	call(page, "patch", human, map[string]any{"id": "legacy-compatible-edit", "schema": 2, "epoch": state["epoch"], "ifRev": state["rev"], "target": "legacy-intro", "ifHash": block["hash"], "text": "Legacy text edited"}, 200)
	state = read()
	if state["schema"] != float64(2) {
		t.Fatal("legacy edit compatibility changed")
	}
	restore := map[string]any{"id": "legacy-return", "schema": 2, "epoch": state["epoch"], "ifRev": state["rev"], "checkpoint": "legacy-one"}
	call(page, "restore", human, restore, 200)
	state = read()
	if state["schema"] != float64(1) {
		t.Fatal("legacy checkpoint silently upgraded")
	}
	upgrade := map[string]any{"id": "actual-rich-upgrade", "schema": 1, "target_schema": 3, "page_id": page, "epoch": state["epoch"], "ifRev": state["rev"]}
	assertComment := func() {
		t.Helper()
		items := call(page, "comments", human, map[string]any{}, 200)["comments"].([]any)
		if len(items) != 1 {
			t.Fatalf("legacy comment missing: %v", items)
		}
		comment := items[0].(map[string]any)
		if comment["status"] != "attached" || comment["quote"] != "Legacy" || comment["from"] != float64(0) || comment["to"] != float64(6) {
			t.Fatalf("legacy character anchor changed: %v", comment)
		}
	}
	original := state["snapshot"]
	receipt := call(page, "upgrade", human, upgrade, 200)
	state = read()
	if state["schema"] != float64(3) || state["snapshot"] != original || state["rev"] != receipt["rev"] {
		t.Fatal("upgrade changed original binary or schema/revision")
	}
	assertComment()
	if call(page, "upgrade", human, upgrade, 200)["duplicate"] != true {
		t.Fatal("upgrade retry not durable")
	}
	// Generate the actual browser-equivalent incremental Yjs mark/code update.
	script = `import * as Y from './tools/docs-service/node_modules/yjs/dist/yjs.mjs';import {load,find,decode,encode} from './tools/docs-service/schema.mjs';
const state=JSON.parse(process.argv[1]),doc=load(state.snapshot);for(const u of state.updates||[])Y.applyUpdate(doc,decode(typeof u==='string'?u:u.update));const vector=Y.encodeStateVector(doc);
find(doc,'legacy-intro').toArray()[0].format(0,6,{link:{href:'https://example.invalid/'}});
const code=new Y.XmlElement('codeBlock');code.setAttribute('id','rich-code');const text=new Y.XmlText();text.insert(0,'const literal = "<script>";');code.insert(0,[text]);doc.getXmlFragment('default').insert(1,[code]);
console.log(JSON.stringify({update:encode(Y.encodeStateAsUpdate(doc,vector))}));doc.destroy();`
	command = exec.Command(node, "--input-type=module", "-e", script, string(mustDocumentsJSON(t, state)))
	command.Dir = filepath.Clean(root)
	raw, err = command.Output()
	if err != nil {
		t.Fatalf("rich binary update fixture: %v", err)
	}
	var patch map[string]any
	if err = json.Unmarshal(raw, &patch); err != nil {
		t.Fatal(err)
	}
	patch["id"] = "actual-rich-human"
	patch["schema"] = 3
	patch["page_id"] = page
	patch["epoch"] = state["epoch"]
	patch["kind"] = "human"
	call(page, "patch", human, patch, 200)
	state = read()
	content := state["content"].(map[string]any)["content"].([]any)
	if len(content) != 2 || content[1].(map[string]any)["type"] != "codeBlock" {
		t.Fatal("code block not durable")
	}
	text := content[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if text["marks"].([]any)[0].(map[string]any)["type"] != "link" {
		t.Fatal("link not durable")
	}
	// The existing live generic run can use only a qualified semantic edit;
	// the private Go boundary authenticates the entire before/after tree.
	execOrFatal(t, h.db, `INSERT INTO page_artifact_run_delegations(page_id,run_id,granted_by_user_id,expires_at,granted_at) SELECT ?,run_id,granted_by_user_id,expires_at,granted_at FROM page_artifact_run_delegations WHERE page_id=? AND run_id='artifact-run'`, page, sourcePage)
	readByAgent := call(page, "read", agent, map[string]any{}, 200)
	block = readByAgent["outline"].([]any)[0].(map[string]any)
	call(page, "patch", agent, map[string]any{"id": "actual-rich-semantic", "schema": 3, "page_id": page, "epoch": readByAgent["epoch"], "ifRev": readByAgent["rev"], "target": "legacy-intro", "ifHash": block["hash"], "text": "Legacy changed"}, 200)
	state = read()
	content = state["content"].(map[string]any)["content"].([]any)
	assertComment()
	for index, scenario := range []string{"success", "human change"} {
		base := read()
		target := base["outline"].([]any)[0].(map[string]any)
		original := target["text"].(string)
		run := fmt.Sprintf("rich-selected-run-%d", index)
		execOrFatal(t, h.db, `INSERT INTO artifact_run_principals(run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at) SELECT ?,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at FROM artifact_run_principals WHERE run_id='artifact-run'`, run)
		execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES(?,?,'artifact-crew','artifact-agent','run.started','system','fixture',?)`, run+"-started", workspace, run)
		token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, "artifact-crew"), workspace, "artifact-agent", run)
		input := map[string]any{"page_id": page, "chat_id": "artifact-chat", "instruction": "Revise only selected text", "base_rev": base["rev"], "submission_id": run, "targets": []any{map[string]any{"id": "legacy-intro", "hash": target["hash"], "from": 7, "to": len(original), "quote": original[7:]}}}
		req := pagesGrantRequest(t, "POST", "/", workspace, user, "OWNER", string(mustDocumentsJSON(t, input)))
		req.SetPathValue("slug", "rich-legacy")
		req.Header.Set("Authorization", "Bearer "+human)
		UserFromContext(req.Context()).SessionID = "docs-session"
		queued := httptest.NewRecorder()
		h.CreateDocumentRequest(queued, req)
		if queued.Code != 202 {
			t.Fatalf("rich selected admission: %d %s", queued.Code, queued.Body.String())
		}
		var result DocumentRequestCreateResponse
		if err := json.Unmarshal(queued.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		execOrFatal(t, h.db, `UPDATE document_requests SET status='running' WHERE id=?`, result.ID)
		trusted, err := chataudience.CaptureTrusted(t.Context(), h.db, "artifact-chat", user, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = bindDocumentRequestRun(httptest.NewRequest("POST", "/", nil), h.db, result.ID, run, trusted); err != nil {
			t.Fatal(err)
		}
		current := call(page, "read", token, map[string]any{}, 200)
		selectedPatch := map[string]any{"id": "patch-" + run, "schema": 3, "page_id": page, "epoch": current["epoch"], "ifRev": base["rev"], "target": "legacy-intro", "ifHash": target["hash"], "text": "Legacy selected"}
		if scenario == "success" {
			ack := call(page, "patch", token, selectedPatch, 200)
			next := call(page, "read", token, map[string]any{}, 200)
			call(page, "checkpoint", token, map[string]any{"id": result.ID + "-checkpoint", "schema": 3, "page_id": page, "epoch": next["epoch"], "ifRev": next["rev"]}, 200)
			if call(page, "patch", token, selectedPatch, 200)["duplicate"] != true || read()["rev"] != ack["rev"] {
				t.Fatal("selected rich exact retry duplicated revision")
			}
		} else {
			call(page, "patch", human, map[string]any{"id": "rich-intervening-human", "schema": 3, "page_id": page, "epoch": current["epoch"], "ifRev": base["rev"], "target": "legacy-intro", "ifHash": target["hash"], "text": "Legacy human"}, 200)
			fresh := call(page, "read", token, map[string]any{}, 200)
			selectedPatch["epoch"] = fresh["epoch"]
			selectedPatch["ifRev"] = fresh["rev"]
			selectedPatch["ifHash"] = fresh["outline"].([]any)[0].(map[string]any)["hash"]
			call(page, "patch", token, selectedPatch, 409)
			if read()["rev"] != fresh["rev"] {
				t.Fatal("fresh conditions bypassed original rich request")
			}
			var receipts int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM document_receipts WHERE page_id=? AND id=?`, page, selectedPatch["id"]).Scan(&receipts); err != nil || receipts != 0 {
				t.Fatalf("denied rich receipt=%d %v", receipts, err)
			}
		}
		assertComment()
	}
	state = read()
	content = state["content"].(map[string]any)["content"].([]any)
	call(page, "checkpoint", human, map[string]any{"id": "rich-three", "schema": 3, "page_id": page, "epoch": state["epoch"], "ifRev": state["rev"]}, 200)
	call(page, "initialize", agent, map[string]any{"id": "agent-rich-reinit", "schema": 3, "page_id": page, "body": state["content"]}, 403)
	call(page, "restore", human, map[string]any{"id": "rich-to-legacy", "schema": 3, "page_id": page, "epoch": state["epoch"], "ifRev": state["rev"], "checkpoint": "legacy-one"}, 200)
	state = read()
	if state["schema"] != float64(1) {
		t.Fatal("mixed restore ignored source version")
	}
	pinned := call(page, "checkpoint-read", human, map[string]any{"checkpoint": "rich-three"}, 200)
	if pinned["schema"] != float64(3) || string(mustDocumentsJSON(t, pinned["content"])) != string(mustDocumentsJSON(t, map[string]any{"type": "doc", "content": content})) {
		t.Fatal("rich checkpoint lost actual marked/code content")
	}
	call(page, "restore", human, map[string]any{"id": "legacy-to-rich", "schema": 1, "epoch": state["epoch"], "ifRev": state["rev"], "checkpoint": "rich-three"}, 200)
	if read()["schema"] != float64(3) {
		t.Fatal("mixed restore did not retain rich source version")
	}
	assertComment()
	t.Log("actual schema1 read/edit/comment/checkpoint; schema1→3 immutable upgrade/retry; marked/code schema3 history and mixed restore PASS")
	return page, upgrade
}
