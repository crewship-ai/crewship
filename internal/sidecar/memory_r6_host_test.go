package sidecar

// R6 acceptance: the agent-facing memory tools reach the host's guaranteed
// ledger through a real host + real sidecar + real SQLite/filesystem boundary.
//
// Real: api.NewRouter (the internal memory mutation and canonical routes, the
// run capability check, the work-ledger authorization, the file lock, the
// durable intent), the sidecar built by NewServer (its MCP handler, its HTTP
// write/read handlers, its host forwarding and R5 sent/unknown discipline),
// agtv2 run tokens derived exactly as the orchestrator derives them, a
// migrated SQLite database and a real storage directory. Substituted: nothing
// on the memory path. There is no agent process and no container; the calls
// are what the MCP client and the HTTP client inside the container send.

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/memory/memdiff"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const (
	r6Master  = "r6-master-token-0123456789abcdef0123456789abcdef"
	r6JWT     = "r6-jwt-secret-long-enough-for-the-validator-yes"
	r6WS      = "ws-r6"
	r6Crew    = "crew-r6"
	r6AgentID = "agent-r6-alpha"
	r6Slug    = "alpha"
	r6RunA    = "run-r6-a"
	r6RunB    = "run-r6-b"
)

type r6Host struct {
	srv     *httptest.Server
	db      *sql.DB
	storage string
}

func newR6Host(t *testing.T) *r6Host {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
	for _, q := range []string{
		`INSERT INTO workspaces (id, name, slug) VALUES ('` + r6WS + `', 'R6', 'r6')`,
		`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus) VALUES ('` + r6Crew + `', '` + r6WS + `', 'R6 crew', 'r6-crew', 'free', 4096, 2.0)`,
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, tool_profile, timeout_seconds, memory_enabled) VALUES ('` + r6AgentID + `', '` + r6WS + `', '` + r6Crew + `', 'Alpha', '` + r6Slug + `', 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 1)`,
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, tool_profile, timeout_seconds, memory_enabled) VALUES ('agent-r6-beta', '` + r6WS + `', '` + r6Crew + `', 'Beta', 'beta', 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	storage, blobs := t.TempDir(), t.TempDir()
	router, err := api.NewRouter(db, r6JWT, runTestLogger(),
		api.WithInternalToken(r6Master), api.WithStoragePath(storage), api.WithMemoryVersionsBlobRoot(blobs))
	if err != nil {
		t.Fatalf("build the real api router: %v", err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return &r6Host{srv: srv, db: db, storage: storage}
}

func (h *r6Host) seedAttempt(t *testing.T, workID, runID string, generation int64) {
	t.Helper()
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	if _, err := h.db.Exec(`
		INSERT INTO work_items (id, workspace_id, source, source_ref, agent_id, crew_id, session_id,
			class, input_json, input_sha256, target_revision, state, generation, attempts,
			priority, eligible_at, created_at, updated_at)
		VALUES (?,?,'webhook','',?,?,'','background','{}','sha','rev-1','running',?,1,0,?,?,?)`,
		workID, r6WS, r6AgentID, r6Crew, generation, now, now, now); err != nil {
		t.Fatalf("seed work item: %v", err)
	}
	if _, err := h.db.Exec(`
		INSERT INTO work_attempts (run_id, work_id, attempt, generation, lease_owner,
			lease_expires_at, heartbeat_at, started_at, runtime_phase)
		VALUES (?,?,1,?,'test','2126-01-01T00:00:00Z',?,?,'confirmed')`,
		runID, workID, generation, now, now); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
}

func (h *r6Host) endAttempt(t *testing.T, runID string) {
	t.Helper()
	if _, err := h.db.Exec(`UPDATE work_attempts SET ended_at = ? WHERE run_id = ?`,
		time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z07:00"), runID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE work_items SET state = 'succeeded' WHERE id = (SELECT work_id FROM work_attempts WHERE run_id = ?)`, runID); err != nil {
		t.Fatal(err)
	}
}

func (h *r6Host) hostFile(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.storage, "crews", r6Crew, "agents", r6Slug, ".memory", filepath.FromSlash(file)))
	if err != nil {
		return ""
	}
	return string(b)
}

