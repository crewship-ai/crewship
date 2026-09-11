package api

// The host memory-mutation surface, driven end to end: the real requireInternal
// middleware in front of the real handlers, over HTTP, with real derived
// tokens, a real migrated SQLite database and a real temporary filesystem.
//
// A fake would prove the handler calls the functions it calls. What is under
// test is whether ProfileGuaranteed is REACHABLE — whether a write through this
// route actually reaches the ledger and comes back with a revision that means
// something — and whether every requirement it refuses without is genuinely
// refused. Both are properties of the database's constraints and the
// filesystem's rename, so they are exercised against both.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/memory/memdiff"
)

const memMutMaster = "memory-mutation-test-master-token"

const (
	memMutWS    = "test-workspace-id"
	memMutCrew  = "crew-memmut"
	memMutAgent = "agent-memmut-alpha"
	memMutSlug  = "alpha"
	memMutRun   = "run-memmut-1"
)

type memMutFixture struct {
	t       *testing.T
	db      *sql.DB
	srv     *httptest.Server
	storage string
	blobs   string
}

// newMemMutFixture seeds one workspace, one crew, two agents (alpha acts, beta
// is the sibling every cross-agent assertion uses) and one LIVE work attempt
// for alpha at generation 3, then serves requireInternal(mutation|canonical)
// over httptest.
func newMemMutFixture(t *testing.T) *memMutFixture {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if wsID != memMutWS {
		t.Fatalf("unexpected seeded workspace id %q", wsID)
	}
	seedCrewRow(t, db, memMutCrew, wsID, "MemMut", "memmut")
	execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES (?, ?, ?, 'Alpha', ?)`,
		memMutAgent, wsID, memMutCrew, memMutSlug)
	execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('agent-memmut-beta', ?, ?, 'Beta', 'beta')`,
		wsID, memMutCrew)

	f := &memMutFixture{
		t:  t,
		db: db,
		// storageDir, not t.TempDir: setupTestDB registers a cleanup that
		// drains this package's detached handler goroutines, and a directory
		// taken afterwards is torn down BEFORE that drain runs (t.Cleanup is
		// LIFO), so a straggler would find its tree already gone.
		storage: storageDir(t),
		blobs:   storageDir(t),
	}
	f.seedLiveRun(memMutRun, memMutAgent, 3, "running")

	h := NewMemoryMutationHandler(db, f.storage, f.blobs, newTestLogger())
	ih := NewInternalHandler(db, memMutMaster, newTestLogger())
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/internal/memory/mutation", ih.requireInternal(http.HandlerFunc(h.Mutate)))
	mux.Handle("GET /api/v1/internal/memory/canonical", ih.requireInternal(http.HandlerFunc(h.Read)))
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// seedLiveRun writes one work item plus its current attempt. generation is
// carried on BOTH rows because that is what "current" means: the item's
// generation is the live term and the attempt's is the term it was claimed
// under; a mismatch is a worker that lost its lease.
func (f *memMutFixture) seedLiveRun(runID, agentID string, generation int64, state string) {
	f.t.Helper()
	workID := "work-" + runID
	execOrFatal(f.t, f.db, `
		INSERT INTO work_items (id, workspace_id, source, agent_id, crew_id, state, generation,
		                        eligible_at, created_at, updated_at)
		VALUES (?, ?, 'chat', ?, ?, ?, ?, '2026-09-10T00:00:00Z', '2026-09-10T00:00:00Z', '2026-09-10T00:00:00Z')`,
		workID, memMutWS, agentID, memMutCrew, state, generation)
	execOrFatal(f.t, f.db, `
		INSERT INTO work_attempts (run_id, work_id, attempt, generation, lease_owner,
		                           lease_expires_at, heartbeat_at, started_at)
		VALUES (?, ?, 1, ?, 'dispatcher-1', '2126-09-10T00:00:00Z', '2026-09-10T00:00:00Z', '2026-09-10T00:00:00Z')`,
		runID, workID, generation)
}

func (f *memMutFixture) crewToken() string {
	return internaltoken.DeriveCrewToken(memMutMaster, memMutWS, memMutCrew)
}

