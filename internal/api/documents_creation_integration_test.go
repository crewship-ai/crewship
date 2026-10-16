//go:build linux && (docs_foundation || conformance)

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/sidecar"
)

func TestDocumentCreationManagedRouterSidecarCLI(t *testing.T) {
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
	h, workspace, owner, _, _ := artifactRunFixture(t)
	dir, err := os.MkdirTemp("", "doc-create-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	const secret = "synthetic-document-creation-private-service-secret"
	secretFile := filepath.Join(dir, "secret")
	if err = os.WriteFile(secretFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	authoritySocket, nodeSocket := filepath.Join(dir, "a.sock"), filepath.Join(dir, "n.sock")
	const jwtSecret = "synthetic-document-creation-jwt-secret"
	writer := journal.NewWriter(h.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	router, err := NewRouter(h.db, jwtSecret, newTestLogger(), WithInternalToken(h.artifactMasterToken), WithJournal(writer), WithStoragePath(h.artifactStoragePath), WithDocumentAuthority(authoritySocket, secretFile), WithDocumentService(nodeSocket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	node := exec.Command("node", filepath.Join(root, "tools/docs-service/service.mjs"))
	node.Env = append(os.Environ(), "CREWSHIP_DOCS_SOCKET="+nodeSocket, "CREWSHIP_DOCS_AUTHORITY_SOCKET="+authoritySocket, "CREWSHIP_DOCS_SERVICE_TOKEN_FILE="+secretFile)
	node.Stderr = os.Stderr
	if err = node.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Process.Kill(); _ = node.Wait() })
	documentsWaitSocket(t, nodeSocket)
	documentsWaitReady(t, nodeSocket, secret)
	host := httptest.NewServer(router)
	t.Cleanup(host.Close)
	receipt, err := chataudience.CaptureTrusted(t.Context(), h.db, "artifact-chat", owner, nil)
	if err != nil || receipt == nil {
		t.Fatal(err)
	}
	transcript := conversation.NewStore(filepath.Join(dir, "transcript"), newTestLogger(), conversation.WithDB(h.db))
	t.Cleanup(transcript.Close)
	intent := documents.CreationIntent{SubmissionID: "managed-first-document"}
	message := conversation.Message{ID: "managed-document-message", AgentID: receipt.AgentID, Role: conversation.RoleUser, Content: "Write a useful launch plan document", AuthorUserID: owner, Timestamp: time.Now(), Metadata: map[string]any{documents.CreationMetadataKey: map[string]any{"submission_id": intent.SubmissionID}}}
	if err = transcript.AppendDocumentCreation(t.Context(), receipt.ChatID, message, intent, receipt); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	request := func(base, method, path, token, internal string, body any) ([]byte, int, error) {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		req, err := http.NewRequestWithContext(t.Context(), method, base+path, bytes.NewReader(raw))
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Workspace-ID", workspace)
		req.Header.Set("Content-Type", "application/json")
		if internal != "" {
			req.Header.Set("X-Internal-Token", internal)
		}
		res, err := client.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer res.Body.Close()
		data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		return data, res.StatusCode, err
	}
	run := "managed-doc-create-run"
	admission := map[string]any{"id": run, "agent_id": receipt.AgentID, "workspace_id": workspace, "chat_id": receipt.ChatID, "human_authority": receipt, "trigger_message_id": message.ID, "artifact_human_receipt": receipt, "artifact_human_message_id": message.ID, "document_creation_intent": intent}
	if raw, status, err := request(host.URL, "POST", "/api/v1/internal/runs", "", h.artifactMasterToken, admission); err != nil || status != 201 {
		t.Fatalf("actual trusted run admission%d %v %s", status, err, raw)
	}
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, "artifact-crew"), workspace, receipt.AgentID, run)
	// Reserve only the documented protected destination. If occupied, fail
	// explicitly; never evict or redirect a shared runtime to make this pass.
	reservation, err := net.Listen("tcp", "127.0.0.1:9119")
	if err != nil {
		t.Fatalf("owned protected loopback9119 unavailable; no shared runtime eviction: %v", err)
	}
	_ = reservation.Close()
	side := sidecar.NewServer(sidecar.ServerConfig{Addr: "127.0.0.1:9119", Logger: newTestLogger(), IPC: &sidecar.IPCConfig{BaseURL: host.URL, Token: internaltoken.DeriveCrewToken(h.artifactMasterToken, workspace, "artifact-crew"), WorkspaceID: workspace, CrewID: "artifact-crew", AgentID: receipt.AgentID, AgentSlug: "artifact-agent", AgentToken: internaltoken.DeriveAgentToken(h.artifactMasterToken, workspace, receipt.AgentID), AgentRunKey: internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, "artifact-crew"), RunID: run, RunChatID: receipt.ChatID}})
	sideCtx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- side.Start(sideCtx) }()
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("owned sidecar stop timeout")
		}
	})
	select {
	case <-side.Ready():
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("owned sidecar readiness timeout")
	}
	binary := filepath.Join(dir, "crewship-cli")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/crewship")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("actual CLI build: %v %s", err, output)
	}
	authoring := `{"slug":"managed-launch-plan","name":"Launch plan","mutation_id":"launch-one","blocks":[{"type":"heading","level":1,"text":"Launch plan"},{"type":"paragraph","text":"Pilot with one team and review feedback."},{"type":"bulletList","items":["Choose an owner.","Record acceptance results."]},{"type":"orderedList","items":["Qualify the pilot.","Expand after review."]},{"type":"codeBlock","text":"const literal = '<script>';\n"}]}`
	file := filepath.Join(dir, "authoring.json")
	if err = os.WriteFile(file, []byte(authoring), 0600); err != nil {
		t.Fatal(err)
	}
	execute := func(inputFile string) ([]byte, error) {
		command := exec.CommandContext(t.Context(), binary, "doc", "create", "--file", inputFile)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "CREWSHIP_") && !strings.HasPrefix(entry, "HTTP_PROXY=") {
				command.Env = append(command.Env, entry)
			}
		}
		command.Env = append(command.Env, "CREWSHIP_AGENT_TOKEN="+token, "CREWSHIP_SERVER=https://untrusted.invalid", "CREWSHIP_PROFILE=must-not-load-human-profile", "HTTP_PROXY=http://127.0.0.1:9")
		return command.CombinedOutput()
	}
	output, err := execute(file)
	if err != nil {
		t.Fatalf("actual protected CLI create: %v %s", err, output)
	}
	var created documentCreationResult
	if json.Unmarshal(output, &created) != nil || created.PageID == "" || created.Schema != 3 || created.Revision != 0 || created.WorkspaceID != workspace || !strings.Contains(created.URL, "workspace_id=") {
		t.Fatalf("actual CLI result: %s", output)
	}
	retry, err := execute(file)
	if err != nil {
		t.Fatalf("actual CLI exact retry: %v %s", err, retry)
	}
	var repeated documentCreationResult
	_ = json.Unmarshal(retry, &repeated)
	if repeated != created {
		t.Fatal("lost-ACK exact retry changed saved identity")
	}
	changedFile := filepath.Join(dir, "changed.json")
	if err = os.WriteFile(changedFile, []byte(strings.Replace(authoring, "Pilot with one team", "A changed plan", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := execute(changedFile)
	if err == nil || !bytes.Contains(changed, []byte("HTTP409")) {
		t.Fatalf("actual CLI changed mutation not rejected: %v %s", err, changed)
	}
	session, err := sessions.NewDBStore(h.db).Create(t.Context(), owner, "synthetic", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := auth.NewJWTValidator(jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	human, err := validator.IssueAccessToken(owner, session.ID, "Synthetic owner", "owned-create@example.test")
	if err != nil {
		t.Fatal(err)
	}
	raw, status, err := request(host.URL, "GET", "/api/v1/pages/"+created.Slug+"/document", human, "", nil)
	var saved DocumentStateResponse
	if err != nil || status != 200 || json.Unmarshal(raw, &saved) != nil || saved.Schema != 3 || saved.Rev != 0 || !bytes.Contains(raw, []byte("Pilot with one team and review feedback.")) || !bytes.Contains(raw, []byte("codeBlock")) {
		t.Fatalf("actual saved human read%d %v %s", status, err, raw)
	}
	for _, block := range saved.Outline {
		if len(block.Hash) != 64 || block.ID == "" {
			t.Fatal("server did not generate canonical block identities/hashes")
		}
	}
	raw, status, err = request(host.URL, "GET", "/api/v1/chats/artifact-chat/artifact-results", human, "", nil)
	var results struct {
		Results []artifactChatResult `json:"results"`
	}
	if err != nil || status != 200 || json.Unmarshal(raw, &results) != nil || len(results.Results) != 1 || results.Results[0].PageID != created.PageID || results.Results[0].Type != "document" || results.Results[0].OnBehalfOfUserID != owner {
		t.Fatalf("durable chat result%d %v %s", status, err, raw)
	}
	// Creation does not silently delegate subsequent document operations.
	raw, status, err = request("http://127.0.0.1:9119", "GET", "/api/v1/pages/"+created.Slug+"/document", token, "", nil)
	if err != nil || status != 403 || bytes.Contains(raw, []byte("Pilot with one team")) {
		t.Fatalf("creation expanded subsequent read: %d %v %s", status, err, raw)
	}
	var actor string
	if err = h.db.QueryRow(`SELECT actor FROM document_receipts WHERE page_id=?`, created.PageID).Scan(&actor); err != nil || actor != "agent:"+receipt.AgentID+":"+run {
		t.Fatalf("creation audit actor %s %v", actor, err)
	}
	t.Log(fmt.Sprintf("actual protected CLI binary/fixed sidecar/Router/private Node24: schema3 content+server identities, exact retry, changed409, durable chat result, human owner read, no continuation delegation; no model run; Page %s", created.PageID))
}
