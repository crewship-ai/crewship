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

// TestDocumentProposalSchemaFourRealEngineCLI (#5137): a human makes a
// schema-4 Doc with marks, a code block language, a quote and a task list
// through the CLI binary and gives the agent write on it; the agent, admitted
// by a schema-4 targeted message, proposes through the real draft route and
// the real Node engine; the human lists, reads and accepts the proposal with
// the CLI. Content and formatting round-trip; the mandate gates hold.
//
//	CREWSHIP_DOCUMENT_CREATE=1 go test -tags docs_foundation ./internal/api \
//	  -run '^TestDocumentProposalSchemaFourRealEngineCLI$' -count=1
func TestDocumentProposalSchemaFourRealEngineCLI(t *testing.T) {
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
	mustExec(t, f.rig.db, `UPDATE workspaces SET slug='proposal-acceptance' WHERE id=?`, f.a.WorkspaceID)
	if err := bindCreation(f, creation); err != nil {
		t.Fatal(err)
	}
	const master = "synthetic-proposal-integration-master"
	dir, err := os.MkdirTemp("", "doc-propose-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	secret := filepath.Join(dir, "secret")
	const serviceSecret = "synthetic-proposal-integration-service-secret"
	if err := os.WriteFile(secret, []byte(serviceSecret), 0600); err != nil {
		t.Fatal(err)
	}
	authoritySocket, nodeSocket := filepath.Join(dir, "a.sock"), filepath.Join(dir, "n.sock")
	writer := journal.NewWriter(f.rig.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	router, err := NewRouter(f.rig.db, "synthetic-proposal-jwt-secret-pad", newTestLogger(), WithInternalToken(master), WithJournal(writer), WithStoragePath(filepath.Join(dir, "storage")), WithDocumentAuthority(authoritySocket, secret), WithDocumentService(nodeSocket))
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

	// The human and the CLI binary.
	const humanToken = "crewship_cli_proposal_acceptance_test"
	digest := sha256.Sum256([]byte(humanToken))
	mustExec(t, f.rig.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('proposal-cli-token',?,'Proposal test',?,'STANDARD','["*"]')`, f.a.HumanUserID, fmt.Sprintf("%x", digest))
	config := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: proposal-acceptance\nformat: json\n", host.URL, humanToken)), 0600); err != nil {
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

	// A schema-4 Doc with every formatting the proposal must keep.
	const slug = "proposal-brief"
	created := must("apiVersion: crewship/v1\nkind: Page\nmetadata:\n  name: Proposal brief\n  slug: "+slug+"\nspec:\n  panels: []\n", "page", "create", "--allow-empty", "--file", "-")
	var page struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(created, &page) != nil || page.ID == "" {
		t.Fatalf("Page create: %s", created)
	}
	text := func(value string, marks ...string) map[string]any {
		node := map[string]any{"type": "text", "text": value}
		if len(marks) > 0 {
			list := []any{}
			for _, mark := range marks {
				list = append(list, map[string]any{"type": mark})
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
		paragraph("lead", text("Keep "), text("bold", "bold"), text(" and "), text("gone", "strike"), text(" words.")),
		map[string]any{"type": "codeBlock", "attrs": map[string]any{"id": "code", "language": "python"}, "content": []any{text(`print("hi")`)}},
		map[string]any{"type": "blockquote", "attrs": map[string]any{"id": "quote"}, "content": []any{paragraph("quote-p", text("Quoted", "italic"))}},
		map[string]any{"type": "taskList", "attrs": map[string]any{"id": "tasks"}, "content": []any{map[string]any{"type": "taskItem", "attrs": map[string]any{"id": "done", "checked": true}, "content": []any{paragraph("done-p", text("Ship it"))}}}},
	}}
	initialize, _ := json.Marshal(map[string]any{"id": "proposal-init", "schema": documents.ProposalSchema, "page_id": page.ID, "body": body})
	must(string(initialize), "doc", "initialize", slug, "--file", "-")
	type docState struct {
		Schema     int             `json:"schema"`
		Rev        int64           `json:"rev"`
		Generation int64           `json:"generation"`
		Proposals  bool            `json:"proposals"`
		Content    json.RawMessage `json:"content"`
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
	before := readDoc()
	if before.Schema != 4 || !before.Proposals {
		t.Fatalf("schema-4 Doc does not serve proposals: %+v", before)
	}

	// The schema-4 targeted message from the human's private chat to the agent.
	f.a.RunID, f.a.ChatID, f.a.MessageID = "proposal-run", "proposal-chat", "proposal-message"
	mustExec(t, f.rig.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES(?,?,?,?,'private')`, f.a.ChatID, f.a.WorkspaceID, f.a.AgentID, f.a.HumanUserID)
	if f.receipt, err = chataudience.CaptureTrusted(t.Context(), f.rig.db, f.a.ChatID, f.a.HumanUserID, nil); err != nil || f.receipt == nil {
		t.Fatalf("private chat: %v", err)
	}
	mustExec(t, f.rig.db, `INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts,author_user_id) VALUES(?,?,?,'user','Tighten the lead sentence','2026-11-08T00:00:00Z',?)`, f.a.MessageID, f.a.ChatID, f.a.AgentID, f.a.HumanUserID)
	if err := f.capture(httptest.NewRequest("POST", "/", nil), f.a.MessageID, f.receipt); err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.rig.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('proposal-start',?,?,?,'run.started','system','test',?)`, f.a.WorkspaceID, f.a.CrewID, f.a.AgentID, f.a.RunID)
	intent := documents.IterationIntent{SubmissionID: "propose-1", PageID: page.ID, Schema: documents.ProposalSchema, Rev: before.Rev, Generation: before.Generation}
	pages := router.Pages()
	// Gate: the Doc is shared and the agent holds no grant yet.
	if err := pages.persistDocumentIterationIntent(httptest.NewRequest("POST", "/", nil), f.receipt, f.a.MessageID, intent); err == nil {
		t.Fatal("agent without a write grant was mandated to propose")
	}
	if out, err := cli("", "page", "grant", slug, "--agent", "principal-agent", "--level", "read"); err != nil {
		t.Fatalf("read grant: %v %s", err, out)
	}
	if err := pages.persistDocumentIterationIntent(httptest.NewRequest("POST", "/", nil), f.receipt, f.a.MessageID, intent); err == nil {
		t.Fatal("agent read grant was mandated to propose")
	}
	must("", "page", "grant", slug, "--agent", "principal-agent", "--level", "write")
	if err := pages.persistDocumentIterationIntent(httptest.NewRequest("POST", "/", nil), f.receipt, f.a.MessageID, intent); err != nil {
		t.Fatalf("persist propose mandate: %v", err)
	}
	if err := bindIterationFixture(t, f, intent); err != nil {
		t.Fatalf("bind propose mandate: %v", err)
	}

	// The agent: real draft route, real engine, the sidecar's own packet code.
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(master, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	exchange := func(method string, body []byte) (int, []byte, error) {
		return request(method, "/api/v1/internal/documents/draft", token, body)
	}
	status, raw, err := exchange(http.MethodGet, nil)
	if err != nil || status != 200 {
		t.Fatalf("propose view: %d %s %v", status, raw, err)
	}
	var view sidecar.DocumentDraftView
	if err := json.Unmarshal(raw, &view); err != nil || view.Mode != "propose" || view.Schema != 4 {
		t.Fatalf("propose view: %s", raw)
	}
	targets := map[string]bool{}
	for _, target := range view.Targets {
		targets[target.ID] = true
	}
	if !targets["lead"] || targets["code"] {
		t.Fatalf("view targets: %+v", view.Targets)
	}
	revised := "Keep bold and gone words, now tighter."
	input, _ := json.Marshal(sidecar.DocumentDraftPrepare{View: view, Target: "lead", Text: revised, MutationID: "proposal-lead"})
	prepared, err := sidecar.PrepareDocumentDraftOrProposal(input)
	if err != nil {
		t.Fatal(err)
	}
	packet, ok := prepared.(sidecar.DocumentProposalPacket)
	if !ok {
		t.Fatalf("propose view prepared a direct packet: %T", prepared)
	}
	// A direct patch on a propose mandate is refused; nothing is written.
	patch, _ := json.Marshal(map[string]any{"operation": "patch", "generation": packet.Generation, "request": map[string]any{"id": "direct", "schema": 3, "page_id": page.ID, "epoch": view.Epoch, "ifRev": view.Rev, "target": "lead", "ifHash": packet.Proposal.IfHash, "text": revised}})
	if status, raw, _ := exchange(http.MethodPost, patch); status != 403 {
		t.Fatalf("direct patch on a propose mandate: %d %s", status, raw)
	}
	outcome, err := sidecar.ApplyDocumentProposal(packet, exchange)
	if err != nil || outcome.Status != "proposed" {
		t.Fatalf("propose: %+v %v", outcome, err)
	}
	if replay, err := sidecar.ApplyDocumentProposal(packet, exchange); err != nil || replay.Status != "proposed" {
		t.Fatalf("exact proposal retry: %+v %v", replay, err)
	}
	if after := readDoc(); after.Rev != before.Rev || !bytes.Equal(after.Content, before.Content) {
		t.Fatal("a proposal changed the document")
	}
	// The agent's token never reaches the human review routes.
	if status, _, _ := request(http.MethodGet, "/api/v1/pages/@"+page.ID+"/document/proposals", token, nil); status < 400 {
		t.Fatalf("agent listed proposals: %d", status)
	}

	// The human reviews and accepts with the CLI.
	var list struct {
		Proposals []DocumentProposalWire `json:"proposals"`
	}
	if raw := must("", "doc", "proposals", slug); json.Unmarshal(raw, &list) != nil || len(list.Proposals) != 1 || list.Proposals[0].Status != "open" || list.Proposals[0].Target.BlockIDs[0] != "lead" || list.Proposals[0].Text != revised {
		t.Fatalf("proposals: %s", raw)
	}
	id := list.Proposals[0].ID
	var detail DocumentProposalWire
	if raw := must("", "doc", "proposal", slug, id); json.Unmarshal(raw, &detail) != nil || len(detail.BaseContent) == 0 || len(detail.ProposedContent) == 0 {
		t.Fatalf("proposal detail: %s", raw)
	}
	if out, err := cli("", "doc", "proposal-accept", slug, id, "--expected-rev", fmt.Sprint(before.Rev+1), "--request-id", "accept-1"); err == nil {
		t.Fatalf("accept against a head the reviewer never saw: %s", out)
	}
	var decided DocumentProposalReceipt
	if raw := must("", "doc", "proposal-accept", slug, id, "--expected-rev", fmt.Sprint(before.Rev), "--request-id", "accept-1"); json.Unmarshal(raw, &decided) != nil || decided.Rev != before.Rev+1 || decided.Kind != "human" {
		t.Fatalf("accept: %s", raw)
	}
	var retried DocumentProposalReceipt
	if raw := must("", "doc", "proposal-accept", slug, id, "--expected-rev", fmt.Sprint(before.Rev), "--request-id", "accept-1"); json.Unmarshal(raw, &retried) != nil || !retried.Duplicate || retried.Rev != decided.Rev {
		t.Fatalf("exact decision retry: %s", raw)
	}
	accepted := readDoc()
	if accepted.Rev != before.Rev+1 {
		t.Fatalf("accept rev: %d", accepted.Rev)
	}
	var content struct {
		Content []map[string]any `json:"content"`
	}
	var original struct {
		Content []json.RawMessage `json:"content"`
	}
	if json.Unmarshal(accepted.Content, &content) != nil || json.Unmarshal(before.Content, &original) != nil || len(content.Content) != 4 {
		t.Fatalf("accepted content: %s", accepted.Content)
	}
	lead, _ := json.Marshal(content.Content[0])
	var proposedLead any
	var acceptedLead any
	_ = json.Unmarshal(detail.ProposedContent, &proposedLead)
	_ = json.Unmarshal(lead, &acceptedLead)
	if fmt.Sprint(proposedLead) != fmt.Sprint(acceptedLead) {
		t.Fatalf("accepted block is not the reviewed proposal:\n%s\n%s", lead, detail.ProposedContent)
	}
	var runs struct {
		Content []struct {
			Text  string `json:"text"`
			Marks []struct {
				Type string `json:"type"`
			} `json:"marks"`
		} `json:"content"`
	}
	marked := map[string]string{}
	if json.Unmarshal(lead, &runs) == nil {
		for _, run := range runs.Content {
			if len(run.Marks) == 1 {
				marked[run.Text] = run.Marks[0].Type
			}
		}
	}
	if marked["bold"] != "bold" || marked["gone"] != "strike" {
		t.Fatalf("lead lost formatting: %s", lead)
	}
	for i := 1; i < 4; i++ {
		rest, _ := json.Marshal(content.Content[i])
		var was, now any
		_ = json.Unmarshal(original.Content[i], &was)
		_ = json.Unmarshal(rest, &now)
		if fmt.Sprint(was) != fmt.Sprint(now) {
			t.Fatalf("block %d changed: %s", i, rest)
		}
	}
	if raw := must("", "doc", "proposals", slug); json.Unmarshal(raw, &list) != nil || len(list.Proposals) != 1 || list.Proposals[0].Status != "accepted" {
		t.Fatalf("accepted status: %s", raw)
	}

	// Revoking the agent's write grant ends the mandate for the next proposal.
	must("", "page", "revoke", slug, "--agent", "principal-agent")
	if status, raw, _ := exchange(http.MethodGet, nil); status != 403 && status != 409 {
		t.Fatalf("revoked grant still admits the run: %d %s", status, raw)
	}
}