// body is the happy-path request; each test overrides only the field it is
// about, which keeps its diff to the thing under test.
func memMutBody(overrides map[string]any) map[string]any {
	b := map[string]any{
		"operation_id": "op-1",
		"scope":        "agent",
		"file":         "AGENT.md",
		"op":           "append",
		"content":      "one\n",
		"run_id":       memMutRun,
		"generation":   3,
	}
	for k, v := range overrides {
		if v == nil {
			delete(b, k)
			continue
		}
		b[k] = v
	}
	return b
}

func (f *memMutFixture) post(token, slug string, body map[string]any) (int, map[string]any, string) {
	f.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatal(err)
	}
	req, err := http.NewRequest("POST", f.srv.URL+"/api/v1/internal/memory/mutation", bytes.NewReader(raw))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Internal-Token", token)
	}
	if slug != "" {
		req.Header.Set(actingAgentSlugHeader, slug)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(buf.Bytes(), &out)
	return resp.StatusCode, out, buf.String()
}

func (f *memMutFixture) get(token, slug, scope, file string) (int, map[string]any, string) {
	f.t.Helper()
	req, err := http.NewRequest("GET",
		f.srv.URL+"/api/v1/internal/memory/canonical?scope="+scope+"&file="+file, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("X-Internal-Token", token)
	}
	if slug != "" {
		req.Header.Set(actingAgentSlugHeader, slug)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(buf.Bytes(), &out)
	return resp.StatusCode, out, buf.String()
}

// mustPost is the happy path with the fixture's own token and acting slug.
func (f *memMutFixture) mustPost(body map[string]any) map[string]any {
	f.t.Helper()
	status, out, raw := f.post(f.crewToken(), memMutSlug, body)
	if status != http.StatusOK {
		f.t.Fatalf("status = %d, want 200: %s", status, raw)
	}
	return out
}

// agentFile is the HOST path the container sees at /crew/agents/<slug>/.memory.
func (f *memMutFixture) agentFile(file string) string {
	f.t.Helper()
	root, err := memory.HostAgentMemoryRoot(f.storage, memMutCrew, memMutSlug)
	if err != nil {
		f.t.Fatalf("HostAgentMemoryRoot: %v", err)
	}
	return filepath.Join(root, file)
}

func (f *memMutFixture) onDisk(file string) string {
	f.t.Helper()
	b, err := os.ReadFile(f.agentFile(file))
	if err != nil {
		f.t.Fatalf("read %s: %v", file, err)
	}
	return string(b)
}

func num(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	v, ok := m[key].(float64)
	if !ok {
		t.Fatalf("%q missing or not a number in %v", key, m)
	}
	return v
}

// ── The headline: the guaranteed profile is reachable and the revision is real

// TestInternalMemoryMutation_GuaranteedWriteReportsARealRevision is the whole
// point of the route. Before it, ProfileGuaranteed refused every caller in the
// tree: the two agent-facing writers run inside the agent container and hold no
// *sql.DB, so the profile's "a durable ledger handle" requirement could not be
// met by anybody. This asserts the write happens, the bytes land on the HOST
// side of the crew bind mount, the response carries a monotonic revision with
// ledger_recorded true, and the ledger really has the rows behind it.
func TestInternalMemoryMutation_GuaranteedWriteReportsARealRevision(t *testing.T) {
	f := newMemMutFixture(t)

	out := f.mustPost(memMutBody(nil))
	if got := out["profile"]; got != string(memory.ProfileGuaranteed) {
		t.Errorf("profile = %v, want %q", got, memory.ProfileGuaranteed)
	}
	if got := num(t, out, "revision"); got != 1 {
		t.Errorf("revision = %v, want 1 — a first contract write anchors revision 1", got)
	}
	if out["ledger_recorded"] != true {
		t.Errorf("ledger_recorded = %v, want true: a write that did not reach the ledger must never claim it did", out["ledger_recorded"])
	}
	if out["content_sha256"] == "" || out["content_sha256"] == nil {
		t.Errorf("content_sha256 empty: %v", out)
	}
	if got := f.onDisk("AGENT.md"); got != "one\n" {
		t.Errorf("on disk = %q, want %q", got, "one\n")
	}

	// The revision is not a number the handler made up: it is anchored.
	var revision int64
	var anchorSHA string
	if err := f.db.QueryRow(
		`SELECT revision, content_sha256 FROM memory_revisions WHERE workspace_id = ? AND path = ?`,
		memMutWS, "agent:alpha/AGENT.md").Scan(&revision, &anchorSHA); err != nil {
		t.Fatalf("no revision anchor row: %v", err)
	}
	if revision != 1 || anchorSHA != out["content_sha256"] {
		t.Errorf("anchor = (%d, %s), response = (%v, %v)", revision, anchorSHA, out["revision"], out["content_sha256"])
	}

	// And the mutation is recorded with the run it belonged to (I4).
	var mutRun string
	var mutGen int64
	if err := f.db.QueryRow(
		`SELECT run_id, generation FROM memory_mutations WHERE workspace_id = ? AND operation_id = ?`,
		memMutWS, "op-1").Scan(&mutRun, &mutGen); err != nil {
		t.Fatalf("no mutation row: %v", err)
	}
	if mutRun != memMutRun || mutGen != 3 {
		t.Errorf("mutation recorded run=%q gen=%d, want %q/3", mutRun, mutGen, memMutRun)
	}

	// A second append moves the revision on, monotonically.
	out2 := f.mustPost(memMutBody(map[string]any{"operation_id": "op-2", "content": "two\n"}))
	if got := num(t, out2, "revision"); got != 2 {
		t.Errorf("second revision = %v, want 2", got)
	}
	if got := f.onDisk("AGENT.md"); got != "one\ntwo\n" {
		t.Errorf("on disk = %q, want %q", got, "one\ntwo\n")
	}
}

// ── Refusal, once per requirement ────────────────────────────────────────────

// TestInternalMemoryMutation_RefusedForEachMissingRequirement walks the four
// things ProfileGuaranteed refuses without, plus the run id this route needs to
// wire Authorize at all. Each case must be REFUSED and must leave the file
// untouched — a downgrade that succeeded would be the exact failure the profile
// exists to prevent.
func TestInternalMemoryMutation_RefusedForEachMissingRequirement(t *testing.T) {
	f := newMemMutFixture(t)
	// Establish revision 1 so the replace cases below have a real base; a
	// replace expecting a revision the file never reached is a conflict, not a
	// profile failure, and would test the wrong thing.
	f.mustPost(memMutBody(nil))

	replace := func(over map[string]any) map[string]any {
		b := memMutBody(map[string]any{
			"operation_id":      "op-replace",
			"op":                "replace",
			"content":           "one\ntwo\n",
			"expected_revision": 1,
			"removals":          []memdiff.Removal{},
		})
		for k, v := range over {
			if v == nil {
				delete(b, k)
				continue
			}
			b[k] = v
		}
		return b
	}

	for _, tc := range []struct {
		name string
		body map[string]any
		want int
	}{
		{
			name: "no caller-supplied operation id",
			body: memMutBody(map[string]any{"operation_id": ""}),
			want: http.StatusBadRequest,
		},
		{
			name: "no run id, so Authorize has no run or generation to verify",
			body: memMutBody(map[string]any{"operation_id": "op-x", "run_id": nil}),
			want: http.StatusBadRequest,
		},
		{
			name: "replace with no removals declaration (nil means undeclared, not empty)",
			body: replace(map[string]any{"removals": nil}),
			want: http.StatusBadRequest,
		},
		{
			name: "replace with no expected revision, so nothing is compared and set",
			body: replace(map[string]any{"expected_revision": 0}),
			want: http.StatusBadRequest,
		},
		{
			name: "a run the acting agent does not own, so I4 cannot be satisfied",
			body: memMutBody(map[string]any{"operation_id": "op-y", "run_id": "run-that-does-not-exist"}),
			want: http.StatusForbidden,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, raw := f.post(f.crewToken(), memMutSlug, tc.body)
			if status != tc.want {
				t.Fatalf("status = %d, want %d: %s", status, tc.want, raw)
			}
			if got := f.onDisk("AGENT.md"); got != "one\n" {
				t.Errorf("a refused write changed the file: %q", got)
			}
		})
	}
}

