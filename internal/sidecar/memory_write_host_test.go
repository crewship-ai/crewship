package sidecar

// The host forward on POST /memory/write, and the DECLARED fallback around it.
//
// The interesting property is not "the sidecar can make an HTTP request". It is
// that exactly one of three things happens to every write and the response says
// which: the host performed it under the guaranteed profile, the host refused
// it (and the refusal is relayed rather than retried locally, which would
// perform the lost update the CAS just prevented), or the host was never
// reached and the in-container legacy path served it while declaring that it
// did.

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/memory/memdiff"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// hostStubCall records one request the sidecar made to the stub host.
type hostStubCall struct {
	capability string
	method     string
	path       string
	token      string
	slug       string
	body       map[string]any
}

type hostStub struct {
	srv   *httptest.Server
	mu    sync.Mutex // audit requests can arrive after the write response
	calls []hostStubCall
	// mutation and canonical are the canned answers; either may be nil, in
	// which case the stub 404s that route (an older host).
	mutation  func() (int, string)
	canonical func() (int, string)
}

func newHostStub(t *testing.T) *hostStub {
	t.Helper()
	h := &hostStub{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := hostStubCall{
			method:     r.Method,
			capability: r.Header.Get("Authorization"),
			path:       r.URL.Path,
			token:      r.Header.Get("X-Internal-Token"),
			slug:       r.Header.Get(actingAgentSlugHeader),
			body:       map[string]any{},
		}
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &call.body)
		}
		h.mu.Lock()
		h.calls = append(h.calls, call)
		h.mu.Unlock()

		var fn func() (int, string)
		switch r.URL.Path {
		case memoryHostMutationPath:
			fn = h.mutation
		case memoryHostCanonicalPath:
			fn = h.canonical
		}
		if fn == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"Not Found"}`))
			return
		}
		status, body := fn()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hostStub) called(path string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, c := range h.calls {
		if c.path == path {
			n++
		}
	}
	return n
}

// lastCall selects the route under the same lock as the recorder. An
// asynchronous audit request may follow a mutation, so the last HTTP request
// overall is not necessarily the mutation whose identity we want to assert.
func (h *hostStub) lastCall(t *testing.T, path string) hostStubCall {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.calls) - 1; i >= 0; i-- {
		if h.calls[i].path == path {
			return h.calls[i]
		}
	}
	t.Fatalf("no host request for %s", path)
	return hostStubCall{}
}

// newHostWriteServer is newWriteTestServer plus an IPC config pointed at the
// stub, which is what turns the host path on.
func newHostWriteServer(t *testing.T, baseURL string) (*Server, string) {
	t.Helper()
	base := t.TempDir()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := memory.New(base, memory.DefaultConfig())
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	ex := newMemoryExecutor(silent)
	t.Cleanup(func() { ex.Close(time.Second) })
	return &Server{
		memoryEngine:    eng,
		agentMemoryBase: base,
		scrubber:        scrubber.New(),
		logger:          silent,
		memoryExec:      ex,
		ipc: &IPCConfig{
			BaseURL: baseURL,
			Token:   "internal-token-for-test",
			// No per-agent bearer token is provisioned in this fixture, so
			// hybridActingSlug resolves the boot identity — the only possible
			// acting identity on such a deployment.
			AgentSlug:   "alpha",
			AgentID:     "agent-alpha",
			CrewID:      "crew-1",
			WorkspaceID: "ws-1",
		},
	}, base
}

func postWrite(t *testing.T, s *Server, body MemoryWriteRequest) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "http://localhost/memory/write", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	s.handleMemoryWrite(rr, req)
	return rr
}

func decodeWrite(t *testing.T, rr *httptest.ResponseRecorder) MemoryWriteResponse {
	t.Helper()
	var out MemoryWriteResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode write response: %v (body=%s)", err, rr.Body.String())
	}
	return out
}

// TestHandleMemoryWrite_ForwardsToHostUnderTheGuaranteedProfile is the reason
// the whole forward exists: with a host reachable and a run to fence against,
// the write leaves the container and comes back with a revision that means
// something.
func TestHandleMemoryWrite_ForwardsToHostUnderTheGuaranteedProfile(t *testing.T) {
	stub := newHostStub(t)
	stub.mutation = func() (int, string) {
		return http.StatusOK, `{"profile":"guaranteed","revision":7,"content_sha256":"abc123",
			"ledger_recorded":true,"bytes_written":4,"mutation_id":"mut-1","base_revision":6}`
	}
	s, base := newHostWriteServer(t, stub.srv.URL)

	rr := postWrite(t, s, MemoryWriteRequest{
		File:        "AGENT.md",
		Content:     "one\n",
		Mode:        "append",
		OperationID: "op-1",
		RunID:       "run-1",
		Generation:  3,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	out := decodeWrite(t, rr)
	if out.Profile != string(memory.ProfileGuaranteed) {
		t.Errorf("profile = %q, want guaranteed", out.Profile)
	}
	if out.Revision != 7 || !out.RevisionChecked {
		t.Errorf("revision = %d checked = %v, want 7/true", out.Revision, out.RevisionChecked)
	}
	if out.Degraded || out.DegradedReason != "" {
		t.Errorf("a write that reached the ledger must not report a downgrade: %+v", out)
	}
	if out.MutationID != "mut-1" || out.BaseRevision != 6 {
		t.Errorf("ledger record not surfaced: %+v", out)
	}

	if got := stub.called(memoryHostMutationPath); got != 1 {
		t.Fatalf("host mutation calls = %d, want 1", got)
	}
	call := stub.lastCall(t, memoryHostMutationPath)
	if call.token != "internal-token-for-test" {
		t.Errorf("X-Internal-Token = %q", call.token)
	}
	if call.slug != "alpha" {
		t.Errorf("X-Acting-Agent-Slug = %q, want alpha — the host must be able to attribute the write", call.slug)
	}
	if call.body["operation_id"] != "op-1" || call.body["run_id"] != "run-1" || call.body["generation"] != float64(3) {
		t.Errorf("fencing pair / operation id not forwarded: %v", call.body)
	}
	if call.body["op"] != "append" {
		t.Errorf("op = %v, want append", call.body["op"])
	}

	// The host owns the bytes on this path: nothing was written in-container,
	// which is the point — two writers of one file is what §8 removes.
	if _, err := os.Stat(filepath.Join(base, "AGENT.md")); !os.IsNotExist(err) {
		t.Errorf("the guaranteed path also wrote locally; the host is the single writer")
	}
}

// TestHandleMemoryWrite_DeclaresTheFallbackWhenTheHostIsUnreachable. The
// fallback is allowed; a SILENT one is not. The write still happens (refusing
// would discard content the agent cannot get back) and the response says
// plainly that it was not revision-checked.
func TestHandleMemoryWrite_DeclaresTheFallbackWhenTheHostIsUnreachable(t *testing.T) {
	stub := newHostStub(t)
	stub.srv.Close() // nothing is listening any more
	s, base := newHostWriteServer(t, stub.srv.URL)

	rr := postWrite(t, s, MemoryWriteRequest{
		File: "AGENT.md", Content: "one\n", Mode: "append",
		OperationID: "op-1", RunID: "run-1", Generation: 3,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	out := decodeWrite(t, rr)
	if out.Profile != string(memory.ProfileLegacy) {
		t.Errorf("profile = %q, want legacy", out.Profile)
	}
	if out.RevisionChecked || out.Revision != 0 {
		t.Errorf("an unreachable ledger must not report a checked revision: %+v", out)
	}
	if !out.Degraded || out.DegradedReason == "" {
		t.Fatalf("the downgrade was not declared: %+v", out)
	}
	if !strings.Contains(out.DegradedReason, "unreachable") {
		t.Errorf("degraded_reason does not name what stopped it: %q", out.DegradedReason)
	}
	got, err := os.ReadFile(filepath.Join(base, "AGENT.md"))
	if err != nil || string(got) != "one\n" {
		t.Errorf("the legacy path did not write: %q %v", got, err)
	}
}

// TestHandleMemoryWrite_RequireGuaranteedRefusesRatherThanDowngrades makes the
// downgrade the CALLER's decision. A caller that would rather lose the write
// than lose the guarantee says so and gets a 503 with the reason.
func TestHandleMemoryWrite_RequireGuaranteedRefusesRatherThanDowngrades(t *testing.T) {
	stub := newHostStub(t)
	stub.srv.Close()
	s, base := newHostWriteServer(t, stub.srv.URL)

	rr := postWrite(t, s, MemoryWriteRequest{
		File: "AGENT.md", Content: "one\n", Mode: "append",
		OperationID: "op-1", RunID: "run-1", Generation: 3,
		RequireGuaranteed: true,
	})
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "memory_profile_unmet") {
		t.Errorf("refusal does not carry the profile code: %s", rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(base, "AGENT.md")); !os.IsNotExist(err) {
		t.Errorf("require_guaranteed must not fall through to the legacy write")
	}
}

// TestHandleMemoryWrite_RelaysAHostRefusalInsteadOfRetryingLocally is the case
// a naive fallback gets wrong. A 409 from the host is the CAS working; serving
// the same write locally under the legacy profile would perform exactly the
// lost update the conflict just prevented.
func TestHandleMemoryWrite_RelaysAHostRefusalInsteadOfRetryingLocally(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"a revision conflict", http.StatusConflict, `{"error":"memory_conflict","code":"memory_conflict","current_revision":9}`},
		{"an undeclared removal", http.StatusConflict, `{"error":"undeclared_removal","code":"undeclared_removal","current_revision":9}`},
		{"a policy rejection", http.StatusUnprocessableEntity, `{"rejected":true,"kind":"cap"}`},
		{"a refused run generation", http.StatusForbidden, `{"error":"run is not the current live attempt"}`},
		{"a host failure, which may or may not have written", http.StatusInternalServerError, `{"error":"memory mutation failed"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := newHostStub(t)
			stub.mutation = func() (int, string) { return tc.status, tc.body }
			s, base := newHostWriteServer(t, stub.srv.URL)

			rr := postWrite(t, s, MemoryWriteRequest{
				File: "AGENT.md", Content: "one\n", Mode: "append",
				OperationID: "op-1", RunID: "run-1", Generation: 3,
			})
			if rr.Code != tc.status {
				t.Fatalf("status = %d, want %d relayed: %s", rr.Code, tc.status, rr.Body.String())
			}
			if strings.TrimSpace(rr.Body.String()) != tc.body {
				t.Errorf("body was not relayed verbatim:\n got %s\nwant %s", rr.Body.String(), tc.body)
			}
			if _, err := os.Stat(filepath.Join(base, "AGENT.md")); !os.IsNotExist(err) {
				t.Errorf("a host refusal was retried under the legacy profile — that is the lost update the CAS prevented")
			}
		})
	}
}