func (h *r6Host) mutations(t *testing.T, operationID string) (n int, runID string) {
	t.Helper()
	if err := h.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(run_id), '') FROM memory_mutations WHERE operation_id = ?`, operationID).Scan(&n, &runID); err != nil {
		t.Fatal(err)
	}
	return n, runID
}

func r6RunToken(runID string) string {
	return internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(r6Master, r6WS, r6Crew), r6WS, r6AgentID, runID)
}

// newR6Sidecar is the production constructor with the IPC config the
// orchestrator would hand a per-crew sidecar, pointed at baseURL.
func newR6Sidecar(t *testing.T, baseURL string) (*Server, string) {
	t.Helper()
	t.Setenv(runStateDirEnv, t.TempDir())
	memBase := filepath.Join(t.TempDir(), ".memory")
	s := NewServer(ServerConfig{
		Addr: "127.0.0.1:0",
		IPC: &IPCConfig{
			BaseURL:     baseURL,
			Token:       internaltoken.DeriveCrewToken(r6Master, r6WS, r6Crew),
			AgentID:     r6AgentID,
			AgentSlug:   r6Slug,
			CrewID:      r6Crew,
			WorkspaceID: r6WS,
			ContainerID: "container-r6",
			AgentRunKey: internaltoken.DeriveAgentRunKey(r6Master, r6WS, r6Crew),
			RunID:       r6RunA,
		},
		Memory: &MemoryConfig{Enabled: true, BasePath: memBase, AgentSlug: r6Slug},
		Logger: runTestLogger(),
	})
	return s, memBase
}

type mcpResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func (m mcpResult) meta(t *testing.T) map[string]any {
	t.Helper()
	if len(m.Content) < 2 {
		t.Fatalf("result carries no metadata item: %+v", m)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(m.Content[1].Text), &out); err != nil {
		t.Fatalf("metadata is not JSON: %s", m.Content[1].Text)
	}
	return out
}

func (m mcpResult) errorCode(t *testing.T) string {
	t.Helper()
	if !m.IsError || len(m.Content) == 0 {
		t.Fatalf("not an error result: %+v", m)
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(m.Content[0].Text), &out)
	for _, k := range []string{"error_code", "code"} {
		if v, ok := out[k].(string); ok {
			return v
		}
	}
	return m.Content[0].Text
}

func mcpCall(t *testing.T, s *Server, token, tool string, args map[string]any) mcpResult {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	req := httptest.NewRequest("POST", "/mcp/memory", bytes.NewReader(body))
	req.Host = "127.0.0.1:9119"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.handleMemoryMCP(w, req)
	var resp memoryMCPResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("mcp response %d is not JSON-RPC: %s", w.Code, w.Body.String())
	}
	if resp.Error != nil {
		t.Fatalf("mcp %s: JSON-RPC error %d: %s", tool, resp.Error.Code, resp.Error.Message)
	}
	var res mcpResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("mcp %s: result is not a tool result: %s", tool, resp.Result)
	}
	return res
}

func httpWrite(t *testing.T, s *Server, token string, req MemoryWriteRequest) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(req)
	r := httptest.NewRequest("POST", "/memory/write", bytes.NewReader(body))
	r.Host = "127.0.0.1:9119"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.handleMemoryWrite(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func httpRead(t *testing.T, s *Server, token, scope, file string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest("GET", "/memory/read?scope="+scope+"&file="+file, nil)
	r.Host = "127.0.0.1:9119"
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.handleMemoryRead(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestR6_AgentMemoryToolsReachTheHostLedgerWithOneRevisionHistory(t *testing.T) {
	host := newR6Host(t)
	host.seedAttempt(t, "work-r6-a", r6RunA, 1)
	s, localBase := newR6Sidecar(t, host.srv.URL)
	tokenA := r6RunToken(r6RunA)

	// 1. MCP append → the host ledger, attributed to run A, under the host's
	// audit path. Nothing is written inside the container.
	res := mcpCall(t, s, tokenA, "memory.write", map[string]any{
		"tier": "AGENT", "mode": "append", "content": "one\n", "operation_id": "op-a1"})
	if res.IsError {
		t.Fatalf("mcp append: %s", res.Content[0].Text)
	}
	meta := res.meta(t)
	if meta["profile"] != "guaranteed" || meta["revision"] != float64(1) || meta["audit_path"] != "agent:alpha/AGENT.md" ||
		meta["run_id"] != r6RunA || meta["revision_checked"] != true || meta["content_sha256"] != sha("one\n") {
		t.Fatalf("mcp append metadata = %v", meta)
	}
	if got := host.hostFile(t, "AGENT.md"); got != "one\n" {
		t.Fatalf("host file = %q, want the appended line", got)
	}
	if _, err := os.Stat(filepath.Join(localBase, "AGENT.md")); !os.IsNotExist(err) {
		t.Fatalf("the sidecar wrote AGENT.md locally; the guaranteed path must write only through the host (%v)", err)
	}
	if n, run := host.mutations(t, "op-a1"); n != 1 || run != r6RunA {
		t.Fatalf("ledger rows for op-a1 = %d (run %q), want 1 attributed to %s", n, run, r6RunA)
	}

	// 2. MCP read returns the body AND the revision anchor.
	res = mcpCall(t, s, tokenA, "memory.read", map[string]any{"tier": "AGENT"})
	if res.IsError || res.Content[0].Text != "one\n" {
		t.Fatalf("mcp read = %+v", res)
	}
	if meta := res.meta(t); meta["revision"] != float64(1) || meta["content_sha256"] != sha("one\n") || meta["audit_path"] != "agent:alpha/AGENT.md" {
		t.Fatalf("mcp read metadata = %v", meta)
	}

	// 3. HTTP replace against revision 1 → revision 2; MCP read sees 2. One
	// history, whichever surface wrote it. The body names no run: the
	// capability does.
	code, out := httpWrite(t, s, tokenA, MemoryWriteRequest{File: "AGENT.md", Scope: "agent", Mode: "replace",
		Content: "one\ntwo\n", OperationID: "op-h1", ExpectedRevision: 1, Removals: []memdiff.Removal{}})
	if code != 201 || out["revision"] != float64(2) || out["profile"] != "guaranteed" || out["audit_path"] != "agent:alpha/AGENT.md" {
		t.Fatalf("http replace = %d %v", code, out)
	}
	res = mcpCall(t, s, tokenA, "memory.read", map[string]any{"tier": "AGENT"})
	if res.Content[0].Text != "one\ntwo\n" || res.meta(t)["revision"] != float64(2) {
		t.Fatalf("mcp read after http replace = %+v", res)
	}
	if code, out := httpRead(t, s, tokenA, "agent", "AGENT.md"); code != 200 || out["revision"] != float64(2) || out["revision_checked"] != true {
		t.Fatalf("http read = %d %v, want revision 2 checked", code, out)
	}

	// 4. A retry of op-a1 with the same content is answered with the ORIGINAL
	// result and changes nothing; the same id with other content is a
	// conflict.
	res = mcpCall(t, s, tokenA, "memory.write", map[string]any{
		"tier": "AGENT", "mode": "append", "content": "one\n", "operation_id": "op-a1"})
	if res.IsError || res.meta(t)["idempotent"] != true || res.meta(t)["revision"] != float64(1) {
		t.Fatalf("retry of op-a1 = %+v", res)
	}
	if got := host.hostFile(t, "AGENT.md"); got != "one\ntwo\n" {
		t.Fatalf("a retry appended again: %q", got)
	}
	res = mcpCall(t, s, tokenA, "memory.write", map[string]any{
		"tier": "AGENT", "mode": "append", "content": "other\n", "operation_id": "op-a1"})
	if code := res.errorCode(t); code != "operation_conflict" {
		t.Fatalf("op-a1 with other content = %q, want operation_conflict", code)
	}

	// 5. append_daily with a stable identity: twice → one line.
	daily := map[string]any{"entry": "shipped R6", "operation_id": "op-d1", "date": "2026-09-14", "at": "2026-09-14T10:00:00Z"}
	first := mcpCall(t, s, tokenA, "memory.append_daily", daily)
	again := mcpCall(t, s, tokenA, "memory.append_daily", daily)
	if first.IsError || again.IsError || again.meta(t)["idempotent"] != true {
		t.Fatalf("append_daily = %+v / %+v", first, again)
	}
	if got := host.hostFile(t, "daily/2026-09-14.md"); got != "- 2026-09-14T10:00:00Z — shipped R6\n" {
		t.Fatalf("daily file = %q, want exactly one entry", got)
	}
	if m := first.meta(t); m["date"] != "2026-09-14" || m["at"] != "2026-09-14T10:00:00Z" || m["audit_path"] != "agent:alpha/daily/2026-09-14.md" {
		t.Fatalf("append_daily metadata = %v", m)
	}

	// 6. First write: an explicit "no revision exists yet" replace wins once.
	res = mcpCall(t, s, tokenA, "memory.write", map[string]any{
		"tier": "pins", "mode": "replace", "content": "p1\n", "operation_id": "op-p1", "removals": []any{}, "first_write": true})
	if res.IsError || res.meta(t)["revision"] != float64(1) {
		t.Fatalf("first write = %+v", res)
	}
	res = mcpCall(t, s, tokenA, "memory.write", map[string]any{
		"tier": "pins", "mode": "replace", "content": "p2\n", "operation_id": "op-p2", "removals": []any{}, "first_write": true})
	if code := res.errorCode(t); code != "memory_conflict" {
		t.Fatalf("second first-write = %q, want memory_conflict", code)
	}
	if got := host.hostFile(t, "pins.md"); got != "p1\n" {
		t.Fatalf("pins after a losing first write = %q", got)
	}
	// And a replace with neither precondition is refused before any round trip.
	res = mcpCall(t, s, tokenA, "memory.write", map[string]any{
		"tier": "pins", "mode": "replace", "content": "p3\n", "operation_id": "op-p3", "removals": []any{}})
	if code := res.errorCode(t); code != "expected_revision_required" {
		t.Fatalf("replace without a precondition = %q", code)
	}

	// 7. Run A ends in the ledger. Its token still verifies, and the host
	// refuses: nothing is written anywhere.
	host.endAttempt(t, r6RunA)
	before := host.hostFile(t, "AGENT.md")
	res = mcpCall(t, s, tokenA, "memory.write", map[string]any{
		"tier": "AGENT", "mode": "append", "content": "late\n", "operation_id": "op-a9"})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "not the current live attempt") {
		t.Fatalf("write after the run ended = %+v, want the host's refusal", res)
	}
	if host.hostFile(t, "AGENT.md") != before {
		t.Fatal("a refused write changed the host file")
	}
	if _, err := os.Stat(filepath.Join(localBase, "AGENT.md")); !os.IsNotExist(err) {
		t.Fatal("a refused guaranteed write fell back to a local file")
	}

	// 8. A second attempt reuses the same sidecar with its OWN identity, and a
	// body that names the previous run under B's token is a substitution.
	host.seedAttempt(t, "work-r6-b", r6RunB, 1)
	tokenB := r6RunToken(r6RunB)
	if code, out := httpWrite(t, s, tokenB, MemoryWriteRequest{File: "AGENT.md", Scope: "agent", Mode: "append",
		Content: "b\n", OperationID: "op-b0", RunID: r6RunA}); code != 403 || out["code"] != "memory_run_substitution" {
		t.Fatalf("substituted run id under token B = %d %v", code, out)
	}
	res = mcpCall(t, s, tokenB, "memory.write", map[string]any{
		"tier": "AGENT", "mode": "append", "content": "b\n", "operation_id": "op-b1"})
	if res.IsError || res.meta(t)["run_id"] != r6RunB {
		t.Fatalf("run B write = %+v", res)
	}
	if n, run := host.mutations(t, "op-b1"); n != 1 || run != r6RunB {
		t.Fatalf("op-b1 attributed to %q (%d rows), want %s", run, n, r6RunB)
	}
	if got := host.hostFile(t, "AGENT.md"); got != "one\ntwo\nb\n" {
		t.Fatalf("host file after run B = %q", got)
	}
}

// dropOnceProxy forwards to the host and, for the FIRST mutation POST, lets
// the host commit and then closes the client connection without relaying the
// answer — the lost response R5 is about.
type dropOnceProxy struct {
	target  string
	dropped atomic.Bool
}

func (p *dropOnceProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	out, err := http.NewRequest(r.Method, p.target+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	out.Header = r.Header.Clone()
	resp, err := http.DefaultClient.Do(out)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/memory/mutation") && p.dropped.CompareAndSwap(false, true) {
		// The host has answered (and committed). Drop the connection instead
		// of relaying: the sidecar sees a transport error AFTER sending.
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		http.Error(w, "cannot hijack", 500)
		return
	}
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(answer)
}

func TestR6_LostHostResponseIsUnknownAndTheRetryIsAnsweredOnce(t *testing.T) {
	host := newR6Host(t)
	host.seedAttempt(t, "work-r6-a", r6RunA, 1)
	proxy := httptest.NewServer(&dropOnceProxy{target: host.srv.URL})
	t.Cleanup(proxy.Close)
	s, localBase := newR6Sidecar(t, proxy.URL)
	tokenA := r6RunToken(r6RunA)

	res := mcpCall(t, s, tokenA, "memory.write", map[string]any{
		"tier": "AGENT", "mode": "append", "content": "once\n", "operation_id": "op-l1"})
	if code := res.errorCode(t); code != "memory_mutation_unknown" {
		t.Fatalf("dropped response = %q, want memory_mutation_unknown", code)
	}
	if _, err := os.Stat(filepath.Join(localBase, "AGENT.md")); !os.IsNotExist(err) {
		t.Fatal("an unknown outcome was resolved with a local write")
	}
	if got := host.hostFile(t, "AGENT.md"); got != "once\n" {
		t.Fatalf("host file after the dropped response = %q (the host had committed)", got)
	}

	retry := mcpCall(t, s, tokenA, "memory.write", map[string]any{
		"tier": "AGENT", "mode": "append", "content": "once\n", "operation_id": "op-l1"})
	if retry.IsError || retry.meta(t)["idempotent"] != true || retry.meta(t)["revision"] != float64(1) {
		t.Fatalf("retry under the same operation id = %+v, want the original result", retry)
	}
	if got := host.hostFile(t, "AGENT.md"); got != "once\n" {
		t.Fatalf("the retry appended again: %q", got)
	}
	if n, _ := host.mutations(t, "op-l1"); n != 1 {
		t.Fatalf("%d ledger rows for op-l1, want 1", n)
	}
}