// TestInternalMemoryMutation_AuthorizeVerifiesTheRunGeneration is the test that
// makes Authorize more than a box ticked. The guaranteed profile refuses a nil
// Authorize, so the cheap way to satisfy it is a closure returning nil — which
// would pass the gate while checking nothing. Each case here is a run that
// EXISTS and is refused anyway, which a nil-returning closure would accept.
func TestInternalMemoryMutation_AuthorizeVerifiesTheRunGeneration(t *testing.T) {
	f := newMemMutFixture(t)
	f.mustPost(memMutBody(nil))

	// A sibling's live run, in the same crew and workspace.
	f.seedLiveRun("run-beta", "agent-memmut-beta", 1, "running")
	// alpha's own attempt, but superseded: the item has moved to generation 5
	// while this attempt still carries 4.
	execOrFatal(t, f.db, `
		INSERT INTO work_items (id, workspace_id, source, agent_id, crew_id, state, generation,
		                        eligible_at, created_at, updated_at)
		VALUES ('work-stale', ?, 'chat', ?, ?, 'running', 5, '2026-09-10T00:00:00Z', '2026-09-10T00:00:00Z', '2026-09-10T00:00:00Z')`,
		memMutWS, memMutAgent, memMutCrew)
	execOrFatal(t, f.db, `
		INSERT INTO work_attempts (run_id, work_id, attempt, generation, lease_owner,
		                           lease_expires_at, heartbeat_at, started_at)
		VALUES ('run-stale', 'work-stale', 1, 4, 'dispatcher-1', '2126-09-10T00:00:00Z', '2026-09-10T00:00:00Z', '2026-09-10T00:00:00Z')`)
	// alpha's own live attempt, but the work already finished.
	f.seedLiveRun("run-done", memMutAgent, 1, "succeeded")
	// alpha's own attempt whose lease ended.
	f.seedLiveRun("run-ended", memMutAgent, 1, "running")
	execOrFatal(t, f.db, `UPDATE work_attempts SET ended_at = '2026-09-10T01:00:00Z' WHERE run_id = 'run-ended'`)

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"a sibling agent's run", memMutBody(map[string]any{"operation_id": "op-a", "run_id": "run-beta", "generation": 1})},
		{"a superseded attempt (I5's stale fencing term)", memMutBody(map[string]any{"operation_id": "op-b", "run_id": "run-stale", "generation": 4})},
		{"a run whose work item is terminal", memMutBody(map[string]any{"operation_id": "op-c", "run_id": "run-done", "generation": 1})},
		{"an attempt whose lease has ended", memMutBody(map[string]any{"operation_id": "op-d", "run_id": "run-ended", "generation": 1})},
		{"the live run, declared at the wrong generation", memMutBody(map[string]any{"operation_id": "op-e", "generation": 2})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, raw := f.post(f.crewToken(), memMutSlug, tc.body)
			if status != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", status, raw)
			}
			if got := f.onDisk("AGENT.md"); got != "one\n" {
				t.Errorf("a run that failed I4 still wrote: %q", got)
			}
		})
	}
}

