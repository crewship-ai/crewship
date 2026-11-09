//go:build linux && (docs_foundation || conformance)

package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/sidecar"
)

// TestDocumentCanvasSchemaSixRealEngineCLI (#5149, opposition C06 note and the
// integrated-flow acceptance): a new Doc at the creation schema (5) with marks,
// a date and a comment is upgraded to Canvas (schema 6) by its owner through
// the public route and the CLI binary. The upgrade is explicit, human-only,
// idempotent and keeps content, hashes, comments and versions; an older
// client cannot write afterwards. Blocks are placed on the Canvas; an agent
// proposal on a placed block goes through the real draft route, sidecar code
// and Node engine at schema 6; accepting keeps formatting and geometry,
// rejecting writes nothing; a restore of pre-Canvas history keeps schema 6.
//
//	CREWSHIP_DOCUMENT_CREATE=1 go test -tags docs_foundation ./internal/api \
//	  -run '^TestDocumentCanvasSchemaSixRealEngineCLI$' -count=1
func TestDocumentCanvasSchemaSixRealEngineCLI(t *testing.T) {
	if os.Getenv("CREWSHIP_DOCUMENT_CREATE") != "1" {
		t.Fatal("requires explicit owned Node24/frozen Docs installation opt-in")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	version, err := exec.Command("node", "--version").Output()
	if err != nil || !strings.HasPrefix(string(version), "v24.") {
		t.Fatalf("Node24 required: %s %v", version, err)
	}
	f, creation := documentCreationAdmissionFixture(t, true)
	mustExec(t, f.rig.db, `UPDATE workspaces SET slug='canvas-six' WHERE id=?`, f.a.WorkspaceID)
	if err := bindCreation(f, creation); err != nil {
		t.Fatal(err)
	}
	const master = "synthetic-canvas-six-integration-master"
	dir, err := os.MkdirTemp("", "doc-canvas6-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	secret := filepath.Join(dir, "secret")
	const serviceSecret = "synthetic-canvas-six-service-secret"
	if err := os.WriteFile(secret, []byte(serviceSecret), 0600); err != nil {
		t.Fatal(err)
	}
	authoritySocket, nodeSocket := filepath.Join(dir, "a.sock"), filepath.Join(dir, "n.sock")
	writer := journal.NewWriter(f.rig.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	router, err := NewRouter(f.rig.db, "synthetic-canvas-six-jwt-secret-for-tests-only", newTestLogger(), WithInternalToken(master), WithJournal(writer), WithStoragePath(filepath.Join(dir, "storage")), WithDocumentAuthority(authoritySocket, secret), WithDocumentService(nodeSocket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	node := exec.Command("node", filepath.Join(root, "tools/docs-service/service.mjs"))
	node.Env = append(os.Environ(), "CREWSHIP_DOCS_SOCKET="+nodeSocket, "CREWSHIP_DOCS_AUTHORITY_SOCKET="+authoritySocket, "CREWSHIP_DOCS_SERVICE_TOKEN_FILE="+secret)
	node.Stderr = os.Stderr
	if err := node.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Process.Kill(); _ = node.Wait() })
	documentsWaitSocket(t, nodeSocket)
	documentsWaitReady(t, nodeSocket, serviceSecret)
	host := httptest.NewServer(router)
	t.Cleanup(host.Close)
	request := func(method, path, token string, body []byte) (int, []byte, error) {
		req, err := http.NewRequestWithContext(t.Context(), method, host.URL+path, bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Workspace-ID", f.a.WorkspaceID)
		req.Header.Set("Content-Type", "application/json")
		if strings.HasPrefix(token, "agtv2.") {
			req.Header.Set("X-Internal-Token", internaltoken.DeriveCrewToken(master, f.a.WorkspaceID, f.a.CrewID))
		}
		client := http.Client{Timeout: 20 * time.Second}
		res, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		return res.StatusCode, raw, err
	}

	const humanToken = "crewship_cli_canvas_six_acceptance"
	digest := sha256.Sum256([]byte(humanToken))
	mustExec(t, f.rig.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('canvas6-cli-token',?,'Canvas six test',?,'STANDARD','["*"]')`, f.a.HumanUserID, fmt.Sprintf("%x", digest))
	config := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: canvas-six\nformat: json\n", host.URL, humanToken)), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "crewship")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/crewship")
	build.Dir = root
	build.Env = nestedGoBuildEnv(t)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("CLI build: %s %v", output, err)
	}
	cli := func(input string, args ...string) ([]byte, error) {
		command := exec.CommandContext(t.Context(), binary, args...)
		command.Stdin = strings.NewReader(input)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "CREWSHIP_") {
				command.Env = append(command.Env, entry)
			}
		}
		command.Env = append(command.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		return command.CombinedOutput()
	}
	must := func(input string, args ...string) []byte {
		t.Helper()
		out, err := cli(input, args...)
		if err != nil {
			t.Fatalf("CLI %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}

	const slug = "canvas-six-brief"
	created := must("apiVersion: crewship/v1\nkind: Page\nmetadata:\n  name: Canvas six brief\n  slug: "+slug+"\nspec:\n  panels: []\n", "page", "create", "--allow-empty", "--file", "-")
	var page struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(created, &page) != nil || page.ID == "" {
		t.Fatalf("Page create: %s", created)
	}
	mark := func(kind string, attrs ...string) map[string]any {
		m := map[string]any{"type": kind}
		if len(attrs) == 2 {
			m["attrs"] = map[string]any{attrs[0]: attrs[1]}
		}
		return m
	}
	text := func(value string, marks ...map[string]any) map[string]any {
		node := map[string]any{"type": "text", "text": value}
		if len(marks) > 0 {
			list := []any{}
			for _, m := range marks {
				list = append(list, m)
			}
			node["marks"] = list
		}
		return node
	}
	paragraph := func(id string, parts ...map[string]any) map[string]any {
		content := []any{}
		for _, part := range parts {
			content = append(content, part)
		}
		return map[string]any{"type": "paragraph", "attrs": map[string]any{"id": id}, "content": content}
	}
	body := map[string]any{"type": "doc", "content": []any{
		paragraph("lead",
			text("Ship "), text("underlined", mark("underline")), text(" in "), text("red", mark("textColor", "color", "red")),
			text(" and "), text("marked", mark("bold"), mark("highlight", "color", "yellow")), text(" by "),
			map[string]any{"type": "date", "attrs": map[string]any{"value": "2026-11-20"}}, text(" for "),
			map[string]any{"type": "mention", "attrs": map[string]any{"userId": f.a.HumanUserID}}, text(" today.")),
		paragraph("other", text("Second paragraph.")),
		paragraph("tail", text("Outside the mandate.")),
	}}
	// New Docs are created at the explicit creation schema, never "latest".
	initialize, _ := json.Marshal(map[string]any{"id": "canvas6-init", "schema": documents.CreateSchema, "page_id": page.ID, "body": body})
	must(string(initialize), "doc", "initialize", slug, "--file", "-")
	type outlineBlock struct {
		ID   string `json:"id"`
		Hash string `json:"hash"`
		Text string `json:"text"`
	}
	type docState struct {
		Schema       int             `json:"schema"`
		Rev          int64           `json:"rev"`
		Generation   int64           `json:"generation"`
		Epoch        string          `json:"epoch"`
		Proposals    bool            `json:"proposals"`
		Capabilities []string        `json:"capabilities"`
		Content      json.RawMessage `json:"content"`
		Outline      []outlineBlock  `json:"outline"`
		Snapshot     string          `json:"snapshot"`
		Updates      []string        `json:"updates"`
	}
	readDoc := func() docState {
		t.Helper()
		var s docState
		raw := must("", "doc", "read", slug)
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("read: %v %s", err, raw)
		}
		return s
	}
	block := func(s docState, id string) string {
		t.Helper()
		var content struct {
			Content []json.RawMessage `json:"content"`
		}
		if json.Unmarshal(s.Content, &content) != nil {
			t.Fatalf("content: %s", s.Content)
		}
		for _, raw := range content.Content {
			var b struct {
				Attrs struct {
					ID string `json:"id"`
				} `json:"attrs"`
			}
			if json.Unmarshal(raw, &b) == nil && b.Attrs.ID == id {
				var compact bytes.Buffer
				if err := json.Compact(&compact, raw); err != nil {
					t.Fatal(err)
				}
				return compact.String()
			}
		}
		t.Fatalf("block %s missing: %s", id, s.Content)
		return ""
	}
	hashOf := func(s docState, id string) string {
		for _, b := range s.Outline {
			if b.ID == id {
				return b.Hash
			}
		}
		t.Fatalf("outline lacks %s", id)
		return ""
	}
	// surface runs the engine's own Yjs and surface model (testdata helper).
	surface := func(s docState, place map[string]map[string]int) map[string]json.RawMessage {
		t.Helper()
		input, _ := json.Marshal(map[string]any{"snapshot": s.Snapshot, "updates": s.Updates, "place": place})
		helper := exec.CommandContext(t.Context(), "node", filepath.Join(root, "internal/api/testdata/docs-canvas/surface.mjs"), root)
		helper.Stdin = bytes.NewReader(input)
		helper.Stderr = os.Stderr
		out, err := helper.Output()
		var result map[string]json.RawMessage
		if err != nil || json.Unmarshal(out, &result) != nil {
			t.Fatalf("surface helper: %v %s", err, out)
		}
		return result
	}

	before := readDoc()
	if before.Schema != documents.CreateSchema {
		t.Fatalf("created at schema %d", before.Schema)
	}
	comment, _ := json.Marshal(map[string]any{"id": "canvas6-comment", "schema": 5, "page_id": page.ID, "epoch": before.Epoch, "ifRev": before.Rev, "block": "other", "from": 0, "to": 6, "text": "Keep this short."})
	must(string(comment), "doc", "comment", slug, "--file", "-")
	commented := readDoc()
	var history struct {
		Checkpoints []documentCheckpointSummary `json:"checkpoints"`
	}
	if raw := must("", "doc", "checkpoints", slug); json.Unmarshal(raw, &history) != nil || len(history.Checkpoints) == 0 {
		t.Fatalf("checkpoints: %s", raw)
	}
	versionsBefore := len(history.Checkpoints)

	// Enabling Canvas is a person's decision: an agent run is refused before the engine.
	upgradeRequest := map[string]any{"id": "canvas6-enable", "schema": 5, "target_schema": 6, "page_id": page.ID, "epoch": commented.Epoch, "ifRev": commented.Rev}
	upgradeBody, _ := json.Marshal(upgradeRequest)
	agentToken := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(master, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	// An agent run credential is no session on the public route (401), bound or not; the engine refuses it with 403 too.
	if status, raw, _ := request(http.MethodPost, "/api/v1/pages/"+slug+"/document/upgrade", agentToken, upgradeBody); status != http.StatusUnauthorized {
		t.Fatalf("unbound agent upgrade: %d %s", status, raw)
	}
	if readDoc().Schema != 5 {
		t.Fatal("a refused upgrade changed the schema")
	}
	// People without Edit on this document are refused too: a workspace Viewer
	// with a read grant, a Member with only a Comment grant and a Restricted
	// member even with a write grant (the route is not on their allowlist).
	for _, person := range []struct{ id, role, mode, level string }{{"canvas6-viewer", "VIEWER", "trusted", "read"}, {"canvas6-commenter", "MEMBER", "trusted", "comment"}, {"canvas6-restricted", "MEMBER", "restricted", "write"}} {
		mustExec(t, f.rig.db, `INSERT INTO users(id,email) VALUES(?,?)`, person.id, person.id+"@example.test")
		mustExec(t, f.rig.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES(?,?,?,?,?)`, person.id+"-member", f.a.WorkspaceID, person.id, person.role, person.mode)
		mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,?,?)`, page.ID, person.id, person.level, f.a.HumanUserID)
		token := "crewship_cli_" + strings.ReplaceAll(person.id, "-", "_")
		sum := sha256.Sum256([]byte(token))
		mustExec(t, f.rig.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES(?,?,'Canvas six person',?,'STANDARD','["*"]')`, person.id+"-token", person.id, fmt.Sprintf("%x", sum))
		packet, _ := json.Marshal(map[string]any{"id": person.id + "-enable", "schema": 5, "target_schema": 6, "page_id": page.ID, "epoch": commented.Epoch, "ifRev": commented.Rev})
		// The token is real: Viewer and Commenter read the document; Restricted is stopped at its allowlist, never with 401.
		if person.mode != "restricted" {
			if status, raw, err := request(http.MethodGet, "/api/v1/pages/"+slug+"/document", token, nil); err != nil || status != http.StatusOK {
				t.Fatalf("%s cannot read the document it was granted: %d %s %v", person.id, status, raw, err)
			}
		}
		// Viewer and Commenter are refused (403); the restricted allowlist hides the route itself (404).
		want := http.StatusForbidden
		if person.mode == "restricted" {
			want = http.StatusNotFound
		}
		if status, raw, err := request(http.MethodPost, "/api/v1/pages/"+slug+"/document/upgrade", token, packet); err != nil || status != want {
			t.Fatalf("%s (%s %s, %s grant) upgrade: %d %s %v", person.id, person.mode, person.role, person.level, status, raw, err)
		}
		if readDoc().Schema != 5 {
			t.Fatalf("%s changed the schema", person.id)
		}
	}
	// A wrong target and a stale revision are refused; nothing changes.
	for name, change := range map[string]map[string]any{"target 5": {"id": "canvas6-wrong", "target_schema": 5}, "stale rev": {"id": "canvas6-stale", "ifRev": commented.Rev - 1}} {
		packet := map[string]any{}
		for k, v := range upgradeRequest {
			packet[k] = v
		}
		for k, v := range change {
			packet[k] = v
		}
		raw, _ := json.Marshal(packet)
		if out, err := cli(string(raw), "doc", "upgrade", slug, "--file", "-"); err == nil {
			t.Fatalf("%s upgrade accepted: %s", name, out)
		}
	}
	var receipt struct {
		Rev       int64 `json:"rev"`
		Schema    int   `json:"schema"`
		Duplicate bool  `json:"duplicate"`
	}
	if raw := must(string(upgradeBody), "doc", "upgrade", slug, "--file", "-"); json.Unmarshal(raw, &receipt) != nil || receipt.Schema != 6 || receipt.Rev != commented.Rev+1 {
		t.Fatalf("upgrade: %s", raw)
	}
	var retry struct {
		Rev       int64 `json:"rev"`
		Duplicate bool  `json:"duplicate"`
	}
	if raw := must(string(upgradeBody), "doc", "upgrade", slug, "--file", "-"); json.Unmarshal(raw, &retry) != nil || !retry.Duplicate || retry.Rev != receipt.Rev {
		t.Fatalf("exact upgrade retry: %s", raw)
	}
	six := readDoc()
	if six.Schema != 6 || six.Rev != receipt.Rev || !six.Proposals || !strings.Contains(strings.Join(six.Capabilities, ","), "propose") || strings.Contains(strings.Join(six.Capabilities, ","), "upgrade") {
		t.Fatalf("Canvas Doc: schema %d rev %d proposals %v capabilities %v", six.Schema, six.Rev, six.Proposals, six.Capabilities)
	}
	if !bytes.Equal(six.Content, commented.Content) || fmt.Sprint(six.Outline) != fmt.Sprint(commented.Outline) {
		t.Fatalf("enabling Canvas changed the Page:\n%s\n%s", commented.Content, six.Content)
	}
	if raw := must("", "doc", "comments", slug); !strings.Contains(string(raw), "Keep this short.") || !strings.Contains(string(raw), `"attached"`) {
		t.Fatalf("comment after upgrade: %s", raw)
	}
	if raw := must("", "doc", "checkpoints", slug); json.Unmarshal(raw, &history) != nil || len(history.Checkpoints) < versionsBefore {
		t.Fatalf("versions after upgrade: %s", raw)
	}
	// An editor still at schema 5 is refused and must reload; it never writes over the Canvas.
	old, _ := json.Marshal(map[string]any{"id": "canvas6-old-client", "schema": 5, "page_id": page.ID, "epoch": six.Epoch, "ifRev": six.Rev, "target": "tail", "ifHash": hashOf(six, "tail"), "text": "Old editor."})
	if out, err := cli(string(old), "doc", "patch", slug, "--file", "-"); err == nil {
		t.Fatalf("a schema-5 client wrote a Canvas Doc: %s", out)
	}

	// Place two blocks: geometry only, no block hash changes.
	geometry := map[string]map[string]int{"lead": {"x": 40, "y": 60, "w": 320, "h": 120, "z": 1}, "other": {"x": 480, "y": 60, "w": 240, "h": 96, "z": 2}}
	var placeUpdate string
	if err := json.Unmarshal(surface(six, geometry)["update"], &placeUpdate); err != nil || placeUpdate == "" {
		t.Fatal("no placement update")
	}
	place, _ := json.Marshal(map[string]any{"id": "canvas6-place", "schema": 6, "page_id": page.ID, "epoch": six.Epoch, "kind": "human", "update": placeUpdate})
	must(string(place), "doc", "patch", slug, "--file", "-")
	placed := readDoc()
	wantPlacements := `[{"id":"lead","x":40,"y":60,"w":320,"h":120,"z":1},{"id":"other","x":480,"y":60,"w":240,"h":96,"z":2}]`
	if got := string(surface(placed, nil)["placements"]); got != wantPlacements {
		t.Fatalf("placements after reload: %s", got)
	}
	if fmt.Sprint(placed.Outline) != fmt.Sprint(six.Outline) {
		t.Fatalf("placing blocks changed a block hash: %+v %+v", six.Outline, placed.Outline)
	}

	// An agent proposes on the placed blocks at schema 6.
	f.a.RunID, f.a.ChatID, f.a.MessageID = "canvas6-run", "canvas6-chat", "canvas6-message"
	mustExec(t, f.rig.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES(?,?,?,?,'private')`, f.a.ChatID, f.a.WorkspaceID, f.a.AgentID, f.a.HumanUserID)
	if f.receipt, err = chataudience.CaptureTrusted(t.Context(), f.rig.db, f.a.ChatID, f.a.HumanUserID, nil); err != nil || f.receipt == nil {
		t.Fatalf("private chat: %v", err)
	}
	mustExec(t, f.rig.db, `INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts,author_user_id) VALUES(?,?,?,'user','Tighten the first two paragraphs','2026-11-09T00:00:00Z',?)`, f.a.MessageID, f.a.ChatID, f.a.AgentID, f.a.HumanUserID)
	if err := f.capture(httptest.NewRequest("POST", "/", nil), f.a.MessageID, f.receipt); err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.rig.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('canvas6-start',?,?,?,'run.started','system','test',?)`, f.a.WorkspaceID, f.a.CrewID, f.a.AgentID, f.a.RunID)
	pages := router.Pages()
	must("", "page", "grant", slug, "--agent", "principal-agent", "--level", "write")
	stale := documents.IterationIntent{SubmissionID: "canvas6-0", PageID: page.ID, Schema: 5, Rev: placed.Rev, Generation: placed.Generation, Blocks: "lead,other"}
	if err := pages.persistDocumentIterationIntent(httptest.NewRequest("POST", "/", nil), f.receipt, f.a.MessageID, stale); err == nil {
		t.Fatal("a schema-5 mandate was admitted on a Canvas Doc")
	}
	intent := documents.IterationIntent{SubmissionID: "canvas6-1", PageID: page.ID, Schema: 6, Rev: placed.Rev, Generation: placed.Generation, Blocks: "lead,other"}
	if err := pages.persistDocumentIterationIntent(httptest.NewRequest("POST", "/", nil), f.receipt, f.a.MessageID, intent); err != nil {
		t.Fatalf("persist schema-6 propose mandate: %v", err)
	}
	if err := bindIterationFixture(t, f, intent); err != nil {
		t.Fatalf("bind schema-6 propose mandate: %v", err)
	}
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(master, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	// Even with a bound propose mandate an agent never upgrades a document.
	if status, raw, _ := request(http.MethodPost, "/api/v1/pages/"+slug+"/document/upgrade", token, upgradeBody); status != http.StatusUnauthorized {
		t.Fatalf("bound agent upgrade: %d %s", status, raw)
	}
	exchange := func(method string, body []byte) (int, []byte, error) {
		return request(method, "/api/v1/internal/documents/draft", token, body)
	}
	status, raw, err := exchange(http.MethodGet, nil)
	if err != nil || status != 200 {
		t.Fatalf("propose view: %d %s %v", status, raw, err)
	}
	var view sidecar.DocumentDraftView
	if err := json.Unmarshal(raw, &view); err != nil || view.Mode != "propose" || view.Schema != 6 {
		t.Fatalf("propose view: %s", raw)
	}
	prepare := func(target, text, id string) sidecar.DocumentProposalPacket {
		t.Helper()
		input, _ := json.Marshal(sidecar.DocumentDraftPrepare{View: view, Target: target, Text: text, MutationID: id})
		prepared, err := sidecar.PrepareDocumentDraftOrProposal(input)
		if err != nil {
			t.Fatalf("prepare %s: %v", id, err)
		}
		packet, ok := prepared.(sidecar.DocumentProposalPacket)
		if !ok || packet.Proposal.Schema != 6 {
			t.Fatalf("schema-6 propose view prepared %T %+v", prepared, prepared)
		}
		return packet
	}
	lead := prepare("lead", "Deliver underlined in red and marked by  for  today.", "canvas6-lead")
	if outcome, err := sidecar.ApplyDocumentProposal(lead, exchange); err != nil || outcome.Status != "proposed" {
		t.Fatalf("propose lead: %+v %v", outcome, err)
	}
	other := prepare("other", "Second paragraph, rejected.", "canvas6-other")
	if outcome, err := sidecar.ApplyDocumentProposal(other, exchange); err != nil || outcome.Status != "proposed" {
		t.Fatalf("propose other: %+v %v", outcome, err)
	}
	if after := readDoc(); after.Rev != placed.Rev || !bytes.Equal(after.Content, placed.Content) {
		t.Fatal("a proposal changed the document")
	}
	var list struct {
		Proposals []DocumentProposalWire `json:"proposals"`
	}
	if raw := must("", "doc", "proposals", slug); json.Unmarshal(raw, &list) != nil || len(list.Proposals) != 2 {
		t.Fatalf("proposals: %s", raw)
	}
	ids := map[string]string{}
	for _, p := range list.Proposals {
		ids[p.Target.BlockIDs[0]] = p.ID
	}
	var rejected DocumentProposalReceipt
	if raw := must("", "doc", "proposal-reject", slug, ids["other"], "--expected-rev", fmt.Sprint(placed.Rev), "--request-id", "canvas6-reject"); json.Unmarshal(raw, &rejected) != nil || rejected.Rev != placed.Rev {
		t.Fatalf("reject: %s", raw)
	}
	var decided DocumentProposalReceipt
	if raw := must("", "doc", "proposal-accept", slug, ids["lead"], "--expected-rev", fmt.Sprint(placed.Rev), "--request-id", "canvas6-accept"); json.Unmarshal(raw, &decided) != nil || decided.Rev != placed.Rev+1 {
		t.Fatalf("accept: %s", raw)
	}
	accepted := readDoc()
	if accepted.Schema != 6 || accepted.Rev != placed.Rev+1 {
		t.Fatalf("accepted state: schema %d rev %d", accepted.Schema, accepted.Rev)
	}
	acceptedLead := block(accepted, "lead")
	for _, want := range []string{`"text":"Deliver "`, `"type":"underline"`, `"type":"textColor"`, `"type":"highlight"`, `"type":"bold"`, `"value":"2026-11-20"`, `"userId":"` + f.a.HumanUserID + `"`} {
		if !strings.Contains(acceptedLead, want) {
			t.Fatalf("accepted lead lost %s: %s", want, acceptedLead)
		}
	}
	if block(accepted, "other") != block(placed, "other") || block(accepted, "tail") != block(placed, "tail") {
		t.Fatalf("a rejected or unrelated block changed: %s", accepted.Content)
	}
	if got := string(surface(accepted, nil)["placements"]); got != wantPlacements {
		t.Fatalf("accepting a proposal moved the Canvas: %s", got)
	}

	// Restoring the creation version (before Canvas) keeps Canvas enabled.
	if raw := must("", "doc", "checkpoints", slug); json.Unmarshal(raw, &history) != nil {
		t.Fatalf("checkpoints: %s", raw)
	}
	initial := ""
	for _, c := range history.Checkpoints {
		if c.Rev == 0 {
			initial = c.ID
		}
	}
	if initial == "" {
		t.Fatalf("no creation checkpoint: %+v", history.Checkpoints)
	}
	restore, _ := json.Marshal(map[string]any{"page_id": page.ID, "id": "canvas6-restore", "schema": 6, "epoch": accepted.Epoch, "ifRev": accepted.Rev, "checkpoint": initial})
	must(string(restore), "doc", "restore", slug, "--file", "-")
	restored := readDoc()
	if restored.Schema != 6 || restored.Rev != accepted.Rev+1 || !restored.Proposals {
		t.Fatalf("restored state: schema %d rev %d proposals %v", restored.Schema, restored.Rev, restored.Proposals)
	}
	if block(restored, "lead") != block(before, "lead") {
		t.Fatalf("restore did not bring back the original lead: %s", block(restored, "lead"))
	}
}