// TestHandleMemoryWrite_DerivesExpectedRevisionFromTheHostRead closes the loop
// the guaranteed profile needs: a client cannot supply expected_revision
// without having been told the revision, and the anchor lives in the host
// database. The caller passes the hash it read; this handler asks the host
// which revision that hash IS.
func TestHandleMemoryWrite_DerivesExpectedRevisionFromTheHostRead(t *testing.T) {
	stub := newHostStub(t)
	stub.canonical = func() (int, string) {
		return http.StatusOK, `{"exists":true,"content_sha256":"sha-of-base","revision":4,"ledger_recorded":true}`
	}
	stub.mutation = func() (int, string) {
		return http.StatusOK, `{"profile":"guaranteed","revision":5,"content_sha256":"sha-new","ledger_recorded":true,"bytes_written":8}`
	}
	s, _ := newHostWriteServer(t, stub.srv.URL)

	rr := postWrite(t, s, MemoryWriteRequest{
		File: "AGENT.md", Content: "one\ntwo\n", Mode: "replace",
		OperationID: "op-1", RunID: "run-1", Generation: 3,
		ExpectedSHA256: "sha-of-base",
		Removals:       []memdiff.Removal{},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	if out := decodeWrite(t, rr); out.Revision != 5 || !out.RevisionChecked {
		t.Errorf("revision = %d checked = %v, want 5/true", out.Revision, out.RevisionChecked)
	}
	if got := stub.called(memoryHostCanonicalPath); got != 1 {
		t.Fatalf("canonical reads = %d, want 1", got)
	}
	mut := stub.lastCall(t, memoryHostMutationPath)
	if mut.path != memoryHostMutationPath {
		t.Fatalf("last call = %s, want the mutation", mut.path)
	}
	if mut.body["expected_revision"] != float64(4) {
		t.Errorf("expected_revision = %v, want 4 — the revision the read reported", mut.body["expected_revision"])
	}
}

// TestHandleMemoryWrite_ExpectedSHAMismatchIsAConflictBeforeAnyWrite: if the
// file moved on between the caller's read and now, the hashes disagree and this
// is a conflict here, one round trip before the ledger would say the same.
func TestHandleMemoryWrite_ExpectedSHAMismatchIsAConflictBeforeAnyWrite(t *testing.T) {
	stub := newHostStub(t)
	stub.canonical = func() (int, string) {
		return http.StatusOK, `{"exists":true,"content_sha256":"sha-someone-else-wrote","revision":9,"ledger_recorded":true}`
	}
	stub.mutation = func() (int, string) {
		return http.StatusOK, `{"profile":"guaranteed","revision":10,"ledger_recorded":true}`
	}
	s, base := newHostWriteServer(t, stub.srv.URL)

	rr := postWrite(t, s, MemoryWriteRequest{
		File: "AGENT.md", Content: "rewritten\n", Mode: "replace",
		OperationID: "op-1", RunID: "run-1", Generation: 3,
		ExpectedSHA256: "sha-the-caller-read",
		Removals:       []memdiff.Removal{},
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rr.Code, rr.Body.String())
	}
	var conflict MemoryMutationConflictEnvelope
	if err := json.Unmarshal(rr.Body.Bytes(), &conflict); err != nil {
		t.Fatalf("decode conflict: %v", err)
	}
	if conflict.Code != "memory_conflict" || conflict.CurrentRevision != 9 {
		t.Errorf("conflict = %+v, want memory_conflict at revision 9", conflict)
	}
	if stub.called(memoryHostMutationPath) != 0 {
		t.Errorf("the mutation was attempted despite a base the caller never read")
	}
	if _, err := os.Stat(filepath.Join(base, "AGENT.md")); !os.IsNotExist(err) {
		t.Errorf("a conflicting replace was written locally")
	}
}

// TestHandleMemoryWrite_StatesWhyTheHostPathWasNotTaken. Every precondition the
// forward cannot meet is a named reason on the response, and none of them costs
// a round trip to discover.
func TestHandleMemoryWrite_StatesWhyTheHostPathWasNotTaken(t *testing.T) {
	for _, tc := range []struct {
		name    string
		req     MemoryWriteRequest
		wantHas string
	}{
		{
			name:    "no run id, so I4 cannot be verified",
			req:     MemoryWriteRequest{File: "AGENT.md", Content: "x\n", Mode: "append", OperationID: "op-1"},
			wantHas: "run id",
		},
		{
			name: "a replace with no removals declaration",
			req: MemoryWriteRequest{File: "AGENT.md", Content: "x\n", Mode: "replace", OperationID: "op-1",
				RunID: "run-1", Generation: 3, ExpectedRevision: 2},
			wantHas: "removals",
		},
		{
			name: "a replace with nothing to compare and set against",
			req: MemoryWriteRequest{File: "AGENT.md", Content: "x\n", Mode: "replace", OperationID: "op-1",
				RunID: "run-1", Generation: 3, Removals: []memdiff.Removal{}},
			wantHas: "expected_revision",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := newHostStub(t)
			stub.mutation = func() (int, string) { return http.StatusOK, `{"profile":"guaranteed","revision":1}` }
			s, _ := newHostWriteServer(t, stub.srv.URL)

			rr := postWrite(t, s, tc.req)
			if rr.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
			}
			out := decodeWrite(t, rr)
			if !out.Degraded || !strings.Contains(out.DegradedReason, tc.wantHas) {
				t.Fatalf("degraded_reason = %q, want it to name %q", out.DegradedReason, tc.wantHas)
			}
			if out.Profile != string(memory.ProfileLegacy) || out.RevisionChecked {
				t.Errorf("a write that never reached the host claimed the guaranteed profile: %+v", out)
			}
			if stub.called(memoryHostMutationPath) != 0 {
				t.Errorf("a request that cannot qualify still paid a round trip")
			}
		})
	}
}