// ── The §8 error vocabulary ──────────────────────────────────────────────────

// TestInternalMemoryMutation_StaleExpectedRevisionIsAMemoryConflict: the CAS is
// the reason the profile exists. A replace against a revision the file has
// moved past must be refused with the revision to re-read against, never
// resolved by overwriting.
func TestInternalMemoryMutation_StaleExpectedRevisionIsAMemoryConflict(t *testing.T) {
	f := newMemMutFixture(t)
	f.mustPost(memMutBody(nil))
	f.mustPost(memMutBody(map[string]any{"operation_id": "op-2", "content": "two\n"}))
	// The file is now at revision 2. Propose a replace diffed against 1.

	status, out, raw := f.post(f.crewToken(), memMutSlug, memMutBody(map[string]any{
		"operation_id":      "op-stale",
		"op":                "replace",
		"content":           "rewritten\n",
		"expected_revision": 1,
		"removals":          []memdiff.Removal{},
	}))
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", status, raw)
	}
	if out["code"] != "memory_conflict" {
		t.Errorf("code = %v, want memory_conflict: %s", out["code"], raw)
	}
	if got := num(t, out, "current_revision"); got != 2 {
		t.Errorf("current_revision = %v, want 2 — the client cannot re-propose without it", got)
	}
	if got := f.onDisk("AGENT.md"); got != "one\ntwo\n" {
		t.Errorf("a stale replace overwrote the file: %q", got)
	}
}

