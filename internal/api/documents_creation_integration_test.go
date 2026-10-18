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
	t.Run("legacy-create-only", func(t *testing.T) { testDocumentCreationManagedRouterSidecarCLI(t, false) })
	t.Run("explicit-drafting", func(t *testing.T) { testDocumentCreationManagedRouterSidecarCLI(t, true) })
}

func testDocumentCreationManagedRouterSidecarCLI(t *testing.T, drafting bool) {
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
	f, intent := documentCreationAdmissionFixture(t, drafting)
	if err := bindCreation(f, intent); err != nil {
		t.Fatal(err)
	}
	h, workspace, owner := f.pages, f.a.WorkspaceID, f.a.HumanUserID
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, workspace, owner)
	execOrFatal(t, h.db, `UPDATE workspaces SET slug='managed-private-cli' WHERE id=?`, workspace)
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
	executeDoc := func(args ...string) ([]byte, error) {
		command := exec.CommandContext(t.Context(), binary, append([]string{"doc"}, args...)...)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "CREWSHIP_") && !strings.HasPrefix(entry, "HTTP_PROXY=") {
				command.Env = append(command.Env, entry)
			}
		}
		command.Env = append(command.Env, "CREWSHIP_AGENT_TOKEN="+token, "CREWSHIP_SERVER=https://untrusted.invalid", "CREWSHIP_PROFILE=must-not-load-human-profile", "HTTP_PROXY=http://127.0.0.1:9")
		return command.CombinedOutput()
	}
	execute := func(inputFile string) ([]byte, error) { return executeDoc("create", "--file", inputFile) }
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
	// Exercise the advertised MCP wire against the same live sidecar, Router,
	// private Node process and durable creation used by the actual CLI above.
	mcpCreate := func(arguments string, wantStatus int, wantError bool) json.RawMessage {
		t.Helper()
		payload := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "doc_create", "arguments": json.RawMessage(arguments)}}
		raw, status, err := request("http://127.0.0.1:9119", "POST", "/mcp/routines", token, "", payload)
		var envelope struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err != nil || status != http.StatusOK || json.Unmarshal(raw, &envelope) != nil || len(envelope.Result.Content) != 1 || envelope.Result.Content[0].Type != "text" || envelope.Result.IsError != wantError {
			t.Fatalf("actual MCP creation envelope: HTTP%d %v %s", status, err, raw)
		}
		var result struct {
			Status int             `json:"status"`
			Body   json.RawMessage `json:"body"`
		}
		if json.Unmarshal([]byte(envelope.Result.Content[0].Text), &result) != nil || result.Status != wantStatus || len(result.Body) == 0 {
			t.Fatalf("actual MCP creation wrapped status: %s", envelope.Result.Content[0].Text)
		}
		return result.Body
	}
	var mcpRepeated documentCreationResult
	if raw := mcpCreate(authoring, http.StatusOK, false); json.Unmarshal(raw, &mcpRepeated) != nil || mcpRepeated != created {
		t.Fatal("actual MCP exact retry changed the CLI-created document identity")
	}
	mcpCreate(strings.Replace(authoring, "Pilot with one team", "A changed MCP plan", 1), http.StatusConflict, true)
	var creationCount, documentCount int
	if err := h.db.QueryRow(`SELECT count(*) FROM document_creations`).Scan(&creationCount); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT count(*) FROM documents`).Scan(&documentCount); err != nil || creationCount != 1 || documentCount != 1 {
		t.Fatalf("MCP retry/conflict duplicated creation: ledger%d documents%d %v", creationCount, documentCount, err)
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
	// The private document was created through the actual agent CLI for a
	// MEMBER. Separate authenticated identities exercise production routing,
	// the real Node service, catalogs and access reports through a real binary.
	for _, role := range []string{"OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"} {
		id := "managed-private-" + role
		pagesSeedUser(t, h, workspace, id, strings.ToLower(role)+"@private.example", role)
		login, err := sessions.NewDBStore(h.db).Create(t.Context(), id, "synthetic", "127.0.0.1", auth.RefreshTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		bearer, err := validator.IssueAccessToken(id, login.ID, role, strings.ToLower(role)+"@private.example")
		if err != nil {
			t.Fatal(err)
		}
		cfg := filepath.Join(dir, "human-"+role+".yaml")
		if err := os.WriteFile(cfg, []byte("server: "+host.URL+"\nworkspace: managed-private-cli\ntoken: "+bearer+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		invoke := func(args ...string) ([]byte, error) {
			cmd := exec.CommandContext(t.Context(), binary, append(args, "--format", "json")...)
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "CREWSHIP_") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1")
			return cmd.CombinedOutput()
		}
		for _, args := range [][]string{{"page", "get", created.Slug}, {"doc", "read", created.Slug}, {"page", "get", "@" + created.PageID}, {"doc", "read", "@" + created.PageID}} {
			out, err := invoke(args...)
			if role == "OWNER" {
				if err != nil {
					t.Fatalf("OWNER actual CLI %v: %v %s", args, err, out)
				}
			} else if err == nil || !bytes.Contains(out, []byte("404")) || bytes.Contains(out, []byte("Launch plan")) {
				t.Fatalf("%s disclosed private document via actual CLI %v: %v %s", role, args, err, out)
			}
		}
		out, err := invoke("page", "list")
		if err != nil {
			t.Fatalf("actual CLI catalog: %v %s", err, out)
		}
		if bytes.Contains(out, []byte(created.Slug)) != (role == "OWNER") {
			t.Fatalf("%s catalog audience: %s", role, out)
		}
		if role == "ADMIN" || role == "OWNER" {
			out, err := invoke("page", "access", "--subject", "user:"+owner)
			if err != nil || bytes.Contains(out, []byte(created.Slug)) != (role == "OWNER") {
				t.Fatalf("%s actual CLI subject access audience: %v %s", role, err, out)
			}
		}
		if role == "OWNER" {
			out, err := invoke("page", "access", created.Slug)
			if err != nil || bytes.Contains(out, []byte("admin@private.example")) {
				t.Fatalf("actual access report: %v %s", err, out)
			}
		}
	}
	var ownerAccessCount int
	if err := h.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE entity_id=? AND action='artifact.private_owner_accessed' AND user_id='managed-private-OWNER'`, created.PageID).Scan(&ownerAccessCount); err != nil || ownerAccessCount < 2 {
		t.Fatalf("real CLI OWNER reads not audited: %d %v", ownerAccessCount, err)
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
	if drafting {
		viewRaw, err := executeDoc("draft", "view")
		if err != nil {
			t.Fatalf("actual drafting view after valid creation: %v %s", err, viewRaw)
		}
		var view struct {
			PageID     string                 `json:"page_id"`
			Schema     int                    `json:"schema"`
			Rev        int64                  `json:"rev"`
			Generation int64                  `json:"generation"`
			Epoch      string                 `json:"epoch"`
			Targets    []DocumentOutlineBlock `json:"targets"`
		}
		if len(viewRaw) > 64<<10 || json.Unmarshal(viewRaw, &view) != nil || view.PageID != created.PageID || view.Schema != 3 || view.Rev != 0 || view.Epoch == "" || len(view.Targets) == 0 || bytes.Contains(viewRaw, []byte(`"snapshot"`)) || bytes.Contains(viewRaw, []byte(`"updates"`)) {
			t.Fatalf("actual drafting view lacks bounded consent-bound targets: %s", viewRaw)
		}
		var target DocumentOutlineBlock
		for _, candidate := range view.Targets {
			if candidate.Type == "paragraph" && candidate.Text == "Pilot with one team and review feedback." {
				target = candidate
			}
		}
		if target.ID == "" || len(target.Hash) != 64 {
			t.Fatal("draft view did not expose the real saved paragraph")
		}
		// New draft tools return direct semantic JSON; doc_create retains its
		// original HTTP-status wrapper above.
		mcpDraft := func(name string, arguments any, wantError bool) []byte {
			t.Helper()
			payload := map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": name, "arguments": arguments}}
			raw, status, err := request("http://127.0.0.1:9119", "POST", "/mcp/routines", token, "", payload)
			var envelope struct {
				Result struct {
					IsError bool `json:"isError"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			if err != nil || status != 200 || json.Unmarshal(raw, &envelope) != nil || len(envelope.Result.Content) != 1 || envelope.Result.Content[0].Type != "text" || envelope.Result.IsError != wantError {
				t.Fatalf("actual draft MCP %s: HTTP%d %v %s", name, status, err, raw)
			}
			return []byte(envelope.Result.Content[0].Text)
		}
		writeJSONFile := func(name string, value any) string {
			t.Helper()
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			return path
		}
		type draftRequest struct {
			ID     string `json:"id"`
			Schema int    `json:"schema"`
			PageID string `json:"page_id"`
			Epoch  string `json:"epoch"`
			IfRev  int64  `json:"ifRev"`
			Target string `json:"target,omitempty"`
			IfHash string `json:"ifHash,omitempty"`
			Text   string `json:"text,omitempty"`
		}
		type draftPacket struct {
			Generation int64        `json:"generation"`
			Patch      draftRequest `json:"patch"`
			Checkpoint draftRequest `json:"checkpoint"`
		}
		type preparedDraft struct {
			Status string      `json:"status"`
			Packet draftPacket `json:"packet"`
		}
		prepareInput := map[string]any{"view": json.RawMessage(viewRaw), "target": target.ID, "text": "CONFIRMED-A checkpoint content", "mutation_id": "managed-draft-a"}
		preparedRaw, err := executeDoc("draft", "prepare", "--file", writeJSONFile("prepare-a.json", prepareInput))
		var prepared preparedDraft
		if err != nil || json.Unmarshal(preparedRaw, &prepared) != nil || prepared.Status != "prepared_unsaved" {
			t.Fatalf("actual immutable prepare: %v %s", err, preparedRaw)
		}
		packet := prepared.Packet
		if packet.Generation != view.Generation || packet.Patch.ID != "managed-draft-a" || packet.Patch.Schema != view.Schema || packet.Patch.PageID != created.PageID || packet.Patch.Epoch != view.Epoch || packet.Patch.IfRev != view.Rev || packet.Patch.Target != target.ID || packet.Patch.IfHash != target.Hash || packet.Patch.Text != "CONFIRMED-A checkpoint content" || packet.Checkpoint.ID != "managed-draft-a-checkpoint" || packet.Checkpoint.IfRev != view.Rev+1 || packet.Checkpoint.Epoch != view.Epoch || packet.Checkpoint.PageID != created.PageID {
			t.Fatalf("prepare changed the original server scope: %+v", packet)
		}
		packetFile := filepath.Join(dir, "immutable-a.json")
		if err := os.WriteFile(packetFile, preparedRaw, 0600); err != nil {
			t.Fatal(err)
		}
		type appliedDraft struct {
			Status     string                   `json:"status"`
			Patch      DocumentMutationResponse `json:"patch"`
			Checkpoint DocumentMutationResponse `json:"checkpoint"`
		}
		assertApplied := func(raw []byte) {
			t.Helper()
			var result appliedDraft
			if json.Unmarshal(raw, &result) != nil || result.Status != "checkpointed" || result.Patch.ID != packet.Patch.ID || result.Patch.Rev != 1 || result.Patch.Epoch != view.Epoch || result.Patch.Actor != "agent:"+f.a.AgentID+":"+run || result.Patch.Kind != "agent" || result.Checkpoint.ID != packet.Checkpoint.ID || result.Checkpoint.Checkpoint != packet.Checkpoint.ID || result.Checkpoint.Rev != 1 || result.Checkpoint.Epoch != view.Epoch {
				t.Fatalf("actual commit/pin confirmation: %s", raw)
			}
		}
		applied, err := executeDoc("draft", "apply", "--file", packetFile)
		if err != nil {
			t.Fatalf("actual semantic patch and pin: %v %s", err, applied)
		}
		assertApplied(applied)
		for _, retry := range []string{"apply", "checkpoint"} {
			raw, err := executeDoc("draft", retry, "--file", packetFile)
			if err != nil {
				t.Fatalf("actual exact %s retry: %v %s", retry, err, raw)
			}
			if retry == "apply" {
				assertApplied(raw)
			} else {
				var result appliedDraft
				if json.Unmarshal(raw, &result) != nil || result.Status != "checkpointed" || result.Checkpoint.ID != packet.Checkpoint.ID || result.Checkpoint.Rev != 1 {
					t.Fatalf("checkpoint retry changed pin: %s", raw)
				}
			}
		}
		assertApplied(mcpDraft("doc_draft_apply", map[string]any{"packet": packet}, false))
		pin := mcpDraft("doc_draft_checkpoint", map[string]any{"packet": packet}, false)
		var pinned appliedDraft
		if json.Unmarshal(pin, &pinned) != nil || pinned.Status != "checkpointed" || pinned.Checkpoint.Checkpoint != packet.Checkpoint.ID || pinned.Checkpoint.Rev != 1 {
			t.Fatalf("MCP pin retry changed result: %s", pin)
		}
		unchanged, err := os.ReadFile(packetFile)
		if err != nil || !bytes.Equal(unchanged, preparedRaw) {
			t.Fatal("apply/retry rewrote the original immutable packet")
		}
		changedPacket := packet
		changedPacket.Patch.Text = "Changed same-ID payload must not replace A"
		changed, err = executeDoc("draft", "apply", "--file", writeJSONFile("changed-packet.json", changedPacket))
		if err == nil || !bytes.Contains(changed, []byte("409")) {
			t.Fatalf("changed same-ID packet not conflicted: %v %s", err, changed)
		}
		mcpDraft("doc_draft_apply", map[string]any{"packet": changedPacket}, true)
		assertDurableA := func() {
			t.Helper()
			raw, status, err := request(host.URL, "GET", "/api/v1/pages/"+created.Slug+"/document", human, "", nil)
			var current DocumentStateResponse
			if err != nil || status != 200 || json.Unmarshal(raw, &current) != nil || current.PageID != created.PageID || current.Rev != 1 || !bytes.Contains(raw, []byte("CONFIRMED-A checkpoint content")) || bytes.Contains(raw, []byte("UNCONFIRMED-B")) || bytes.Contains(raw, []byte(changedPacket.Patch.Text)) {
				t.Fatalf("durable A changed: HTTP%d %v %s", status, err, raw)
			}
			var head, cursor, checkpointRev int64
			var receipts, checkpoints int
			if err := h.db.QueryRow(`SELECT head_rev FROM documents WHERE page_id=?`, created.PageID).Scan(&head); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow(`SELECT draft_rev FROM document_creation_consents WHERE result_page_id=?`, created.PageID).Scan(&cursor); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow(`SELECT rev FROM document_checkpoints WHERE page_id=? AND id=?`, created.PageID, packet.Checkpoint.ID).Scan(&checkpointRev); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow(`SELECT count(*) FROM document_receipts WHERE page_id=?`, created.PageID).Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow(`SELECT count(*) FROM document_checkpoints WHERE page_id=?`, created.PageID).Scan(&checkpoints); err != nil {
				t.Fatal(err)
			}
			if head != 1 || cursor != 1 || checkpointRev != 1 || receipts != 3 || checkpoints != 1 {
				t.Fatalf("retry/conflict changed durable rows: head%d cursor%d pin%d receipts%d pins%d", head, cursor, checkpointRev, receipts, checkpoints)
			}
		}
		assertDurableA()
		nextView := mcpDraft("doc_draft_view", map[string]any{}, false)
		if json.Unmarshal(nextView, &view) != nil || view.Rev != 1 || view.Epoch != packet.Patch.Epoch || view.PageID != created.PageID {
			t.Fatalf("draft reread changed identity/fence: %s", nextView)
		}
		pendingInput := map[string]any{"view": json.RawMessage(nextView), "target": target.ID, "text": "UNCONFIRMED-B same-document pending content", "mutation_id": "managed-draft-b"}
		pendingRaw, err := executeDoc("draft", "prepare", "--file", writeJSONFile("prepare-b.json", pendingInput))
		var pending preparedDraft
		if err != nil || json.Unmarshal(pendingRaw, &pending) != nil || pending.Status != "prepared_unsaved" || pending.Packet.Patch.IfRev != 1 || pending.Packet.Patch.PageID != created.PageID || pending.Packet.Patch.Target != target.ID || pending.Packet.Patch.Text != pendingInput["text"] {
			t.Fatalf("same-Document B was not prepared: %v %s", err, pendingRaw)
		}
		var mcpPending preparedDraft
		if raw := mcpDraft("doc_draft_prepare", pendingInput, false); json.Unmarshal(raw, &mcpPending) != nil || mcpPending != pending {
			t.Fatalf("MCP prepare rederived original packet: %s", raw)
		}
		// Preparing B is local only: it has not been sent to patch or checkpoint.
		assertDurableA()
		fresh := f.a
		fresh.RunID, fresh.MessageID = "managed-fresh-run", "principal-next-message"
		if err := captureArtifactChatPrincipal(httptest.NewRequest("POST", "/", nil), h.db, fresh.RunID, workspace, fresh.AgentID, fresh.CrewID, fresh.ChatID, fresh.MessageID, f.receipt); err != nil {
			t.Fatal(err)
		}
		mustExec(t, h.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('managed-fresh-start',?,?,?,'run.started','system','test',?)`, workspace, fresh.CrewID, fresh.AgentID, fresh.RunID)
		if err := h.checkArtifactRunPrincipal(t.Context(), &fresh); err != nil {
			t.Fatalf("fresh principal control: %v", err)
		}
		freshToken := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, fresh.CrewID), workspace, fresh.AgentID, fresh.RunID)
		crewToken := internaltoken.DeriveCrewToken(h.artifactMasterToken, workspace, fresh.CrewID)
		for _, method := range []string{"GET", "POST"} {
			var body any
			if method == "POST" {
				body = map[string]any{"operation": "patch", "generation": pending.Packet.Generation, "request": pending.Packet.Patch}
			}
			raw, status, err := request(host.URL, method, "/api/v1/internal/documents/draft", freshToken, crewToken, body)
			if err != nil || status != 403 || bytes.Contains(raw, []byte("CONFIRMED-A")) || bytes.Contains(raw, []byte("UNCONFIRMED-B")) {
				t.Fatalf("new live run inherited draft rights: HTTP%d %v %s", status, err, raw)
			}
		}
		assertDurableA()
		// A human's intervening semantic edit invalidates the original agent cursor.
		raw, status, err = request(host.URL, "GET", "/api/v1/pages/"+created.Slug+"/document", human, "", nil)
		if err != nil || status != 200 || json.Unmarshal(raw, &saved) != nil {
			t.Fatalf("human edit control: HTTP%d %v", status, err)
		}
		var humanTarget DocumentOutlineBlock
		for _, candidate := range saved.Outline {
			if candidate.ID == target.ID {
				humanTarget = candidate
			}
		}
		if humanTarget.ID == "" || humanTarget.Text != "CONFIRMED-A checkpoint content" {
			t.Fatal("human cannot see confirmed A target")
		}
		humanPatch := map[string]any{"id": "managed-human-edit", "schema": saved.Schema, "page_id": created.PageID, "epoch": saved.Epoch, "ifRev": saved.Rev, "target": humanTarget.ID, "ifHash": humanTarget.Hash, "text": "Human intervening content"}
		raw, status, err = request(host.URL, "POST", "/api/v1/pages/"+created.Slug+"/document/patch", human, "", humanPatch)
		if err != nil || status != 200 {
			t.Fatalf("actual intervening human edit: HTTP%d %v %s", status, err, raw)
		}
		refusedView, err := executeDoc("draft", "view")
		if err == nil || !bytes.Contains(refusedView, []byte("409")) {
			t.Fatalf("agent reread silently rebound scope: %v %s", err, refusedView)
		}
		refusedPatch, err := executeDoc("draft", "apply", "--file", writeJSONFile("pending-b.json", pending))
		if err == nil || !bytes.Contains(refusedPatch, []byte("409")) {
			t.Fatalf("pending B rebased after human edit: %v %s", err, refusedPatch)
		}
		raw, status, err = request(host.URL, "GET", "/api/v1/pages/"+created.Slug+"/document", human, "", nil)
		if err != nil || status != 200 || json.Unmarshal(raw, &saved) != nil || saved.Rev != 2 || !bytes.Contains(raw, []byte("Human intervening content")) || bytes.Contains(raw, []byte("UNCONFIRMED-B")) {
			t.Fatalf("refusal lost human edit or published B: HTTP%d %v %s", status, err, raw)
		}
		for _, check := range []struct {
			query string
			want  int
		}{
			{`SELECT count(*) FROM document_creations`, 1}, {`SELECT count(*) FROM documents`, 1},
			{`SELECT count(*) FROM document_checkpoints`, 1}, {`SELECT count(*) FROM document_receipts`, 4},
			{`SELECT count(*) FROM page_grants WHERE subject_type='agent'`, 0},
			{`SELECT count(*) FROM document_receipts WHERE id='managed-draft-b'`, 0},
		} {
			var count int
			if err := h.db.QueryRow(check.query).Scan(&count); err != nil || count != check.want {
				t.Fatalf("draft side effects: %s count%d want%d %v", check.query, count, check.want, err)
			}
		}
		if err := filepath.Walk(filepath.Join(dir, "storage"), func(_ string, info os.FileInfo, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				return fmt.Errorf("private Document unexpectedly published a storage file")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		t.Log("actual CLI+MCP draft: immutable original packet, semantic commit and pinned A, exact retries/conflict, B prepared but never committed, live human cursor fence, fresh-run denial, no copies/grants/storage publication; no model/container-interruption evidence")
	}
	t.Log(fmt.Sprintf("actual protected CLI binary/fixed sidecar/Router/private Node24: schema3 content+server identities, CLI+MCP exact retry, changed409, one durable creation, durable creation provenance, human owner read, no continuation delegation; no model run; Page %s", created.PageID))
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
