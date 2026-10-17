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
	f, intent := documentCreationAdmissionFixture(t)
	if err := bindCreation(f, intent); err != nil {
		t.Fatal(err)
	}
	h, workspace, owner := f.pages, f.a.WorkspaceID, f.a.HumanUserID
	h.artifactMasterToken = "synthetic-managed-create-master"
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
	router, err := NewRouter(h.db, jwtSecret, newTestLogger(), WithInternalToken(h.artifactMasterToken), WithJournal(writer), WithStoragePath(filepath.Join(dir, "storage")), WithDocumentAuthority(authoritySocket, secretFile), WithDocumentService(nodeSocket))
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
	// The F2 fixture directly admits the trusted human consent and original live
	// run; this test does not qualify public chat bridge admission.
	run := f.a.RunID
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, f.a.CrewID), workspace, f.a.AgentID, run)
	// Reserve only the documented protected destination. If occupied, fail
	// explicitly; never evict or redirect a shared runtime to make this pass.
	reservation, err := net.Listen("tcp", "127.0.0.1:9119")
	if err != nil {
		t.Fatalf("owned protected loopback9119 unavailable; no shared runtime eviction: %v", err)
	}
	_ = reservation.Close()
	side := sidecar.NewServer(sidecar.ServerConfig{Addr: "127.0.0.1:9119", Logger: newTestLogger(), IPC: &sidecar.IPCConfig{BaseURL: host.URL, Token: internaltoken.DeriveCrewToken(h.artifactMasterToken, workspace, f.a.CrewID), WorkspaceID: workspace, CrewID: f.a.CrewID, AgentID: f.a.AgentID, AgentSlug: "principal-agent", AgentToken: internaltoken.DeriveAgentToken(h.artifactMasterToken, workspace, f.a.AgentID), AgentRunKey: internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, f.a.CrewID), RunID: run, RunChatID: f.a.ChatID}})
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
	var durablePage, durableRun, durableHuman, durableChat string
	if err := h.db.QueryRow(`SELECT page_id,run_id,human_user_id,source_chat_id FROM document_creations`).Scan(&durablePage, &durableRun, &durableHuman, &durableChat); err != nil || durablePage != created.PageID || durableRun != run || durableHuman != owner || durableChat != f.a.ChatID {
		t.Fatalf("durable creation provenance differs: %v", err)
	}
	var grants int
	if err := h.db.QueryRow(`SELECT count(*) FROM page_grants WHERE page_id=? AND subject_type='agent'`, created.PageID).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("creation expanded Page grants: %d %v", grants, err)
	}
	// Creation does not silently delegate subsequent document operations.
	raw, status, err = request(host.URL, "GET", "/api/v1/pages/"+created.Slug+"/document", token, "", nil)
	if err != nil || status != 401 || bytes.Contains(raw, []byte("Pilot with one team")) {
		t.Fatalf("creation expanded subsequent read: %d %v %s", status, err, raw)
	}
	var actor string
	if err = h.db.QueryRow(`SELECT actor FROM document_receipts WHERE page_id=?`, created.PageID).Scan(&actor); err != nil || actor != "agent:"+f.a.AgentID+":"+run {
		t.Fatalf("creation audit actor %s %v", actor, err)
	}
	t.Log(fmt.Sprintf("actual protected CLI binary/fixed sidecar/Router/private Node24: schema3 content+server identities, exact retry, changed409, durable creation provenance, human owner read, no continuation delegation; no model run; Page %s", created.PageID))
}

func documentsWaitSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("private child socket did not start: ", path)
}

func documentsWaitReady(t *testing.T, socket, secret string) {
	t.Helper()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(t.Context(), "GET", "http://docs/health/ready", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Crewship-Docs-Service", secret)
		if response, err := client.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("document engine did not acknowledge readiness after binding")
}