// TestInternalMemoryMutation_UndeclaredRemovalIsRefused: the diff removing a
// base line the request did not declare is §8's undeclared_removal. It is what
// stops "rewrite the whole file" from silently deleting a concurrent writer's
// lines under the cover of a correct revision.
func TestInternalMemoryMutation_UndeclaredRemovalIsRefused(t *testing.T) {
	f := newMemMutFixture(t)
	f.mustPost(memMutBody(map[string]any{"content": "one\ntwo\nthree\n"}))

	status, out, raw := f.post(f.crewToken(), memMutSlug, memMutBody(map[string]any{
		"operation_id":      "op-drop",
		"op":                "replace",
		"content":           "one\nthree\n",
		"expected_revision": 1,
		// "two" is gone and nothing says so.
		"removals": []memdiff.Removal{},
	}))
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", status, raw)
	}
	if out["code"] != "undeclared_removal" {
		t.Errorf("code = %v, want undeclared_removal: %s", out["code"], raw)
	}
	if got := f.onDisk("AGENT.md"); got != "one\ntwo\nthree\n" {
		t.Errorf("an undeclared removal was written: %q", got)
	}
}

// TestInternalMemoryMutation_IdenticalRetryReturnsTheOriginalResult is
// idempotency, and it is the requirement a synthesised operation id cannot
// meet. An agent that retries a timed-out append must not append twice.
func TestInternalMemoryMutation_IdenticalRetryReturnsTheOriginalResult(t *testing.T) {
	f := newMemMutFixture(t)
	first := f.mustPost(memMutBody(nil))
	second := f.mustPost(memMutBody(nil))

	if second["idempotent"] != true {
		t.Errorf("idempotent = %v, want true on the identical retry: %v", second["idempotent"], second)
	}
	if first["revision"] != second["revision"] || first["content_sha256"] != second["content_sha256"] {
		t.Errorf("retry returned a different result:\n first = %v\nsecond = %v", first, second)
	}
	if got := f.onDisk("AGENT.md"); got != "one\n" {
		t.Errorf("the retry appended a second time: %q", got)
	}

	// A DIFFERENT request under the same id is a client bug, and answering it
	// would make the id meaningless.
	status, out, raw := f.post(f.crewToken(), memMutSlug, memMutBody(map[string]any{"content": "different\n"}))
	if status != http.StatusConflict || out["code"] != "operation_conflict" {
		t.Fatalf("status = %d code = %v, want 409 operation_conflict: %s", status, out["code"], raw)
	}
}

// ── Authentication and identity ──────────────────────────────────────────────

// TestInternalMemoryMutation_RefusesUnauthenticatedAndWrongAgent. The route
// takes the SIDECAR's identity from the token and narrows it to one agent with
// X-Acting-Agent-Slug; the header can only ever narrow the token's authority,
// never widen it. Every way of failing that must be a refusal, and none of them
// may touch the file.
func TestInternalMemoryMutation_RefusesUnauthenticatedAndWrongAgent(t *testing.T) {
	f := newMemMutFixture(t)
	f.mustPost(memMutBody(nil))

	execOrFatal(t, f.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws-memmut-other', 'Other', 'other-memmut')`)
	execOrFatal(t, f.db, `INSERT INTO agents (id, workspace_id, name, slug) VALUES ('agent-memmut-foreign', 'ws-memmut-other', 'Foreign', 'foreign')`)
	seedCrewRow(t, f.db, "crew-memmut-2", memMutWS, "MemMut Two", "memmut-two")

	for _, tc := range []struct {
		name  string
		token string
		slug  string
	}{
		{"no internal token at all", "", memMutSlug},
		{"a forged internal token", "crwv1.test-workspace-id.crew-memmut.deadbeef", memMutSlug},
		{"the unbound master token, which has no workspace to resolve the slug inside", memMutMaster, memMutSlug},
		{"no acting agent header", internaltoken.DeriveCrewToken(memMutMaster, memMutWS, memMutCrew), ""},
		{"a slug that names no agent", internaltoken.DeriveCrewToken(memMutMaster, memMutWS, memMutCrew), "nobody"},
		{"an agent in a sibling crew the token is not bound to", internaltoken.DeriveCrewToken(memMutMaster, memMutWS, "crew-memmut-2"), memMutSlug},
		{"an agent in a foreign workspace", internaltoken.DeriveCrewToken(memMutMaster, memMutWS, memMutCrew), "foreign"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, raw := f.post(tc.token, tc.slug, memMutBody(map[string]any{
				"operation_id": "op-forbidden",
				"content":      "should never land\n",
			}))
			if status == http.StatusOK {
				t.Fatalf("status = 200, want a refusal: %s", raw)
			}
			if status < 400 {
				t.Fatalf("status = %d, want a 4xx refusal: %s", status, raw)
			}
			if got := f.onDisk("AGENT.md"); got != "one\n" {
				t.Errorf("an unauthenticated or misattributed write landed: %q", got)
			}
		})
	}
}

