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

// TestDocumentProposalSchemaFiveRealEngineCLI (#5233, opposition O01): a new
// Doc at the creation schema (5) carries underline, a palette text colour, a
// highlight, a date and a mention. A bound agent proposal goes through the
// real draft route, sidecar packet code and Node engine; a person accepts one
// and rejects another after a concurrent human change, with the CLI binary.
// Formatting and atoms survive, a target outside the mandate and an edit
// beside an atom are refused, a schema-4 client cannot write, and the history
// restore keeps schema 5 and its content.
//
//	CREWSHIP_DOCUMENT_CREATE=1 go test -tags docs_foundation ./internal/api \
//	  -run '^TestDocumentProposalSchemaFiveRealEngineCLI$' -count=1
func TestDocumentProposalSchemaFiveRealEngineCLI(t *testing.T) {
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
	mustExec(t, f.rig.db, `UPDATE workspaces SET slug='proposal-five' WHERE id=?`, f.a.WorkspaceID)
	if err := bindCreation(f, creation); err != nil {
		t.Fatal(err)
	}
	const master = "synthetic-proposal-five-integration-master"
	dir, err := os.MkdirTemp("", "doc-propose5-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	secret := filepath.Join(dir, "secret")
	const serviceSecret = "synthetic-proposal-five-service-secret"
	if err := os.WriteFile(secret, []byte(serviceSecret), 0600); err != nil {
		t.Fatal(err)
	}
	authoritySocket, nodeSocket := filepath.Join(dir, "a.sock"), filepath.Join(dir, "n.sock")
	writer := journal.NewWriter(f.rig.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	router, err := NewRouter(f.rig.db, "synthetic-proposal-five-jwt-secret", newTestLogger(), WithInternalToken(master), WithJournal(writer), WithStoragePath(filepath.Join(dir, "storage")), WithDocumentAuthority(authoritySocket, secret), WithDocumentService(nodeSocket))
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

	const humanToken = "crewship_cli_proposal_five_acceptance"
	digest := sha256.Sum256([]byte(humanToken))
	mustExec(t, f.rig.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('proposal5-cli-token',?,'Proposal five test',?,'STANDARD','["*"]')`, f.a.HumanUserID, fmt.Sprintf("%x", digest))
	config := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: proposal-five\nformat: json\n", host.URL, humanToken)), 0600); err != nil {
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

	const slug = "proposal-five-brief"
	created := must("apiVersion: crewship/v1\nkind: Page\nmetadata:\n  name: Proposal five brief\n  slug: "+slug+"\nspec:\n  panels: []\n", "page", "create", "--allow-empty", "--file", "-")
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
	initialize, _ := json.Marshal(map[string]any{"id": "proposal5-init", "schema": documents.CreateSchema, "page_id": page.ID, "body": body})
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
	block := func(s docState, id string) json.RawMessage {
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
				return compact.Bytes()
			}
		}
		t.Fatalf("block %s missing: %s", id, s.Content)
		return nil
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
	before := readDoc()
	if before.Schema != 5 || !before.Proposals || !strings.Contains(strings.Join(before.Capabilities, ","), "propose") {
		t.Fatalf("schema-5 Doc does not serve proposals: schema=%d proposals=%v capabilities=%v", before.Schema, before.Proposals, before.Capabilities)
	}
	originalLead := block(before, "lead")

	// An editor that only knows schema 4 can never write over schema-5 marks.
	old, _ := json.Marshal(map[string]any{"id": "old-client", "schema": 4, "page_id": page.ID, "epoch": before.Epoch, "ifRev": before.Rev, "target": "other", "ifHash": hashOf(before, "other"), "text": "Schema four overwrite."})
	if out, err := cli(string(old), "doc", "patch", slug, "--file", "-"); err == nil {
		t.Fatalf("schema-4 client wrote a schema-5 Doc: %s", out)
	}

	// The human's private chat names the Doc, two blocks and its own schema.
	f.a.RunID, f.a.ChatID, f.a.MessageID = "proposal5-run", "proposal5-chat", "proposal5-message"
	mustExec(t, f.rig.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES(?,?,?,?,'private')`, f.a.ChatID, f.a.WorkspaceID, f.a.AgentID, f.a.HumanUserID)
	if f.receipt, err = chataudience.CaptureTrusted(t.Context(), f.rig.db, f.a.ChatID, f.a.HumanUserID, nil); err != nil || f.receipt == nil {
		t.Fatalf("private chat: %v", err)
	}
	mustExec(t, f.rig.db, `INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts,author_user_id) VALUES(?,?,?,'user','Tighten the first two paragraphs','2026-11-09T00:00:00Z',?)`, f.a.MessageID, f.a.ChatID, f.a.AgentID, f.a.HumanUserID)
	if err := f.capture(httptest.NewRequest("POST", "/", nil), f.a.MessageID, f.receipt); err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.rig.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('proposal5-start',?,?,?,'run.started','system','test',?)`, f.a.WorkspaceID, f.a.CrewID, f.a.AgentID, f.a.RunID)
	pages := router.Pages()
	// A mandate naming a schema the Doc does not have is refused.
	stale := documents.IterationIntent{SubmissionID: "propose5-0", PageID: page.ID, Schema: 4, Rev: before.Rev, Generation: before.Generation, Blocks: "lead,other"}
	must("", "page", "grant", slug, "--agent", "principal-agent", "--level", "propose")
	var directGrants int
	if err := f.rig.db.QueryRow(`SELECT count(*) FROM page_grants WHERE page_id=? AND subject_type='agent' AND subject_id=? AND level IN ('write','produce') AND retired_at IS NULL`, page.ID, f.a.AgentID).Scan(&directGrants); err != nil || directGrants != 0 {
		t.Fatalf("proposal fixture acquired direct Edit: %d %v", directGrants, err)
	}
	if err := pages.persistDocumentIterationIntent(httptest.NewRequest("POST", "/", nil), f.receipt, f.a.MessageID, stale); err == nil {
		t.Fatal("a schema-4 mandate was admitted on a schema-5 Doc")
	}
	intent := documents.IterationIntent{SubmissionID: "propose5-1", PageID: page.ID, Schema: 5, Rev: before.Rev, Generation: before.Generation, Blocks: "lead,other"}
	if err := pages.persistDocumentIterationIntent(httptest.NewRequest("POST", "/", nil), f.receipt, f.a.MessageID, intent); err != nil {
		t.Fatalf("persist schema-5 propose mandate: %v", err)
	}
	if err := bindIterationFixture(t, f, intent); err != nil {
		t.Fatalf("bind schema-5 propose mandate: %v", err)
	}

	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(master, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	exchange := func(method string, body []byte) (int, []byte, error) {
		return request(method, "/api/v1/internal/documents/draft", token, body)
	}
	status, raw, err := exchange(http.MethodGet, nil)
	if err != nil || status != 200 {
		t.Fatalf("propose view: %d %s %v", status, raw, err)
	}
	var view sidecar.DocumentDraftView
	if err := json.Unmarshal(raw, &view); err != nil || view.Mode != "propose" || view.Schema != 5 {
		t.Fatalf("propose view: %s", raw)
	}
	targets := map[string]string{}
	for _, target := range view.Targets {
		targets[target.ID] = target.Hash
	}
	if targets["lead"] == "" || targets["other"] == "" || targets["tail"] != "" {
		t.Fatalf("view targets: %+v", view.Targets)
	}
	prepare := func(target, text, id string) sidecar.DocumentProposalPacket {
		t.Helper()
		input, _ := json.Marshal(sidecar.DocumentDraftPrepare{View: view, Target: target, Text: text, MutationID: id})
		prepared, err := sidecar.PrepareDocumentDraftOrProposal(input)
		if err != nil {
			t.Fatalf("prepare %s: %v", id, err)
		}
		packet, ok := prepared.(sidecar.DocumentProposalPacket)
		if !ok || packet.Proposal.Schema != 5 {
			t.Fatalf("schema-5 propose view prepared %T %+v", prepared, prepared)
		}
		return packet
	}
	leadText := "Deliver underlined in red and marked by  for  today."
	// The agent CLI binary prepares the packet (crewship doc draft prepare is
	// local). view and propose need the sidecar's fixed loopback origin, so
	// this test sends the packet with the same sidecar code the CLI calls.
	agentInput, _ := json.Marshal(sidecar.DocumentDraftPrepare{View: view, Target: "lead", Text: leadText, MutationID: "proposal5-lead"})
	agentCLI := exec.CommandContext(t.Context(), binary, "doc", "draft", "prepare", "--file", "-", "--format", "json")
	agentCLI.Stdin = bytes.NewReader(agentInput)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "CREWSHIP_") {
			agentCLI.Env = append(agentCLI.Env, entry)
		}
	}
	agentCLI.Env = append(agentCLI.Env, "CREWSHIP_AGENT_TOKEN="+token, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
	prepared, err := agentCLI.Output()
	if err != nil {
		t.Fatalf("agent CLI prepare: %v %s", err, prepared)
	}
	lead, err := sidecar.ParseDocumentProposalPacket(bytes.TrimSpace(prepared))
	if err != nil || lead.Proposal.Schema != 5 || lead.Proposal.Target != "lead" {
		t.Fatalf("agent CLI packet: %v %s", err, prepared)
	}
	if lead != prepare("lead", leadText, "proposal5-lead") {
		t.Fatal("the CLI and the sidecar prepare different packets")
	}
	// A direct patch on a propose mandate is refused; nothing is written.
	patch, _ := json.Marshal(map[string]any{"operation": "patch", "generation": lead.Generation, "request": map[string]any{"id": "direct5", "schema": 5, "page_id": page.ID, "epoch": view.Epoch, "ifRev": view.Rev, "target": "lead", "ifHash": lead.Proposal.IfHash, "text": leadText}})
	if status, raw, _ := exchange(http.MethodPost, patch); status != 403 {
		t.Fatalf("direct patch on a propose mandate: %d %s", status, raw)
	}
	if outcome, err := sidecar.ApplyDocumentProposal(lead, exchange); err != nil || outcome.Status != "proposed" {
		t.Fatalf("propose lead: %+v %v", outcome, err)
	}
	if replay, err := sidecar.ApplyDocumentProposal(lead, exchange); err != nil || replay.Status != "proposed" {
		t.Fatalf("exact proposal retry: %+v %v", replay, err)
	}
	other := prepare("other", "Second paragraph, tightened.", "proposal5-other")
	if outcome, err := sidecar.ApplyDocumentProposal(other, exchange); err != nil || outcome.Status != "proposed" {
		t.Fatalf("propose other: %+v %v", outcome, err)
	}
	// Text inserted exactly beside the date is ambiguous and refused.
	beside := prepare("lead", "Ship underlined in red and marked by X for  today.", "proposal5-beside")
	if outcome, err := sidecar.ApplyDocumentProposal(beside, exchange); err == nil || outcome.Status == "proposed" {
		t.Fatalf("an edit beside a date was proposed: %+v", outcome)
	}
	// A block outside the mandate is refused by the server, not only the sidecar.
	outside, _ := json.Marshal(map[string]any{"operation": "propose", "generation": view.Generation, "request": map[string]any{"id": "proposal5-tail", "schema": 5, "page_id": page.ID, "epoch": view.Epoch, "ifRev": view.Rev, "target": "tail", "ifHash": hashOf(before, "tail"), "text": "Rewritten outside."}})
	if status, raw, _ := exchange(http.MethodPost, outside); status < 400 {
		t.Fatalf("proposal outside the mandate: %d %s", status, raw)
	}
	if after := readDoc(); after.Rev != before.Rev || !bytes.Equal(after.Content, before.Content) {
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
		if p.Status != "open" {
			t.Fatalf("proposal not open: %+v", p)
		}
		ids[p.Target.BlockIDs[0]] = p.ID
	}

	// A concurrent human change on the second paragraph, through the CLI.
	human, _ := json.Marshal(map[string]any{"id": "human5-other", "schema": 5, "page_id": page.ID, "epoch": before.Epoch, "ifRev": before.Rev, "target": "other", "ifHash": hashOf(before, "other"), "text": "Second paragraph, by a person."})
	must(string(human), "doc", "patch", slug, "--file", "-")
	moved := readDoc()
	if moved.Rev != before.Rev+1 {
		t.Fatalf("human patch rev: %d", moved.Rev)
	}
	// The proposal on the changed block can no longer be accepted, only rejected.
	if out, err := cli("", "doc", "proposal-accept", slug, ids["other"], "--expected-rev", fmt.Sprint(moved.Rev), "--request-id", "accept5-other"); err == nil {
		t.Fatalf("accepted a proposal whose block a person changed: %s", out)
	}
	var rejected DocumentProposalReceipt
	if raw := must("", "doc", "proposal-reject", slug, ids["other"], "--expected-rev", fmt.Sprint(moved.Rev), "--request-id", "reject5-other"); json.Unmarshal(raw, &rejected) != nil || rejected.Rev != moved.Rev {
		t.Fatalf("reject: %s", raw)
	}
	var detail DocumentProposalWire
	if raw := must("", "doc", "proposal", slug, ids["lead"]); json.Unmarshal(raw, &detail) != nil || len(detail.ProposedContent) == 0 {
		t.Fatalf("proposal detail: %s", raw)
	}
	var decided DocumentProposalReceipt
	if raw := must("", "doc", "proposal-accept", slug, ids["lead"], "--expected-rev", fmt.Sprint(moved.Rev), "--request-id", "accept5-lead"); json.Unmarshal(raw, &decided) != nil || decided.Rev != moved.Rev+1 || decided.Kind != "human" {
		t.Fatalf("accept: %s", raw)
	}
	var retried DocumentProposalReceipt
	if raw := must("", "doc", "proposal-accept", slug, ids["lead"], "--expected-rev", fmt.Sprint(moved.Rev), "--request-id", "accept5-lead"); json.Unmarshal(raw, &retried) != nil || !retried.Duplicate || retried.Rev != decided.Rev {
		t.Fatalf("exact decision retry: %s", raw)
	}
	accepted := readDoc()
	if accepted.Schema != 5 || accepted.Rev != moved.Rev+1 {
		t.Fatalf("accepted state: schema %d rev %d", accepted.Schema, accepted.Rev)
	}
	acceptedLead := block(accepted, "lead")
	var proposedValue, acceptedValue any
	_ = json.Unmarshal(detail.ProposedContent, &proposedValue)
	_ = json.Unmarshal(acceptedLead, &acceptedValue)
	if fmt.Sprint(proposedValue) != fmt.Sprint(acceptedValue) {
		t.Fatalf("accepted block is not the reviewed proposal:\n%s\n%s", acceptedLead, detail.ProposedContent)
	}
	for _, want := range []string{`"text":"Deliver "`, `"type":"underline"`, `"color":"red"`, `"type":"textColor"`, `"color":"yellow"`, `"type":"highlight"`, `"type":"bold"`, `"value":"2026-11-20"`, `"userId":"` + f.a.HumanUserID + `"`} {
		if !strings.Contains(string(acceptedLead), want) {
			t.Fatalf("accepted lead lost %s: %s", want, acceptedLead)
		}
	}
	if !strings.Contains(string(block(accepted, "other")), "by a person") || !bytes.Equal(block(accepted, "tail"), block(before, "tail")) {
		t.Fatalf("other blocks changed: %s", accepted.Content)
	}
	if raw := must("", "doc", "proposals", slug); !strings.Contains(string(raw), `"accepted"`) || !strings.Contains(string(raw), `"rejected"`) {
		t.Fatalf("decided statuses: %s", raw)
	}

	// History: the creation checkpoint is restored at schema 5 with its marks.
	var history struct {
		Checkpoints []documentCheckpointSummary `json:"checkpoints"`
	}
	if raw := must("", "doc", "checkpoints", slug); json.Unmarshal(raw, &history) != nil || len(history.Checkpoints) == 0 {
		t.Fatalf("checkpoints: %s", raw)
	}
	initial := ""
	for _, c := range history.Checkpoints {
		if c.Rev == 0 {
			initial = c.ID
			if c.Schema != 5 {
				t.Fatalf("creation checkpoint schema: %+v", c)
			}
		}
	}
	if initial == "" {
		t.Fatalf("no creation checkpoint: %+v", history.Checkpoints)
	}
	restore, _ := json.Marshal(map[string]any{"page_id": page.ID, "id": "restore5", "schema": 5, "epoch": accepted.Epoch, "ifRev": accepted.Rev, "checkpoint": initial})
	must(string(restore), "doc", "restore", slug, "--file", "-")
	restored := readDoc()
	if restored.Schema != 5 || restored.Rev != accepted.Rev+1 || !restored.Proposals {
		t.Fatalf("restored state: schema %d rev %d proposals %v", restored.Schema, restored.Rev, restored.Proposals)
	}
	var was, now any
	_ = json.Unmarshal(originalLead, &was)
	_ = json.Unmarshal(block(restored, "lead"), &now)
	if fmt.Sprint(was) != fmt.Sprint(now) {
		t.Fatalf("restore lost schema-5 content:\n%s\n%s", originalLead, block(restored, "lead"))
	}
}