// TestHandleMemoryWrite_ScreensWithTheCrewScrubberBeforeForwarding. The host
// runs its own default scrubber, but only this process has the crew's
// credential literals registered — so skipping this would make the guaranteed
// path the weaker of the two on the one check that is per-crew.
func TestHandleMemoryWrite_ScreensWithTheCrewScrubberBeforeForwarding(t *testing.T) {
	stub := newHostStub(t)
	stub.mutation = func() (int, string) { return http.StatusOK, `{"profile":"guaranteed","revision":1}` }
	s, _ := newHostWriteServer(t, stub.srv.URL)

	rr := postWrite(t, s, MemoryWriteRequest{
		File: "AGENT.md", Content: "key sk-ant-api03-abcdefghijklmnopqrst", Mode: "append",
		OperationID: "op-1", RunID: "run-1", Generation: 3,
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rr.Code, rr.Body.String())
	}
	if stub.called(memoryHostMutationPath) != 0 {
		t.Errorf("a credential was forwarded to the host before being screened")
	}
}

func TestMemoryHostRequest_ForwardsAuthenticatedRunCapability(t *testing.T) {
	stub := newHostStub(t)
	stub.mutation = func() (int, string) { return http.StatusForbidden, `{"error":"host refused"}` }
	s, _ := newHostWriteServer(t, stub.srv.URL)
	s.runs = newRunRegistry()
	s.ipc.WorkspaceID = "ws-cap"
	s.ipc.AgentRunKey = internaltoken.DeriveAgentRunKey("test-master", "ws-cap", "crew-cap")
	token := internaltoken.DeriveAgentRunToken(s.ipc.AgentRunKey, "ws-cap", s.ipc.AgentID, "run-cap")
	req := httptest.NewRequest(http.MethodPost, "/memory/write", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	_, err := s.memoryHostRequest(req, http.MethodPost, memoryHostMutationPath, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := stub.lastCall(t, memoryHostMutationPath).capability; got != "Bearer "+token {
		t.Fatal("host did not receive the caller's exact capability")
	}
}