// ── The read half of the contract ────────────────────────────────────────────

// TestInternalMemoryCanonicalRead_ClosesTheGuaranteedLoop. A client cannot
// supply expected_revision without having been told the revision, and the
// anchor lives in the host database — the sidecar's own GET /memory/read has no
// ledger and reports 0. Without this route the guaranteed profile would be
// reachable in principle and unusable in practice, so this asserts the exact
// round trip: read the revision, replace against it, succeed.
func TestInternalMemoryCanonicalRead_ClosesTheGuaranteedLoop(t *testing.T) {
	f := newMemMutFixture(t)

	// Before any write: the key exists as a concept, nothing is anchored.
	status, out, raw := f.get(f.crewToken(), memMutSlug, "agent", "AGENT.md")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", status, raw)
	}
	if out["exists"] != false || num(t, out, "revision") != 0 {
		t.Errorf("empty key should read exists=false revision=0, got %v", out)
	}
	if out["ledger_recorded"] != true {
		t.Errorf("ledger_recorded = %v, want true — the HOST read always has the ledger", out["ledger_recorded"])
	}

	f.mustPost(memMutBody(map[string]any{"content": "one\ntwo\n"}))

	status, out, raw = f.get(f.crewToken(), memMutSlug, "agent", "AGENT.md")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", status, raw)
	}
	if got := num(t, out, "revision"); got != 1 {
		t.Fatalf("revision = %v, want 1: %s", got, raw)
	}
	if out["content"] != "one\ntwo\n" {
		t.Errorf("content = %v, want the canonical file", out["content"])
	}
	if out["drift"] != false {
		t.Errorf("drift = %v, want false right after a contract write", out["drift"])
	}
	if prov, ok := out["provenance"].(map[string]any); !ok || prov["run_id"] != memMutRun {
		t.Errorf("provenance does not name the run that wrote it: %v", out["provenance"])
	}

	// The loop: the revision this read reported is the one the replace CASes
	// against, and it is accepted.
	replaced := f.mustPost(memMutBody(map[string]any{
		"operation_id":      "op-loop",
		"op":                "replace",
		"content":           "one\ntwo\nthree\n",
		"expected_revision": int64(num(t, out, "revision")),
		"removals":          []memdiff.Removal{},
	}))
	if got := num(t, replaced, "revision"); got != 2 {
		t.Errorf("revision after the replace = %v, want 2", got)
	}
	if replaced["removals_verified"] != true {
		t.Errorf("removals_verified = %v, want true — the declaration was checked", replaced["removals_verified"])
	}
}

// TestInternalMemoryCanonicalRead_RefusesTheSameWayTheWriteDoes: a read surface
// wider than the write surface is how an agent reads a sibling's private tier.
func TestInternalMemoryCanonicalRead_RefusesTheSameWayTheWriteDoes(t *testing.T) {
	f := newMemMutFixture(t)
	for _, tc := range []struct{ name, token, slug, scope, file string }{
		{"no token", "", memMutSlug, "agent", "AGENT.md"},
		{"no acting slug", f.crewToken(), "", "agent", "AGENT.md"},
		{"unknown slug", f.crewToken(), "nobody", "agent", "AGENT.md"},
		{"unsupported file", f.crewToken(), memMutSlug, "agent", "secrets.env"},
		{"traversal", f.crewToken(), memMutSlug, "agent", "..%2F..%2Fetc%2Fpasswd"},
		{"bad scope", f.crewToken(), memMutSlug, "workspace", "AGENT.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, raw := f.get(tc.token, tc.slug, tc.scope, tc.file)
			if status < 400 {
				t.Fatalf("status = %d, want a refusal: %s", status, raw)
			}
		})
	}
}

