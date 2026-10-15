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
	content := map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "attrs": map[string]any{"id": "intro"}, "content": []any{map[string]any{"type": "text", "text": "initial"}}}}}
	for _, p := range []string{page, second} {
		init := map[string]any{"id": "init-" + p, "schema": 2, "body": content}
		initialized := call(p, "initialize", human, init, 200)
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

	// Bounded representative history measurement: six documents total, one
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
	report := map[string]any{"protocol": 1, "schema": 2, "documents": 6, "representativeHistory": representativeReport, "commitMilliseconds": float64(commitDuration.Microseconds()) / 1000, "writerContentionMilliseconds": float64(contentionDuration.Microseconds()) / 1000, "simulatedWriterHoldMilliseconds": 50, "snapshotBytes": snapshotBytes, "replayUpdates": updates, "replayBytes": updateBytes, "nodeRSSKiB": nodeRSSKB, "nodeCrashRestart": true, "goCrashRestart": true, "durableReplay": true}
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
