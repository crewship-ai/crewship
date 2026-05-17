package sidecar

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// newJournalTestServer returns a Server with a silent logger and an
// IPC config pointed at the provided test backend. baseURL is allowed
// to be empty so callers can exercise the "IPC unconfigured" guard.
//
// The helper intentionally takes a *fully* populated IPCConfig because
// emitJournal short-circuits on missing WorkspaceID — the "configured
// but empty workspace" case is a deliberate test scenario, not a
// helper concern.
func newJournalTestServer(baseURL string) *Server {
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &Server{
		logger: silent,
		ipc: &IPCConfig{
			BaseURL:     baseURL,
			Token:       "test-token-abc",
			AgentID:     "agt_1",
			AgentSlug:   "viktor",
			CrewID:      "crw_1",
			WorkspaceID: "wks_1",
		},
	}
}

// TestSidecar_EmitJournal_HappyPath_RoundtripsToBackend verifies that
// emitJournal serialises the request, attaches X-Internal-Token, and
// hits the canonical /api/v1/internal/journal/emit path on crewshipd.
// Regression guard for the wire format: if the JSON field names drift
// from the crewshipd struct (internal/api/internal_journal.go), this
// test catches it before it ships.
func TestSidecar_EmitJournal_HappyPath_RoundtripsToBackend(t *testing.T) {
	var (
		gotPath   string
		gotToken  string
		gotType   string
		gotCtype  string
		gotReq    journalEmitRequest
		hitServer = make(chan struct{}, 1)
	)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Internal-Token")
		gotCtype = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		gotType = gotReq.Type
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"jrn_1"}`))
		hitServer <- struct{}{}
	}))
	defer backend.Close()

	s := newJournalTestServer(backend.URL)
	s.emitJournal(context.Background(), "memory.updated", "wrote AGENT.md",
		map[string]any{"bytes_written": 42}, nil)

	select {
	case <-hitServer:
	case <-time.After(2 * time.Second):
		t.Fatal("backend never received journal emit")
	}

	if gotPath != "/api/v1/internal/journal/emit" {
		t.Errorf("path = %q, want /api/v1/internal/journal/emit", gotPath)
	}
	if gotToken != "test-token-abc" {
		t.Errorf("X-Internal-Token = %q, want test-token-abc", gotToken)
	}
	if gotCtype != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCtype)
	}
	if gotType != "memory.updated" {
		t.Errorf("type = %q, want memory.updated", gotType)
	}
	if gotReq.WorkspaceID != "wks_1" || gotReq.AgentID != "agt_1" || gotReq.CrewID != "crw_1" {
		t.Errorf("scope IDs not propagated: %+v", gotReq)
	}
	if gotReq.Summary != "wrote AGENT.md" {
		t.Errorf("summary = %q, want 'wrote AGENT.md'", gotReq.Summary)
	}
	if v, _ := gotReq.Payload["bytes_written"].(float64); v != 42 {
		t.Errorf("payload bytes_written = %v, want 42", gotReq.Payload["bytes_written"])
	}
}

// TestSidecar_EmitJournal_NilIPC_NoBackendHit guards the fast-path drop
// when the sidecar boots without an IPCConfig. Calling emitJournal in
// this state must not panic and must not attempt any HTTP traffic.
// (Pre-IPC sidecars exist in unit tests and in the rare local-only run
// mode where there's no crewshipd to talk to.)
func TestSidecar_EmitJournal_NilIPC_NoBackendHit(t *testing.T) {
	var hits int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &Server{logger: silent, ipc: nil}

	// Should not panic, should not contact the (still-running) backend.
	s.emitJournal(context.Background(), "anything", "x", nil, nil)

	// Give any (incorrect) goroutine time to land.
	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("backend hit %d times, want 0 — emit should drop when IPC nil", n)
	}
}

// TestSidecar_EmitJournal_EmptyWorkspaceID_DroppedSilently covers the
// partially-populated IPCConfig guard. If WorkspaceID is empty, the
// emit would be rejected by crewshipd anyway (workspace is the auth
// boundary for journal rows), so the sidecar avoids the round-trip
// rather than logging a 4xx per emit. Regression: an earlier version
// of this code path sent the request and silently dropped on 400,
// generating noise in journalctl.
func TestSidecar_EmitJournal_EmptyWorkspaceID_DroppedSilently(t *testing.T) {
	var hits int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	s := newJournalTestServer(backend.URL)
	s.ipc.WorkspaceID = "" // crucial: partial IPC

	s.emitJournal(context.Background(), "memory.updated", "x", nil, nil)

	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("backend hit %d times, want 0 — emit should drop on empty WorkspaceID", n)
	}
}

// TestSidecar_EmitJournal_BackendUnreachable_NoPanic verifies the
// fire-and-forget semantics: a closed backend (think: crewshipd
// restarting under us) must NOT cause emitJournal to panic or return
// an error to the caller. Observability code on the proxy hot path
// must never block agent traffic.
func TestSidecar_EmitJournal_BackendUnreachable_NoPanic(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// Close immediately so the URL is dead.
	url := backend.URL
	backend.Close()

	s := newJournalTestServer(url)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("emitJournal panicked on dead backend: %v", r)
		}
	}()
	s.emitJournal(context.Background(), "network.egress", "GET example.com", nil, nil)
}

// TestSidecar_EmitJournal_Backend500_NoPanic ensures non-2xx responses
// don't break the caller. crewshipd may return 401 (token drift), 422
// (schema regression), or 500 (panic) and the sidecar must continue
// to function — the journal entry is simply dropped.
func TestSidecar_EmitJournal_Backend500_NoPanic(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer backend.Close()

	s := newJournalTestServer(backend.URL)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("emitJournal panicked on backend 500: %v", r)
		}
	}()
	s.emitJournal(context.Background(), "memory.updated", "x", nil, nil)
}

// TestSidecar_PostCostRecord_HappyPath_RoundtripsToBackend exercises
// the full payload — every numeric channel populated + a non-default
// billing mode. Regression guard for the wire format declared in
// internal/api/internal_cost.go.
func TestSidecar_PostCostRecord_HappyPath_RoundtripsToBackend(t *testing.T) {
	var (
		gotPath  string
		gotToken string
		gotRec   sidecarCostRecord
		done     = make(chan struct{}, 1)
	)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Internal-Token")
		_ = json.NewDecoder(r.Body).Decode(&gotRec)
		w.WriteHeader(http.StatusOK)
		done <- struct{}{}
	}))
	defer backend.Close()

	s := newJournalTestServer(backend.URL)
	usage := LLMUsage{
		Provider:            "anthropic",
		Model:               "claude-opus-4-7",
		InputTokens:         1000,
		OutputTokens:        500,
		CachedInputTokens:   200,
		CacheCreationTokens: 50,
	}
	quota := QuotaInfo{RemainingPct: 0.42, Window: "tokens", HadStatus429: false}

	s.postCostRecord(context.Background(), usage, quota, "metered", "team")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("backend never received cost record")
	}

	if gotPath != "/api/v1/internal/cost/record" {
		t.Errorf("path = %q, want /api/v1/internal/cost/record", gotPath)
	}
	if gotToken != "test-token-abc" {
		t.Errorf("X-Internal-Token = %q, want test-token-abc", gotToken)
	}
	if gotRec.Provider != "anthropic" || gotRec.Model != "claude-opus-4-7" {
		t.Errorf("provider/model not propagated: %+v", gotRec)
	}
	if gotRec.InputTokens != 1000 || gotRec.OutputTokens != 500 ||
		gotRec.CachedInputTokens != 200 || gotRec.CacheCreationTokens != 50 {
		t.Errorf("token counts not propagated: %+v", gotRec)
	}
	if gotRec.BillingMode != "metered" || gotRec.SubscriptionPlan != "team" {
		t.Errorf("billing mode/plan not propagated: mode=%q plan=%q",
			gotRec.BillingMode, gotRec.SubscriptionPlan)
	}
	if gotRec.QuotaRemainingPct != 0.42 || gotRec.QuotaWindow != "tokens" {
		t.Errorf("quota not propagated: %+v", gotRec)
	}
}

// TestSidecar_PostCostRecord_EmptyProvider_Skipped guards the
// "nothing to attribute" early return. Without a provider tag the row
// has no useful column to filter on; writing it would just pollute
// the ledger with NULL-provider records.
func TestSidecar_PostCostRecord_EmptyProvider_Skipped(t *testing.T) {
	var hits int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	s := newJournalTestServer(backend.URL)
	usage := LLMUsage{Provider: "", InputTokens: 100} // no provider!
	s.postCostRecord(context.Background(), usage, QuotaInfo{}, "metered", "")

	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("backend hit %d times, want 0 — empty provider must skip", n)
	}
}

// TestSidecar_PostCostRecord_BackendUnreachable_NoPanic mirrors the
// emitJournal counterpart: a dead backend must not panic the caller.
func TestSidecar_PostCostRecord_BackendUnreachable_NoPanic(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := backend.URL
	backend.Close()

	s := newJournalTestServer(url)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("postCostRecord panicked on dead backend: %v", r)
		}
	}()
	s.postCostRecord(context.Background(), LLMUsage{Provider: "anthropic"},
		QuotaInfo{}, "metered", "")
}

// TestSidecar_BuildEgressObserver_NilIPC_Noop ensures the observer
// closure returned to the proxy is safe to invoke even when IPC is
// not configured — the proxy installs observers unconditionally, so
// the drop must live inside the closure.
func TestSidecar_BuildEgressObserver_NilIPC_Noop(t *testing.T) {
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &Server{logger: silent, ipc: nil}
	obs := s.buildEgressObserver()
	if obs == nil {
		t.Fatal("buildEgressObserver returned nil")
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("egress observer panicked with nil IPC: %v", r)
		}
	}()
	obs("api.anthropic.com", "POST", "anthropic", 200) // must not panic
}

// TestSidecar_BuildEgressObserver_HitsBackend wires the observer up to
// a stub backend and confirms it emits one network.egress entry per
// call. The closure spawns a goroutine, so we synchronise on the
// backend hit instead of sleep-and-pray.
func TestSidecar_BuildEgressObserver_HitsBackend(t *testing.T) {
	hit := make(chan journalEmitRequest, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req journalEmitRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusOK)
		hit <- req
	}))
	defer backend.Close()

	s := newJournalTestServer(backend.URL)
	obs := s.buildEgressObserver()
	obs("api.anthropic.com", "POST", "anthropic", 200)

	select {
	case req := <-hit:
		if req.Type != "network.egress" {
			t.Errorf("type = %q, want network.egress", req.Type)
		}
		if req.Summary != "POST api.anthropic.com → 200" {
			t.Errorf("summary = %q, want 'POST api.anthropic.com → 200'", req.Summary)
		}
		if got, _ := req.Payload["host"].(string); got != "api.anthropic.com" {
			t.Errorf("payload.host = %v, want api.anthropic.com", req.Payload["host"])
		}
		if got, _ := req.Payload["provider"].(string); got != "anthropic" {
			t.Errorf("payload.provider = %v, want anthropic", req.Payload["provider"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("egress observer never reached backend")
	}
}

// TestSidecar_BuildEgressObserver_TransportError_SummaryDiffers covers
// the status==0 branch (transport-level failure where no HTTP status
// was ever returned). The summary text shifts from "→ N" to
// "→ transport error" so the UI can flag the row appropriately.
func TestSidecar_BuildEgressObserver_TransportError_SummaryDiffers(t *testing.T) {
	hit := make(chan journalEmitRequest, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req journalEmitRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusOK)
		hit <- req
	}))
	defer backend.Close()

	s := newJournalTestServer(backend.URL)
	obs := s.buildEgressObserver()
	obs("api.openai.com", "GET", "", 0) // status 0 == transport failed

	select {
	case req := <-hit:
		if req.Summary != "GET api.openai.com → transport error" {
			t.Errorf("summary = %q, want 'GET api.openai.com → transport error'", req.Summary)
		}
		// Provider was empty, must be omitted from payload — the
		// downstream cost ledger keys on provider, NULL-provider rows
		// pollute the dashboard.
		if _, ok := req.Payload["provider"]; ok {
			t.Errorf("provider should be omitted when empty, got payload=%v", req.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("egress observer never reached backend")
	}
}

// TestSidecar_BuildLLMCallObserver_EmptyEvent_Skipped guards the
// drop-empty-events shortcut. CodeRabbit specifically flagged two
// edge cases that must NOT be dropped: cache-only observations and
// RemainingPct=0 (which is the exhausted-quota signal). This test
// covers the third case — totally empty — which IS dropped.
func TestSidecar_BuildLLMCallObserver_EmptyEvent_Skipped(t *testing.T) {
	var hits int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	s := newJournalTestServer(backend.URL)
	obs := s.buildLLMCallObserver()
	// All-zero usage, no quota signal — must be dropped.
	obs(LLMUsage{Provider: "anthropic"}, QuotaInfo{}, "metered", "")

	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("backend hit %d times, want 0 — fully-empty event must skip", n)
	}
}

// TestSidecar_BuildLLMCallObserver_CacheOnlyEvent_NotSkipped is the
// flip side of the empty-event drop: even when InputTokens and
// OutputTokens are zero, a non-zero CachedInputTokens still indicates
// a real billing event (cached input is priced ~10% of fresh input;
// dropping these rows would silently under-bill the workspace).
func TestSidecar_BuildLLMCallObserver_CacheOnlyEvent_NotSkipped(t *testing.T) {
	hit := make(chan sidecarCostRecord, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rec sidecarCostRecord
		_ = json.NewDecoder(r.Body).Decode(&rec)
		w.WriteHeader(http.StatusOK)
		hit <- rec
	}))
	defer backend.Close()

	s := newJournalTestServer(backend.URL)
	obs := s.buildLLMCallObserver()
	// Only cached input — must still emit.
	obs(LLMUsage{
		Provider:          "anthropic",
		Model:             "claude-opus-4-7",
		CachedInputTokens: 250,
	}, QuotaInfo{}, "metered", "")

	select {
	case rec := <-hit:
		if rec.CachedInputTokens != 250 {
			t.Errorf("cached input = %d, want 250", rec.CachedInputTokens)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cache-only event was dropped — billing miss")
	}
}

// TestSidecar_BuildLLMCallObserver_NilIPC_Noop mirrors the egress
// counterpart: the closure must be safe to call before IPC is wired
// (e.g. unit tests, or the early-boot window before the orchestrator
// stamps the config).
func TestSidecar_BuildLLMCallObserver_NilIPC_Noop(t *testing.T) {
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &Server{logger: silent, ipc: nil}
	obs := s.buildLLMCallObserver()
	if obs == nil {
		t.Fatal("buildLLMCallObserver returned nil")
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("llm observer panicked with nil IPC: %v", r)
		}
	}()
	obs(LLMUsage{Provider: "anthropic", InputTokens: 100}, QuotaInfo{}, "metered", "")
}