// TestInternalMemoryMutation_FailsClosedWithoutAHostRoot pins #1663's lesson on
// this route: without the storage base path the host cannot resolve the crew's
// bind-mount source, and a best-effort write would land at the host filesystem
// root where no container will ever read it. It must refuse instead.
func TestInternalMemoryMutation_FailsClosedWithoutAHostRoot(t *testing.T) {
	f := newMemMutFixture(t)
	h := NewMemoryMutationHandler(f.db, "", f.blobs, newTestLogger())
	ih := NewInternalHandler(f.db, memMutMaster, newTestLogger())
	srv := httptest.NewServer(ih.requireInternal(http.HandlerFunc(h.Mutate)))
	defer srv.Close()

	raw, _ := json.Marshal(memMutBody(nil))
	req, err := http.NewRequest("POST", srv.URL+"/api/v1/internal/memory/mutation", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Internal-Token", f.crewToken())
	req.Header.Set(actingAgentSlugHeader, memMutSlug)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		t.Fatalf("status = %d, want 503: %s", resp.StatusCode, buf.String())
	}
}

// TestMemoryMutationFileCap_MatchesTheAdvertisedAllowlist keeps this package's
// copy of the allowlist honest. It cannot import internal/sidecar's table (that
// edge would be a cycle), so the shapes are pinned here and the byte ceilings
// are asserted to be the ones the sidecar advertises.
func TestMemoryMutationFileCap_MatchesTheAdvertisedAllowlist(t *testing.T) {
	for _, tc := range []struct {
		file  string
		want  int
		known bool
	}{
		{"AGENT.md", 4000, true},
		{"CREW.md", 4000, true},
		{"pins.md", 8000, true},
		{"daily/2026-09-10.md", memoryMutationDailyCap, true},
		{"daily/nested/x.md", 0, false},
		{"daily/token", 0, false},
		{"nested/AGENT.md", 0, false},
		{"../AGENT.md", 0, false},
		{"secrets.env", 0, false},
	} {
		t.Run(tc.file, func(t *testing.T) {
			got, known := memoryMutationFileCap(tc.file)
			if known != tc.known || got != tc.want {
				t.Errorf("memoryMutationFileCap(%q) = (%d, %v), want (%d, %v)", tc.file, got, known, tc.want, tc.known)
			}
		})
	}
}

func TestInternalMemoryMutation_RefusesSymlinkedParent(t *testing.T) {
	for _, part := range []string{"daily", ".memory", "agent"} {
		t.Run(part, func(t *testing.T) {
			f := newMemMutFixture(t)
			outside := t.TempDir()
			link := f.agentFile("daily")
			destination := filepath.Join(outside, "2026-09-11.md")
			switch part {
			case ".memory":
				link = filepath.Dir(f.agentFile("AGENT.md"))
				destination = filepath.Join(outside, "daily", "2026-09-11.md")
			case "agent":
				link = filepath.Dir(filepath.Dir(f.agentFile("AGENT.md")))
				destination = filepath.Join(outside, ".memory", "daily", "2026-09-11.md")
			}
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(destination, []byte("outside-private\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			status, _, raw := f.get(f.crewToken(), memMutSlug, "agent", "daily/2026-09-11.md")
			if status < 400 || strings.Contains(raw, "outside-private") {
				t.Errorf("read followed symlink: HTTP %d: %s", status, raw)
			}
			status, _, raw = f.post(f.crewToken(), memMutSlug, memMutBody(map[string]any{"file": "daily/2026-09-11.md"}))
			if status < 400 {
				t.Errorf("write followed symlink: HTTP %d: %s", status, raw)
			}
			b, err := os.ReadFile(destination)
			if err != nil || string(b) != "outside-private\n" {
				t.Fatalf("host file outside storage changed to %q: %v", b, err)
			}
			if _, err := os.Lstat(destination + ".lock"); !os.IsNotExist(err) {
				t.Fatalf("outside lock created: %v", err)
			}
		})
	}
}
