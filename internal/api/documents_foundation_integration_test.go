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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/documents"
)

const documentsProcessFixtureEnv = "CREWSHIP_TEST_DOCS_AUTHORITY_FIXTURE"

type documentsProcessFixture struct{ Database, JWTSecret, ServiceSecret, MasterToken, Socket string }

// This child opens the parent's migrated file and serves only its private test
// socket. SIGKILL deliberately bypasses shutdown, exercising actual SQLite and
// writer-fence recovery across a new Go process.
func TestDocumentsAuthorityProcess(t *testing.T) {
	path := os.Getenv(documentsProcessFixtureEnv)
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f documentsProcessFixture
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open("file:" + f.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	router, err := NewRouter(db.DB, f.JWTSecret, newTestLogger(), WithInternalToken(f.MasterToken))
	if err != nil {
		t.Fatal(err)
	}
	router.pages.artifactMasterToken = f.MasterToken
	handler, err := router.DocumentAuthority(t.Context(), f.ServiceSecret)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := documents.ServePrivateUnix(f.Socket, handler, func() error { return documents.New(db.DB, documents.Options{}).InvalidateLeases(t.Context()) })
	if err != nil {
		t.Fatal(err)
	}
	defer stop(context.Background())
	<-t.Context().Done()
}

func TestDocumentsFoundationRealProcesses(t *testing.T) {
	if os.Getenv("CREWSHIP_DOCS_FOUNDATION") != "1" {
		t.Fatal("opted-in suite requires CREWSHIP_DOCS_FOUNDATION=1 and frozen tools/docs-service installation")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("opted-in Docs foundation requires Node 24: ", err)
	}
	version, err := exec.Command(node, "--version").Output()
	if err != nil || !strings.HasPrefix(string(version), "v24.") {
		t.Fatalf("Node 24 required: %s %v", version, err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	servicePath := filepath.Join(root, "tools/docs-service/service.mjs")
	if _, err = os.Stat(filepath.Join(root, "tools/docs-service/node_modules/yjs")); err != nil {
		t.Fatal("opted-in Docs foundation requires frozen pnpm install: ", err)
	}
	h, workspace, user, page, agent := artifactRunFixture(t)
	// A short private path stays below sockaddr_un's platform limit even when
	// the test harness's own temporary root has a long name.
	dir, err := os.MkdirTemp("", "docs-foundation-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	authoritySocket := filepath.Join(dir, "a.sock")
	nodeSocket := filepath.Join(dir, "n.sock")
	secret := strings.Repeat("foundation-secret-", 3)
	jwtSecret := strings.Repeat("jwt-fixture-", 4)
	tokenFile := filepath.Join(dir, "secret")
	if err = os.WriteFile(tokenFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(dir, "authority.json")
	raw, _ := json.Marshal(documentsProcessFixture{reviewDBPath(t, h.db), jwtSecret, secret, h.artifactMasterToken, authoritySocket})
	if err = os.WriteFile(fixturePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	execOrFatal(t, h.db, `INSERT INTO user_sessions(id,user_id,created_at,expires_at,last_used_at) VALUES('docs-session',?,?,?,?)`, user, now.Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339), now.Format(time.RFC3339))
	validator, err := auth.NewJWTValidator(jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	human, err := validator.IssueAccessToken(user, "docs-session", "Docs human", "docs@example.test")
	if err != nil {
		t.Fatal(err)
	}
	// Reuse production page creation to ensure owner and permission fields match.
	second := "docs-second"
	recorder := httptest.NewRecorder()
	h.Create(recorder, pagesGrantRequest(t, "POST", "/api/v1/pages", workspace, user, "OWNER", `{"slug":"docs-second","name":"Second","panels":[],"allow_empty":true}`))
	if recorder.Code != 201 {
		t.Fatalf("create second: %d %s", recorder.Code, recorder.Body.String())
	}
	var secondPage struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(recorder.Body.Bytes(), &secondPage); err != nil {
		t.Fatal(err)
	}
	second = secondPage.ID
	var goChild, nodeChild *exec.Cmd
	kill := func(cmd **exec.Cmd, socket string) {
		if *cmd != nil {
			_ = (*cmd).Process.Kill()
			_ = (*cmd).Wait()
			*cmd = nil
		}
		_ = os.Remove(socket)
	}
	defer func() { kill(&nodeChild, nodeSocket); kill(&goChild, authoritySocket) }()
	startGo := func() {
		executable, e := os.Executable()
		if e != nil {
			t.Fatal(e)
		}
		goChild = exec.Command(executable, "-test.run=^TestDocumentsAuthorityProcess$", "-test.timeout=4m")
		goChild.Env = append(os.Environ(), documentsProcessFixtureEnv+"="+fixturePath)
		goChild.Stderr = os.Stderr
		if e = goChild.Start(); e != nil {
			t.Fatal(e)
		}
		documentsWaitSocket(t, authoritySocket)
	}
	startNode := func() {
		nodeChild = exec.Command(node, servicePath)
		nodeChild.Env = append(os.Environ(), "CREWSHIP_DOCS_SOCKET="+nodeSocket, "CREWSHIP_DOCS_AUTHORITY_SOCKET="+authoritySocket, "CREWSHIP_DOCS_SERVICE_TOKEN_FILE="+tokenFile)
		nodeChild.Stderr = os.Stderr
		if e := nodeChild.Start(); e != nil {
			t.Fatal(e)
		}
		documentsWaitSocket(t, nodeSocket)
		documentsWaitReady(t, nodeSocket, secret)
	}
	startGo()
	startNode()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", nodeSocket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second}
	call := func(page, op, token string, body map[string]any, want int) map[string]any {
		t.Helper()
		data, _ := json.Marshal(body)
		req, e := http.NewRequest("POST", "http://docs/v1/documents/"+page+"/"+op, bytes.NewReader(data))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("X-Crewship-Docs-Service", secret)
		req.Header.Set("X-Workspace-ID", workspace)
		req.Header.Set("Authorization", "Bearer "+token)
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		data, e = io.ReadAll(res.Body)
		if e != nil {
			t.Fatal(e)
		}
		if res.StatusCode != want {
			t.Fatalf("%s %s status %d want %d: %s", page, op, res.StatusCode, want, data)
		}
		var result map[string]any
		if e = json.Unmarshal(data, &result); e != nil {
			t.Fatal(e)
		}
		return result
	}
	schemas := documentResponseSchemas(t)
	content := map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "attrs": map[string]any{"id": "intro"}, "content": []any{map[string]any{"type": "text", "text": "initial"}}}}}
	for _, p := range []string{page, second} {
		init := map[string]any{"id": "init-" + p, "schema": 2, "body": content}
		initialized := call(p, "initialize", human, init, 200)
		validateDocumentResponse(t, schemas, "DocumentInitializationState", mustDocumentsJSON(t, initialized))
		for _, field := range []string{"schema", "rev", "epoch", "generation", "incarnation", "snapshot", "snapshotRev", "updates", "blocks"} {
			if _, ok := initialized[field]; !ok {
				t.Fatalf("actual first initialize missing state field %s: %v", field, initialized)
			}
		}
		for _, field := range []string{"id", "actor", "kind"} {
			if _, ok := initialized[field]; ok {
				t.Fatalf("actual first initialize unexpectedly contains receipt field %s", field)
			}
		}
		if _, numeric := initialized["epoch"].(float64); !numeric {
			t.Fatalf("actual first initialize epoch is not numeric: %T", initialized["epoch"])
		}
		retry := call(p, "initialize", human, init, 200)
		validateDocumentResponse(t, schemas, "DocumentInitializeReceipt", mustDocumentsJSON(t, retry))
		for _, field := range []string{"id", "rev", "epoch", "actor", "kind", "found", "duplicate"} {
			if _, ok := retry[field]; !ok {
				t.Fatalf("actual initialize retry missing receipt field %s", field)
			}
		}
		if retry["duplicate"] != true {
			t.Fatal("initialize retry not deduplicated")
		}
	}
	read := func(p, token string) map[string]any { return call(p, "read", token, map[string]any{}, 200) }
	state := read(page, agent)
	block := state["outline"].([]any)[0].(map[string]any)
	patch := map[string]any{"id": "confirmed-agent", "schema": 2, "epoch": state["epoch"], "target": "intro", "text": "confirmed across crashes", "ifRev": state["rev"], "ifHash": block["hash"]}
	commitStarted := time.Now()
	ack := call(page, "patch", agent, patch, 200)
	commitDuration := time.Since(commitStarted)
	if ack["rev"] != state["rev"].(float64)+1 {
		t.Fatalf("ack %v", ack)
	}
	duplicate := call(page, "patch", agent, patch, 200)
	if duplicate["duplicate"] != true {
		t.Fatalf("retry %v", duplicate)
	}
	// An independent document proceeds through the same managed process.
	s2 := read(second, human)
	b2 := s2["outline"].([]any)[0].(map[string]any)
	call(second, "patch", human, map[string]any{"id": "second-write", "schema": 2, "epoch": s2["epoch"], "target": "intro", "text": "independent", "ifRev": s2["rev"], "ifHash": b2["hash"]}, 200)
	// Hold a real SQL writer for 50ms to measure bounded shared-file contention.
	contentionState := read(second, human)
	contentionBlock := contentionState["outline"].([]any)[0].(map[string]any)
	held, err := h.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = held.Exec(`UPDATE document_writer_fence SET seq=seq+1 WHERE id=1`); err != nil {
		held.Rollback()
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() { time.Sleep(50 * time.Millisecond); _ = held.Rollback(); close(released) }()
	contentionStarted := time.Now()
	call(second, "patch", human, map[string]any{"id": "contended-write", "schema": 2, "epoch": contentionState["epoch"], "target": "intro", "text": "independent after contention", "ifRev": contentionState["rev"], "ifHash": contentionBlock["hash"]}, 200)
	contentionDuration := time.Since(contentionStarted)
	<-released
	beforeNodeRestart := read(page, agent)
	kill(&nodeChild, nodeSocket)
	startNode()
	transport.CloseIdleConnections()
	state = read(page, agent)
	if state["epoch"] == beforeNodeRestart["epoch"] {
		t.Fatal("Node restart reused the public synchronization epoch")
	}
	staleNode := map[string]any{"id": "unconfirmed-before-node-reboot", "schema": 2, "epoch": beforeNodeRestart["epoch"], "target": "intro", "text": "must not replay stale draft", "ifRev": beforeNodeRestart["rev"], "ifHash": beforeNodeRestart["outline"].([]any)[0].(map[string]any)["hash"]}
	call(page, "patch", agent, staleNode, 409)
	if call(page, "patch", agent, patch, 200)["duplicate"] != true {
		t.Fatal("Node reboot lost confirmed original-request retry")
	}
	if !strings.Contains(string(mustDocumentsJSON(t, state["content"])), "confirmed across crashes") {
		t.Fatal("Node restart lost confirmed content")
	}
	oldEpoch := state["epoch"]
	// Simulate an in-flight writer abandoned by abrupt Go process death.
	execOrFatal(t, h.db, `UPDATE documents SET writer='abandoned-writer',owner='abandoned-process',lease_until=? WHERE workspace_id=? AND page_id=?`, time.Now().Add(time.Minute).UnixMilli(), workspace, page)
	kill(&goChild, authoritySocket)
	startGo()
	state = read(page, agent)
	if state["epoch"] == oldEpoch {
		t.Fatal("Go restart failed to fence abandoned writer")
	}
	staleGo := map[string]any{"id": "unconfirmed-before-go-reboot", "schema": 2, "epoch": oldEpoch, "target": "intro", "text": "must not replay stale authority draft", "ifRev": state["rev"], "ifHash": state["outline"].([]any)[0].(map[string]any)["hash"]}
	call(page, "patch", agent, staleGo, 409)
	if call(page, "patch", agent, patch, 200)["duplicate"] != true {
		t.Fatal("Go reboot lost confirmed original-request retry")
	}
	if state["rev"] != ack["rev"] {
		t.Fatalf("Go restart lost revision: %v", state)
	}
	if !strings.Contains(string(mustDocumentsJSON(t, state["content"])), "confirmed across crashes") {
		t.Fatal("Go restart lost replayed content")
	}
	var snapshotBytes, updateBytes, updates int
	if err = h.db.QueryRow(`SELECT length(snapshot) FROM documents WHERE workspace_id=? AND page_id=?`, workspace, page).Scan(&snapshotBytes); err != nil {
		t.Fatal(err)
	}
	if err = h.db.QueryRow(`SELECT count(*),coalesce(sum(length(payload)),0) FROM document_updates WHERE workspace_id=? AND page_id=?`, workspace, page).Scan(&updates, &updateBytes); err != nil {
		t.Fatal(err)
	}
	if updates != 1 || updateBytes <= 0 {
		t.Fatalf("expected incremental durable replay, got %d updates/%d bytes", updates, updateBytes)
	}
	t.Logf("confirmed commit=%s snapshot=%dB replay=%d updates/%dB", commitDuration, snapshotBytes, updates, updateBytes)
	call(page, "patch", human, map[string]any{"id": "future", "schema": 99, "epoch": state["epoch"]}, 409)
	cp := map[string]any{"id": "checkpoint-one", "schema": 2, "epoch": state["epoch"], "ifRev": state["rev"]}
	call(page, "checkpoint", human, cp, 200)
	if call(page, "checkpoint", human, cp, 200)["duplicate"] != true {
		t.Fatal("checkpoint retry not deduplicated")
	}
	changedCheckpoint := map[string]any{"id": "checkpoint-one", "schema": 2, "epoch": cp["epoch"], "ifRev": state["rev"].(float64) + 1}
	call(page, "checkpoint", human, changedCheckpoint, 409)
	state = read(page, human)
	restore := map[string]any{"id": "restore-one", "schema": 2, "epoch": state["epoch"], "ifRev": state["rev"], "checkpoint": "checkpoint-one"}
	restored := call(page, "restore", human, restore, 200)
	if restored["rev"] != state["rev"].(float64)+1 {
		t.Fatal("restore did not advance exactly once")
	}
	if call(page, "restore", human, restore, 200)["duplicate"] != true {
		t.Fatal("restore retry not deduplicated")
	}
	if read(page, human)["rev"] != restored["rev"] {
		t.Fatal("restore retry advanced durable revision")
	}

	// Grade public human response contracts against the real Go bridge and real
	// Node computation/Go persistence, including fields added by the bridge.
	h.documentCall = func(_ context.Context, ws, bearer, p, operation string, body any) ([]byte, int, error) {
		if ws != workspace {
			t.Fatal("public bridge changed workspace")
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, 500, err
		}
		var input map[string]any
		if err := json.Unmarshal(encoded, &input); err != nil {
			return nil, 500, err
		}
		result := call(p, operation, strings.TrimPrefix(bearer, "Bearer "), input, 200)
		encoded, err = json.Marshal(result)
		return encoded, 200, err
	}
	public := func(suffix, component string, input map[string]any) map[string]any {
		t.Helper()
		method, body := "GET", ""
		if input != nil {
			method = "POST"
			body = string(mustDocumentsJSON(t, input))
		}
		req := pagesGrantRequest(t, method, "/api/v1/pages/report/document"+suffix, workspace, user, "OWNER", body)
		req.SetPathValue("slug", "report")
		req.Header.Set("Authorization", "Bearer "+human)
		if strings.HasPrefix(suffix, "/checkpoints/") {
			req.SetPathValue("checkpoint", strings.TrimPrefix(suffix, "/checkpoints/"))
		}
		response := httptest.NewRecorder()
		if input == nil {
			h.GetDocument(response, req)
		} else {
			h.MutateDocument(response, req)
		}
		if response.Code != 200 {
			t.Fatalf("public %s status%d: %s", suffix, response.Code, response.Body.String())
		}
		return validateDocumentResponse(t, schemas, component, response.Body.Bytes())
	}
	publicState := public("", "DocumentState", nil)
	public("/comments", "DocumentComments", nil)
	public("/comments", "DocumentReceipt", map[string]any{"id": "public-contract-comment", "schema": 2, "epoch": publicState["epoch"], "ifRev": publicState["rev"], "block": "intro", "from": 0, "to": 3, "text": "Contract-checked discussion"})
	public("/comments", "DocumentComments", nil)
	public("/comments/resolve", "DocumentReceipt", map[string]any{"id": "public-contract-resolve", "schema": 2, "epoch": publicState["epoch"], "ifRev": publicState["rev"], "comment": "public-contract-comment", "version": 1, "resolved": true})
	public("/comments", "DocumentComments", nil)
	public("/checkpoints", "DocumentCheckpoints", nil)
	public("/checkpoints/checkpoint-one", "DocumentCheckpoint", nil)

	// Real Node computes/validates Yjs and sends semantic metadata across the
	// private HTTP bridge; Go enforces selection bounds in its writer transaction.
	// Fixture admission identities are synthetic, not a model-completion claim.
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'agent','artifact-agent','write',?,?)`, second, user, now.Format(time.RFC3339))
	boundCall := func(page, operation, token string, body map[string]any, want int) map[string]any {
		t.Helper()
		result := call(page, operation, token, body, want)
		t.Logf("selected-request actual HTTP page=%s operation=%s status=%d request=%s response=%s", page, operation, want, mustDocumentsJSON(t, body), mustDocumentsJSON(t, result))
		return result
	}
	for scenarioIndex, scenario := range []string{"selected success", "fresh rebase", "revoked grant"} {
		scenarioRun := fmt.Sprintf("bound-run-%d", scenarioIndex)
		execOrFatal(t, h.db, `INSERT INTO artifact_run_principals(run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at) SELECT ?,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at FROM artifact_run_principals WHERE run_id='artifact-run'`, scenarioRun)
		execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES(?,?,'artifact-crew','artifact-agent','run.started','system','fixture',?)`, scenarioRun+"-started", workspace, scenarioRun)
		scenarioAgent := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, "artifact-crew"), workspace, "artifact-agent", scenarioRun)
		if scenarioIndex == 0 {
			execOrFatal(t, h.db, `INSERT INTO page_artifact_run_delegations(page_id,run_id,granted_by_user_id,expires_at,granted_at) SELECT ?,?,granted_by_user_id,expires_at,granted_at FROM page_artifact_run_delegations WHERE page_id=?`, second, scenarioRun, page)
		}
		slug := "bound-" + strings.ReplaceAll(scenario, " ", "-")
		created := httptest.NewRecorder()
		h.Create(created, pagesGrantRequest(t, "POST", "/api/v1/pages", workspace, user, "OWNER", string(mustDocumentsJSON(t, map[string]any{"slug": slug, "name": scenario, "panels": []any{}, "allow_empty": true}))))
		if created.Code != 201 {
			t.Fatalf("bound create: %d %s", created.Code, created.Body.String())
		}
		var createdPage struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(created.Body.Bytes(), &createdPage); err != nil {
			t.Fatal(err)
		}
		p := createdPage.ID
		execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'agent','artifact-agent','write',?,?)`, p, user, now.Format(time.RFC3339))
		body := map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "attrs": map[string]any{"id": "bound-intro"}, "content": []any{map[string]any{"type": "text", "text": "Hello world"}}}, map[string]any{"type": "paragraph", "attrs": map[string]any{"id": "bound-other"}, "content": []any{map[string]any{"type": "text", "text": "Preserve other block"}}}}}
		boundCall(p, "initialize", human, map[string]any{"id": "init-" + p, "schema": 2, "body": body}, 200)
		baseline := read(p, human)
		first := baseline["outline"].([]any)[0].(map[string]any)
		admission := map[string]any{"page_id": p, "chat_id": "artifact-chat", "instruction": "Revise only selected text", "base_rev": baseline["rev"], "submission_id": "selected-" + p, "targets": []any{map[string]any{"id": "bound-intro", "hash": first["hash"], "from": 2, "to": 5, "quote": "llo"}}}
		req := pagesGrantRequest(t, "POST", "/", workspace, user, "OWNER", string(mustDocumentsJSON(t, admission)))
		req.SetPathValue("slug", slug)
		req.Header.Set("Authorization", "Bearer "+human)
		UserFromContext(req.Context()).SessionID = "docs-session"
		queued := httptest.NewRecorder()
		h.CreateDocumentRequest(queued, req)
		if queued.Code != 202 {
			t.Fatalf("actual canonical admission: %d %s", queued.Code, queued.Body.String())
		}
		var job DocumentRequestCreateResponse
		if err := json.Unmarshal(queued.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		execOrFatal(t, h.db, `UPDATE document_requests SET status='running' WHERE id=?`, job.ID)
		trusted, err := chataudience.CaptureTrusted(t.Context(), h.db, "artifact-chat", user, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = bindDocumentRequestRun(httptest.NewRequest("POST", "/", nil), h.db, job.ID, scenarioRun, trusted); err != nil {
			t.Fatal(err)
		}
		current := read(p, scenarioAgent)
		patch := map[string]any{"id": "bound-patch-" + p, "schema": 2, "epoch": current["epoch"], "target": "bound-intro", "text": "HeNEW world", "ifRev": current["rev"], "ifHash": first["hash"]}
		assertNoMutation := func(wantRev any) {
			t.Helper()
			if got := read(p, human)["rev"]; got != wantRev {
				t.Fatalf("rejected bound patch advanced rev: %v want %v", got, wantRev)
			}
			var receipts, checkpoints, results int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM document_receipts WHERE page_id=?`, p).Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM document_checkpoints WHERE page_id=?`, p).Scan(&checkpoints); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM document_requests WHERE id=? AND (status='succeeded' OR result_revision IS NOT NULL OR checkpoint_id IS NOT NULL)`, job.ID).Scan(&results); err != nil {
				t.Fatal(err)
			}
			// Initialization has its own receipt; only human edits may add another.
			if receipts != 1+int(wantRev.(float64)) || checkpoints != 0 || results != 0 {
				t.Fatalf("rejected request persisted receipt/checkpoint/result: %d/%d/%d", receipts, checkpoints, results)
			}
		}
		switch scenario {
		case "fresh rebase":
			boundCall(p, "patch", human, map[string]any{"id": "intervening-" + p, "schema": 2, "epoch": current["epoch"], "target": "bound-intro", "text": "Hello HUMAN world", "ifRev": current["rev"], "ifHash": first["hash"]}, 200)
			fresh := read(p, scenarioAgent)
			freshBlock := fresh["outline"].([]any)[0].(map[string]any)
			patch["epoch"] = fresh["epoch"]
			patch["ifRev"] = fresh["rev"]
			patch["ifHash"] = freshBlock["hash"]
			boundCall(p, "patch", scenarioAgent, patch, 409)
			assertNoMutation(fresh["rev"])
		case "revoked grant":
			execOrFatal(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_type='agent'`, p)
			boundCall(p, "patch", scenarioAgent, patch, 403)
			assertNoMutation(baseline["rev"])
		case "selected success":
			otherState := read(second, human)
			otherBlock := otherState["outline"].([]any)[0].(map[string]any)
			var beforeOtherReceipts int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM document_receipts WHERE page_id=?`, second).Scan(&beforeOtherReceipts); err != nil {
				t.Fatal(err)
			}
			boundCall(second, "read", scenarioAgent, map[string]any{}, 403)
			boundCall(second, "patch", scenarioAgent, map[string]any{"id": "cross-page-forbidden", "schema": 2, "epoch": otherState["epoch"], "target": otherBlock["id"], "text": "Cross-page escape", "ifRev": otherState["rev"], "ifHash": otherBlock["hash"]}, 403)
			var afterOtherReceipts int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM document_receipts WHERE page_id=?`, second).Scan(&afterOtherReceipts); err != nil {
				t.Fatal(err)
			}
			if read(second, human)["rev"] != otherState["rev"] || beforeOtherReceipts != afterOtherReceipts {
				t.Fatal("cross-page rejection mutated other document")
			}
			patch["text"] = "CHANGED EVERYTHING"
			boundCall(p, "patch", scenarioAgent, patch, 409)
			assertNoMutation(baseline["rev"])
			patch["target"] = "bound-other"
			patch["ifHash"] = baseline["outline"].([]any)[1].(map[string]any)["hash"]
			patch["text"] = "HeNEW world"
			boundCall(p, "patch", scenarioAgent, patch, 409)
			assertNoMutation(baseline["rev"])
			patch["target"] = "bound-intro"
			patch["ifHash"] = first["hash"]
			ack := boundCall(p, "patch", scenarioAgent, patch, 200)
			if ack["rev"] != baseline["rev"].(float64)+1 {
				t.Fatal("bound patch did not advance once")
			}
			if boundCall(p, "patch", scenarioAgent, patch, 200)["duplicate"] != true {
				t.Fatal("bound patch exact retry not durable")
			}
			fresh := read(p, scenarioAgent)
			checkpoint := map[string]any{"id": job.ID + "-checkpoint", "schema": 2, "epoch": fresh["epoch"], "ifRev": fresh["rev"]}
			boundCall(p, "checkpoint", scenarioAgent, checkpoint, 200)
			boundCall(p, "checkpoint", scenarioAgent, checkpoint, 200)
			freshBlock := fresh["outline"].([]any)[0].(map[string]any)
			patch["id"] = "second-forbidden-" + p
			patch["ifRev"] = fresh["rev"]
			patch["ifHash"] = freshBlock["hash"]
			patch["epoch"] = fresh["epoch"]
			boundCall(p, "patch", scenarioAgent, patch, 409)
			if got := read(p, human)["rev"]; got != fresh["rev"] {
				t.Fatal("second bound patch changed saved revision")
			}
			execOrFatal(t, h.db, `UPDATE document_requests SET run_id=NULL WHERE id=?`, job.ID)
			execOrFatal(t, h.db, `DELETE FROM document_requests WHERE id=?`, job.ID)
			var principals int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM artifact_run_principals WHERE run_id=?`, scenarioRun).Scan(&principals); err != nil || principals != 1 {
				t.Fatalf("deletion fixture must retain principal: %d %v", principals, err)
			}
			boundCall(second, "read", scenarioAgent, map[string]any{}, 403)
			boundCall(second, "patch", scenarioAgent, map[string]any{"id": "deleted-request-cross-page", "schema": 2, "epoch": otherState["epoch"], "target": otherBlock["id"], "text": "Deleted request escape", "ifRev": otherState["rev"], "ifHash": otherBlock["hash"]}, 403)
			if read(second, human)["rev"] != otherState["rev"] {
				t.Fatal("deleted request escaped to other document")
			}
		}
	}

	// Bounded representative history measurement: nine documents total, one
	// 200-paragraph document with forty small edits crossing compaction at 32.
	readRSS := func() int {
		b, e := os.ReadFile(fmt.Sprintf("/proc/%d/status", nodeChild.Process.Pid))
		if e != nil {
			return 0
		}
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) > 1 && f[0] == "VmRSS:" {
				n, _ := strconv.Atoi(f[1])
				return n
			}
		}
		return 0
	}
	rssBefore := readRSS()
	var representative string
	paragraphs := make([]any, 200)
	for i := range paragraphs {
		paragraphs[i] = map[string]any{"type": "paragraph", "attrs": map[string]any{"id": fmt.Sprintf("p%03d", i)}, "content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("Paragraph %03d: ", i) + strings.Repeat("Representative shared document content. ", 12)}}}
	}
	for i := 0; i < 4; i++ {
		slug := fmt.Sprintf("measurement-%d", i)
		rr := httptest.NewRecorder()
		h.Create(rr, pagesGrantRequest(t, "POST", "/api/v1/pages", workspace, user, "OWNER", fmt.Sprintf(`{"slug":%q,"name":"Measurement","panels":[],"allow_empty":true}`, slug)))
		if rr.Code != 201 {
			t.Fatalf("measurement page: %d %s", rr.Code, rr.Body.String())
		}
		var p struct {
			ID string `json:"id"`
		}
		if e := json.Unmarshal(rr.Body.Bytes(), &p); e != nil {
			t.Fatal(e)
		}
		body := content
		if i == 0 {
			representative = p.ID
			body = map[string]any{"type": "doc", "content": paragraphs}
		}
		call(p.ID, "initialize", human, map[string]any{"id": "init-" + slug, "schema": 2, "body": body}, 200)
	}
	var initialSnapshotBytes int
	if err = h.db.QueryRow(`SELECT length(snapshot) FROM documents WHERE workspace_id=? AND page_id=?`, workspace, representative).Scan(&initialSnapshotBytes); err != nil {
		t.Fatal(err)
	}
	latencies := make([]float64, 0, 40)
	for i := 0; i < 40; i++ {
		s := read(representative, human)
		b := s["outline"].([]any)[0].(map[string]any)
		started := time.Now()
		call(representative, "patch", human, map[string]any{"id": fmt.Sprintf("history-%02d", i), "schema": 2, "epoch": s["epoch"], "target": "p000", "text": fmt.Sprintf("Edit %02d ", i) + strings.Repeat("Representative edited paragraph. ", 15), "ifRev": s["rev"], "ifHash": b["hash"]}, 200)
		latencies = append(latencies, float64(time.Since(started).Microseconds())/1000)
	}
	rs := read(representative, human)
	call(representative, "checkpoint", human, map[string]any{"id": "representative-checkpoint", "schema": 2, "epoch": rs["epoch"], "ifRev": rs["rev"]}, 200)
	var finalSnapshotBytes, finalProjectionBytes, representativeLogBytes, representativeLogCount, receiptBytes, receiptCount, checkpointBytes, checkpointCount, snapshotRev int
	for _, q := range []struct {
		sql  string
		dest []any
	}{
		{`SELECT length(snapshot),length(projection),snapshot_rev FROM documents WHERE workspace_id=? AND page_id=?`, []any{&finalSnapshotBytes, &finalProjectionBytes, &snapshotRev}},
		{`SELECT count(*),coalesce(sum(length(payload)),0) FROM document_updates WHERE workspace_id=? AND page_id=?`, []any{&representativeLogCount, &representativeLogBytes}},
		{`SELECT count(*),coalesce(sum(length(id)+length(digest)+length(fingerprint)+length(actor)+length(kind)+length(created_at)),0) FROM document_receipts WHERE workspace_id=? AND page_id=?`, []any{&receiptCount, &receiptBytes}},
		{`SELECT count(*),coalesce(sum(length(snapshot)+length(projection)),0) FROM document_checkpoints WHERE workspace_id=? AND page_id=?`, []any{&checkpointCount, &checkpointBytes}},
	} {
		if err = h.db.QueryRow(q.sql, workspace, representative).Scan(q.dest...); err != nil {
			t.Fatal(err)
		}
	}
	if snapshotRev < 32 || representativeLogCount >= 40 || receiptCount != 42 {
		t.Fatalf("history did not compact/preserve receipts: snapshotRev=%d log=%d receipts=%d", snapshotRev, representativeLogCount, receiptCount)
	}
	representativeReport := map[string]any{"paragraphs": 200, "updates": 40, "initialSnapshotBytes": initialSnapshotBytes, "finalSnapshotBytes": finalSnapshotBytes, "finalProjectionBytes": finalProjectionBytes, "snapshotRevision": snapshotRev, "retainedLogUpdates": representativeLogCount, "retainedLogBytes": representativeLogBytes, "receiptCount": receiptCount, "receiptTextBytes": receiptBytes, "checkpointCount": checkpointCount, "checkpointPayloadBytes": checkpointBytes, "logicalPayloadBytes": finalSnapshotBytes + finalProjectionBytes + representativeLogBytes + receiptBytes + checkpointBytes, "commitMilliseconds": latencies, "nodeRSSBeforeKiB": rssBefore, "nodeRSSAfterKiB": readRSS(), "measurementScope": "logical payload lengths; excludes SQLite pages, indexes and WAL; writer hold simulates contention, not slow disk"}
	nodeRSSKB := 0
	if proc, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", nodeChild.Process.Pid)); err == nil {
		for _, line := range strings.Split(string(proc), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "VmRSS:" {
				nodeRSSKB, _ = strconv.Atoi(fields[1])
			}
		}
	}
	report := map[string]any{"protocol": 1, "schema": 2, "documents": 9, "selectedRequestRealHTTP": map[string]any{"scenarios": []string{"selected success", "fresh rebase", "revoked grant"}, "rangeAndTargetRejection": 409, "crossPageGenericDelegationRejection": 403, "deletedRequestAfterRunResetRejection": 403, "freshRebaseRejection": 409, "revokedGrantRejection": 403, "onePatchExactCheckpoint": true, "exactPatchRetry": true, "noRejectedMutationReceiptsOrResults": true, "modelRun": false}, "representativeHistory": representativeReport, "commitMilliseconds": float64(commitDuration.Microseconds()) / 1000, "writerContentionMilliseconds": float64(contentionDuration.Microseconds()) / 1000, "simulatedWriterHoldMilliseconds": 50, "snapshotBytes": snapshotBytes, "replayUpdates": updates, "replayBytes": updateBytes, "nodeRSSKiB": nodeRSSKB, "nodeCrashRestart": true, "goCrashRestart": true, "durableReplay": true}
	reportPath := os.Getenv("CREWSHIP_DOCS_FOUNDATION_OUTPUT")
	if reportPath == "" {
		reportPath = filepath.Join(t.TempDir(), "acceptance.json")
	}
	if !filepath.IsAbs(reportPath) {
		t.Fatal("acceptance output must be absolute")
	}
	if _, err = os.Stat(filepath.Dir(reportPath)); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(reportPath, mustDocumentsJSON(t, report), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("acceptance report: ", reportPath)
	execOrFatal(t, h.db, `UPDATE page_artifact_run_delegations SET revoked_at=? WHERE page_id=?`, now.Format(time.RFC3339), page)
	call(page, "read", agent, map[string]any{}, 403)
	// Exact human session revocation is independently live at the SQL boundary.
	execOrFatal(t, h.db, `UPDATE user_sessions SET revoked_at=? WHERE id='docs-session'`, now.Format(time.RFC3339))
	call(page, "read", human, map[string]any{}, 401)
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
func mustDocumentsJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Listener custody precedes the engine recovery barrier; a bound pathname alone
// is not evidence that this process can safely admit document operations.
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
